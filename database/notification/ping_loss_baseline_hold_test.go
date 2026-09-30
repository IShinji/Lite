package notification

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDisableLatencyMonitoringHoldsFrozenBaseline(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "disable-latency")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertHigh, true)
	before := time.Now().UTC()
	next := adaptiveIncidentPayload(task, true, true)
	next.LatencyEnabled = false
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

	got := reloadAdaptiveIncident(t, db)
	assertAdaptiveRecoveryHold(t, got, before, 300)
	assert.True(t, got.AlertActive)
	assert.False(t, got.LatencyEnabled)
}

func TestDisableLatencyMonitoringUsesStoredWindowWhenRequestOmitsIt(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "disable-latency-window")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertHigh, true)
	before := time.Now().UTC()
	next := adaptiveIncidentPayload(task, true, true)
	next.LatencyEnabled = false
	next.LatencyWindowSeconds = 0
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

	got := reloadAdaptiveIncident(t, db)
	assertAdaptiveRecoveryHold(t, got, before, 300)
}

func TestDisableMasterSwitchHoldsLowLatencyBaseline(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "disable-master")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertLow, true)
	before := time.Now().UTC()
	next := adaptiveIncidentPayload(task, false, true)
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

	got := reloadAdaptiveIncident(t, db)
	assertAdaptiveRecoveryHold(t, got, before, 300)
	assert.False(t, got.Enable)
	assert.False(t, got.AlertActive)
}

func TestReenableLatencyMonitoringKeepsRecoveryHold(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "reenable-latency")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertHigh, true)
	disabled := adaptiveIncidentPayload(task, true, true)
	disabled.LatencyEnabled = false
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&disabled}))
	held := reloadAdaptiveIncident(t, db)
	require.Equal(t, models.AdaptiveBaselineRecoveryHold, held.AdaptiveBaselineStatus)
	require.NotNil(t, held.AdaptiveBaselineResumeAt)

	enabled := adaptiveIncidentPayload(task, true, true)
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&enabled}))
	got := reloadAdaptiveIncident(t, db)
	assert.True(t, got.LatencyEnabled)
	assert.Equal(t, models.LatencyAlertNormal, got.LatencyAlertState)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, got.AdaptiveBaselineStatus)
	require.NotNil(t, got.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)
	require.NotNil(t, got.AdaptiveBaselineResumeAt)
	assert.Equal(t, held.AdaptiveBaselineResumeAt.UTC(), got.AdaptiveBaselineResumeAt.UTC())
}

func TestDeviationChangeHoldsActiveBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		upper float64
		lower float64
	}{
		{name: "high", state: models.LatencyAlertHigh, upper: 40, lower: 20},
		{name: "low", state: models.LatencyAlertLow, upper: 20, lower: 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, task := openAdaptiveIncidentDB(t, "deviation-"+tc.name)
			seedAdaptiveIncident(t, db, task, tc.state, true)
			before := time.Now().UTC()
			next := adaptiveIncidentPayload(task, true, true)
			next.AdaptiveUpperDeviationPercent = tc.upper
			next.AdaptiveLowerDeviationPercent = tc.lower
			require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

			got := reloadAdaptiveIncident(t, db)
			assertAdaptiveRecoveryHold(t, got, before, 300)
			assert.True(t, got.AlertActive)
			assert.Equal(t, tc.upper, got.AdaptiveUpperDeviationPercent)
			assert.Equal(t, tc.lower, got.AdaptiveLowerDeviationPercent)
		})
	}
}

func TestBaselineIdentityChangeWinsOverRecoveryHold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*models.PingLossNotification)
	}{
		{name: "window", mutate: func(next *models.PingLossNotification) { next.BaselineWindowSeconds = 172800 }},
		{name: "samples", mutate: func(next *models.PingLossNotification) { next.BaselineMinimumSamples = 60 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, task := openAdaptiveIncidentDB(t, "baseline-reset-"+tc.name)
			seedAdaptiveIncident(t, db, task, models.LatencyAlertHigh, true)
			next := adaptiveIncidentPayload(task, true, true)
			tc.mutate(&next)
			require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

			got := reloadAdaptiveIncident(t, db)
			assert.Equal(t, models.LatencyAlertNormal, got.LatencyAlertState)
			assert.False(t, got.LatencyIncidentNotified)
			assert.Nil(t, got.AdaptiveBaselineMs)
			assert.Equal(t, models.AdaptiveBaselineWarming, got.AdaptiveBaselineStatus)
			assert.Nil(t, got.AdaptiveBaselineResumeAt)
			assert.True(t, got.AlertActive)
		})
	}
}

func TestReadyBaselineSaveDoesNotStartRecoveryHold(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "ready-save")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertNormal, false)
	require.NoError(t, db.Model(&models.PingLossNotification{}).Where("task_id = ?", task.Id).Updates(map[string]any{
		"adaptive_baseline_status":  models.AdaptiveBaselineReady,
		"latency_incident_notified": false,
	}).Error)
	next := adaptiveIncidentPayload(task, true, true)
	next.LossThreshold = 8
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

	got := reloadAdaptiveIncident(t, db)
	assert.Equal(t, models.LatencyAlertNormal, got.LatencyAlertState)
	assert.Equal(t, models.AdaptiveBaselineReady, got.AdaptiveBaselineStatus)
	assert.Nil(t, got.AdaptiveBaselineResumeAt)
	require.NotNil(t, got.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)
	assert.Equal(t, 8.0, got.LossThreshold)
}

func TestUpsertRepairsNormalFrozenBaseline(t *testing.T) {
	db, task := openAdaptiveIncidentDB(t, "repair-frozen")
	seedAdaptiveIncident(t, db, task, models.LatencyAlertNormal, false)
	require.NoError(t, db.Model(&models.PingLossNotification{}).Where("task_id = ?", task.Id).Updates(map[string]any{
		"adaptive_baseline_status":  models.AdaptiveBaselineFrozen,
		"latency_incident_notified": false,
	}).Error)
	before := time.Now().UTC()
	next := adaptiveIncidentPayload(task, true, true)
	next.LossThreshold = 8
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{&next}))

	got := reloadAdaptiveIncident(t, db)
	assertAdaptiveRecoveryHold(t, got, before, 300)
	assert.Equal(t, 8.0, got.LossThreshold)
}

func openAdaptiveIncidentDB(t *testing.T, name string) (*gorm.DB, models.PingTask) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.PingTask{}, &models.PingLossNotification{}))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-" + name}).Error)
	task := models.PingTask{Name: "API", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "api.example.com", Interval: 10}
	require.NoError(t, db.Create(&task).Error)
	return db, task
}

func seedAdaptiveIncident(t *testing.T, db *gorm.DB, task models.PingTask, state string, notified bool) {
	t.Helper()
	baseline := 100.0
	require.NoError(t, db.Create(&models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true, LossEnabled: true, LatencyEnabled: true,
		AdaptiveBaselineEnabled: true, AlertActive: true, LatencyIncidentNotified: notified,
		WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
		LatencyWindowSeconds: 300, LatencyMinimumSamples: 3, LatencyCooldownSeconds: 1800,
		AdaptiveLowerDeviationPercent: 20, AdaptiveUpperDeviationPercent: 20,
		BaselineWindowSeconds: 86400, BaselineMinimumSamples: 30,
		LatencyAlertState: state, AdaptiveBaselineMs: &baseline,
		AdaptiveBaselineStatus: models.AdaptiveBaselineFrozen,
	}).Error)
}

func adaptiveIncidentPayload(task models.PingTask, enable, latency bool) models.PingLossNotification {
	return models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: enable, LossEnabled: true, LatencyEnabled: latency,
		AdaptiveBaselineEnabled: true,
		WindowSeconds:           60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
		LatencyWindowSeconds: 300, LatencyMinimumSamples: 3, LatencyCooldownSeconds: 1800,
		AdaptiveLowerDeviationPercent: 20, AdaptiveUpperDeviationPercent: 20,
		BaselineWindowSeconds: 86400, BaselineMinimumSamples: 30,
	}
}

func reloadAdaptiveIncident(t *testing.T, db *gorm.DB) models.PingLossNotification {
	t.Helper()
	var got models.PingLossNotification
	require.NoError(t, db.First(&got).Error)
	return got
}

func assertAdaptiveRecoveryHold(t *testing.T, got models.PingLossNotification, before time.Time, windowSeconds int) {
	t.Helper()
	assert.Equal(t, models.LatencyAlertNormal, got.LatencyAlertState)
	assert.False(t, got.LatencyIncidentNotified)
	require.NotNil(t, got.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, got.AdaptiveBaselineStatus)
	require.NotNil(t, got.AdaptiveBaselineResumeAt)
	assert.WithinDuration(t, before.Add(time.Duration(windowSeconds)*time.Second), got.AdaptiveBaselineResumeAt.UTC(), 5*time.Second)
	assert.Nil(t, got.LatencyActiveSince)
}
