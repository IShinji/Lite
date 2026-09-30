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

func TestValidatePingLossNotification(t *testing.T) {
	valid := models.PingLossNotification{
		Client:          "client-a",
		TaskId:          1,
		Enable:          true,
		LossEnabled:     true,
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  1,
		CooldownSeconds: 300,
	}
	assert.NoError(t, ValidatePingLossNotification(valid))

	tests := []struct {
		name   string
		mutate func(*models.PingLossNotification)
	}{
		{name: "missing client", mutate: func(n *models.PingLossNotification) { n.Client = "" }},
		{name: "missing task", mutate: func(n *models.PingLossNotification) { n.TaskId = 0 }},
		{name: "short window", mutate: func(n *models.PingLossNotification) { n.WindowSeconds = 59 }},
		{name: "invalid threshold", mutate: func(n *models.PingLossNotification) { n.LossThreshold = 0 }},
		{name: "invalid samples", mutate: func(n *models.PingLossNotification) { n.MinimumSamples = 0 }},
		{name: "short cooldown", mutate: func(n *models.PingLossNotification) { n.CooldownSeconds = 59 }},
		{name: "no category", mutate: func(n *models.PingLossNotification) { n.LossEnabled = false; n.LatencyEnabled = false }},
		{name: "fixed high not greater", mutate: func(n *models.PingLossNotification) {
			n.LatencyEnabled = true
			n.FixedBaselineMs = 100
			n.LowLatencyThresholdMs = 120
			n.HighLatencyThresholdMs = 120
			n.LatencyWindowSeconds = 300
			n.LatencyMinimumSamples = 3
			n.LatencyCooldownSeconds = 1800
		}},
		{name: "fixed baseline outside bounds", mutate: func(n *models.PingLossNotification) {
			n.LatencyEnabled = true
			n.FixedBaselineMs = 250
			n.LowLatencyThresholdMs = 50
			n.HighLatencyThresholdMs = 200
			n.LatencyWindowSeconds = 300
			n.LatencyMinimumSamples = 3
			n.LatencyCooldownSeconds = 1800
		}},
		{name: "adaptive lower too high", mutate: func(n *models.PingLossNotification) {
			n.LatencyEnabled = true
			n.AdaptiveBaselineEnabled = true
			n.LatencyWindowSeconds = 300
			n.LatencyMinimumSamples = 3
			n.LatencyCooldownSeconds = 1800
			n.AdaptiveLowerDeviationPercent = 100
			n.AdaptiveUpperDeviationPercent = 20
			n.BaselineWindowSeconds = 86400
			n.BaselineMinimumSamples = 30
		}},
		{name: "baseline window shorter than latency", mutate: func(n *models.PingLossNotification) {
			n.LatencyEnabled = true
			n.AdaptiveBaselineEnabled = true
			n.LatencyWindowSeconds = 3600
			n.LatencyMinimumSamples = 3
			n.LatencyCooldownSeconds = 1800
			n.AdaptiveLowerDeviationPercent = 20
			n.AdaptiveUpperDeviationPercent = 20
			n.BaselineWindowSeconds = 300
			n.BaselineMinimumSamples = 30
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			assert.Error(t, ValidatePingLossNotification(candidate))
		})
	}

	adaptive := valid
	adaptive.LatencyEnabled = true
	adaptive.AdaptiveBaselineEnabled = true
	adaptive.LatencyWindowSeconds = 300
	adaptive.LatencyMinimumSamples = 3
	adaptive.LatencyCooldownSeconds = 1800
	adaptive.AdaptiveLowerDeviationPercent = 10
	adaptive.AdaptiveUpperDeviationPercent = 30
	adaptive.BaselineWindowSeconds = 86400
	adaptive.BaselineMinimumSamples = 30
	assert.NoError(t, ValidatePingLossNotification(adaptive))

	fixed := valid
	fixed.LatencyEnabled = true
	fixed.FixedBaselineMs = 100
	fixed.LowLatencyThresholdMs = 50
	fixed.HighLatencyThresholdMs = 200
	fixed.LatencyWindowSeconds = 300
	fixed.LatencyMinimumSamples = 3
	fixed.LatencyCooldownSeconds = 1800
	assert.NoError(t, ValidatePingLossNotification(fixed))

	disabledLatency := valid
	disabledLatency.HighLatencyThresholdMs = 0
	disabledLatency.LowLatencyThresholdMs = 10
	assert.NoError(t, ValidatePingLossNotification(disabledLatency))
}

func TestUpsertPingLossNotificationsCreatesAndUpdatesTargets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-upsert?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a", Name: "Server A"}).Error)
	task := models.PingTask{
		Name:     "Public DNS",
		Clients:  models.StringArray{"client-a"},
		Type:     "icmp",
		Target:   "1.1.1.1",
		Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)

	first := &models.PingLossNotification{
		Client:          "client-a",
		TaskId:          task.Id,
		Enable:          true,
		LossEnabled:     true,
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  3,
		CooldownSeconds: 300,
	}
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{first}))

	var created models.PingLossNotification
	require.NoError(t, db.First(&created).Error)
	require.NotZero(t, created.Id)
	assert.True(t, created.Enable)
	assert.True(t, created.LossEnabled)
	assert.False(t, created.LatencyEnabled)
	assert.Equal(t, 5.0, created.LossThreshold)

	lastNotified := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, db.Model(&created).Updates(map[string]any{
		"last_notified": lastNotified,
		"alert_active":  true,
	}).Error)
	updated := &models.PingLossNotification{
		Client:          "client-a",
		TaskId:          task.Id,
		Enable:          false,
		LossEnabled:     true,
		WindowSeconds:   120,
		LossThreshold:   12.5,
		MinimumSamples:  8,
		CooldownSeconds: 600,
	}
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{updated}))

	var count int64
	require.NoError(t, db.Model(&models.PingLossNotification{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var got models.PingLossNotification
	require.NoError(t, db.First(&got).Error)
	assert.Equal(t, created.Id, got.Id)
	assert.False(t, got.Enable)
	assert.Equal(t, 120, got.WindowSeconds)
	assert.Equal(t, 12.5, got.LossThreshold)
	assert.Equal(t, 8, got.MinimumSamples)
	assert.Equal(t, 600, got.CooldownSeconds)
	require.NotNil(t, got.LastNotified)
	assert.WithinDuration(t, lastNotified, *got.LastNotified, time.Second)
	assert.False(t, got.AlertActive, "changing packet-loss parameters starts a new incident")
}

func TestUpsertPingLossNotificationsRollsBackBatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-rollback?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-rollback"}).Error)
	task := models.PingTask{
		Name: "DNS", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "8.8.8.8", Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)

	valid := &models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true, LossEnabled: true,
		WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
	}
	invalid := &models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true, LossEnabled: true,
		WindowSeconds: 30, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
	}
	require.Error(t, upsertPingLossNotifications(db, []*models.PingLossNotification{valid, invalid}))

	var count int64
	require.NoError(t, db.Model(&models.PingLossNotification{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpsertPingLossNotificationsResetsLatencyWithoutTouchingLoss(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-latency-reset?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.PingTask{}, &models.PingLossNotification{}))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-latency"}).Error)
	task := models.PingTask{Name: "API", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "api.example.com", Interval: 10}
	require.NoError(t, db.Create(&task).Error)
	baseline := 100.0
	require.NoError(t, db.Create(&models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true, LossEnabled: true, LatencyEnabled: true,
		AdaptiveBaselineEnabled: true,
		WindowSeconds:           60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
		LatencyWindowSeconds: 300, LatencyMinimumSamples: 3, LatencyCooldownSeconds: 1800,
		FixedBaselineMs: 100, LowLatencyThresholdMs: 80, HighLatencyThresholdMs: 120,
		AdaptiveLowerDeviationPercent: 20, AdaptiveUpperDeviationPercent: 20,
		BaselineWindowSeconds: 86400, BaselineMinimumSamples: 30,
		AlertActive: true, LatencyIncidentNotified: true, LatencyAlertState: models.LatencyAlertHigh,
		AdaptiveBaselineMs: &baseline, AdaptiveBaselineStatus: models.AdaptiveBaselineFrozen,
	}).Error)

	before := time.Now().UTC()
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{{
		Client: "client-a", TaskId: task.Id, Enable: true, LossEnabled: true, LatencyEnabled: true,
		AdaptiveBaselineEnabled: true,
		WindowSeconds:           60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
		LatencyWindowSeconds: 300, LatencyMinimumSamples: 3, LatencyCooldownSeconds: 1800,
		FixedBaselineMs: 100, LowLatencyThresholdMs: 70, HighLatencyThresholdMs: 130,
		AdaptiveLowerDeviationPercent: 20, AdaptiveUpperDeviationPercent: 20,
		BaselineWindowSeconds: 86400, BaselineMinimumSamples: 30,
	}}))

	var got models.PingLossNotification
	require.NoError(t, db.First(&got).Error)
	assert.True(t, got.AlertActive, "latency judgment changes must not clear an active packet-loss incident")
	assert.Equal(t, models.LatencyAlertNormal, got.LatencyAlertState)
	assert.False(t, got.LatencyIncidentNotified)
	require.NotNil(t, got.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, got.AdaptiveBaselineStatus)
	require.NotNil(t, got.AdaptiveBaselineResumeAt)
	assert.WithinDuration(t, before.Add(300*time.Second), got.AdaptiveBaselineResumeAt.UTC(), 5*time.Second)
}
