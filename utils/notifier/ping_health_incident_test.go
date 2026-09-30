package notifier

import (
	"errors"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSilentLossRecoveryAfterUndeliveredAlert(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	rule := models.PingLossNotification{
		Enable: true, LossEnabled: true, LossThreshold: 5, MinimumSamples: 10, CooldownSeconds: 1800,
	}
	alertStats := pingLossStats{Total: 20, Lost: 2}
	action := evaluatePingLossNotification(rule, alertStats, now)
	assert.Equal(t, pingLossNotificationAlert, action)
	got, sends := persistLossScan(t, rule, alertStats, now, action, false)
	assert.Equal(t, 1, sends)
	assert.True(t, got.AlertActive)
	assert.False(t, got.LossIncidentNotified)
	assert.Nil(t, got.LastNotified)

	later := now.Add(15 * time.Second)
	okStats := pingLossStats{Total: 20, Lost: 1}
	silent := evaluatePingLossNotification(got, okStats, later)
	assert.Equal(t, pingLossNotificationSilentEnd, silent)
	assert.False(t, pingLossActionRequiresSend(silent))
	ended, sends := persistLossScan(t, got, okStats, later, silent, true)
	assert.Equal(t, 0, sends)
	assert.False(t, ended.AlertActive)
	assert.False(t, ended.LossIncidentNotified)
}

func TestDeliveredLossRecoverySendsAndRetries(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	rule := models.PingLossNotification{
		Enable: true, LossEnabled: true, LossThreshold: 5, MinimumSamples: 10, CooldownSeconds: 1800,
	}
	alertStats := pingLossStats{Total: 20, Lost: 2}
	action := evaluatePingLossNotification(rule, alertStats, now)
	delivered, sends := persistLossScan(t, rule, alertStats, now, action, true)
	assert.Equal(t, 1, sends)
	assert.True(t, delivered.AlertActive)
	assert.True(t, delivered.LossIncidentNotified)

	later := now.Add(15 * time.Second)
	okStats := pingLossStats{Total: 20, Lost: 1}
	recovery := evaluatePingLossNotification(delivered, okStats, later)
	assert.Equal(t, pingLossNotificationRecovery, recovery)
	failed, sends := persistLossScan(t, delivered, okStats, later, recovery, false)
	assert.Equal(t, 1, sends)
	assert.True(t, failed.AlertActive)
	assert.True(t, failed.LossIncidentNotified)

	retry := evaluatePingLossNotification(failed, okStats, later.Add(15*time.Second))
	assert.Equal(t, pingLossNotificationRecovery, retry)
	ended, sends := persistLossScan(t, failed, okStats, later.Add(15*time.Second), retry, true)
	assert.Equal(t, 1, sends)
	assert.False(t, ended.AlertActive)
	assert.False(t, ended.LossIncidentNotified)
}

func TestSilentHighLatencyRecoveryAfterUndeliveredAlert(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	highStats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, highStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, eval.Action)
	got, sends := persistLatencyScan(t, rule, highStats, now, eval, false)
	assert.Equal(t, 1, sends)
	assert.Equal(t, models.LatencyAlertHigh, got.NormalizedLatencyAlertState())
	assert.Equal(t, models.AdaptiveBaselineFrozen, got.NormalizedAdaptiveBaselineStatus())
	assert.False(t, got.LatencyIncidentNotified)

	later := now.Add(15 * time.Second)
	okStats := coveredLatencyStats(100, 81, 119, 18, later.Add(-5*time.Minute), later)
	silent := evaluateLatencyAnomaly(got, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationSilentEnd, silent.Action)
	assert.False(t, pingLatencyActionRequiresSend(silent.Action))
	ended, sends := persistLatencyScan(t, got, okStats, later, silent, true)
	assert.Equal(t, 0, sends)
	assert.Equal(t, models.LatencyAlertNormal, ended.NormalizedLatencyAlertState())
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, ended.NormalizedAdaptiveBaselineStatus())
	assert.False(t, ended.LatencyIncidentNotified)
	require.True(t, ended.HasAdaptiveBaseline())
	assert.Equal(t, 100.0, *ended.AdaptiveBaselineMs)
	require.NotNil(t, ended.AdaptiveBaselineResumeAt)
	assert.Equal(t, later.Add(5*time.Minute).UTC(), ended.AdaptiveBaselineResumeAt.UTC())
}

func TestSilentLowLatencyRecoveryAfterUndeliveredAlert(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	lowStats := coveredLatencyStats(70, 60, 75, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, lowStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, eval.Action)
	got, _ := persistLatencyScan(t, rule, lowStats, now, eval, false)
	assert.False(t, got.LatencyIncidentNotified)

	later := now.Add(15 * time.Second)
	okStats := coveredLatencyStats(100, 81, 119, 18, later.Add(-5*time.Minute), later)
	silent := evaluateLatencyAnomaly(got, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationSilentEnd, silent.Action)
	ended, sends := persistLatencyScan(t, got, okStats, later, silent, true)
	assert.Equal(t, 0, sends)
	assert.Equal(t, models.LatencyAlertNormal, ended.NormalizedLatencyAlertState())
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, ended.NormalizedAdaptiveBaselineStatus())
	assert.Equal(t, 100.0, *ended.AdaptiveBaselineMs)
}

func TestDeliveredLatencyRecoverySendsAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats metricstore.PingHealthStats
		alert pingLatencyNotificationAction
		state string
	}{
		{name: "high", stats: coveredLatencyStats(130, 120, 140, 18, time.Time{}, time.Time{}), alert: pingLatencyNotificationAlertHigh, state: models.LatencyAlertHigh},
		{name: "low", stats: coveredLatencyStats(70, 60, 75, 18, time.Time{}, time.Time{}), alert: pingLatencyNotificationAlertLow, state: models.LatencyAlertLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			windowStart := now.Add(-5 * time.Minute)
			tc.stats.FirstSuccessfulAt = &windowStart
			tc.stats.LastSuccessfulAt = &now
			rule := adaptiveLatencyRule(100)
			eval := evaluateLatencyAnomaly(rule, tc.stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
			assert.Equal(t, tc.alert, eval.Action)
			delivered, sends := persistLatencyScan(t, rule, tc.stats, now, eval, true)
			assert.Equal(t, 1, sends)
			assert.Equal(t, tc.state, delivered.NormalizedLatencyAlertState())
			assert.True(t, delivered.LatencyIncidentNotified)

			later := now.Add(15 * time.Second)
			okStats := coveredLatencyStats(100, 81, 119, 18, later.Add(-5*time.Minute), later)
			recovery := evaluateLatencyAnomaly(delivered, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
			assert.Equal(t, pingLatencyNotificationRecovery, recovery.Action)
			failed, sends := persistLatencyScan(t, delivered, okStats, later, recovery, false)
			assert.Equal(t, 1, sends)
			assert.Equal(t, tc.state, failed.NormalizedLatencyAlertState())
			assert.Equal(t, models.AdaptiveBaselineFrozen, failed.NormalizedAdaptiveBaselineStatus())
			assert.True(t, failed.LatencyIncidentNotified)

			retry := evaluateLatencyAnomaly(failed, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
			assert.Equal(t, pingLatencyNotificationRecovery, retry.Action)
			ended, sends := persistLatencyScan(t, failed, okStats, later, retry, true)
			assert.Equal(t, 1, sends)
			assert.Equal(t, models.LatencyAlertNormal, ended.NormalizedLatencyAlertState())
			assert.Equal(t, models.AdaptiveBaselineRecoveryHold, ended.NormalizedAdaptiveBaselineStatus())
			assert.False(t, ended.LatencyIncidentNotified)
			assert.Equal(t, 100.0, *ended.AdaptiveBaselineMs)
		})
	}
}

func TestSilentLatencyRecoveryAfterUndeliveredFlip(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	stale := now.Add(-time.Minute)

	high := adaptiveLatencyRule(100)
	high.LatencyAlertState = models.LatencyAlertHigh
	high.LatencyIncidentNotified = true
	high.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	high.LatencyLastNotified = &stale
	lowStats := coveredLatencyStats(70, 60, 75, 18, windowStart, now)
	flipLow := evaluateLatencyAnomaly(high, lowStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationFlipLow, flipLow.Action)
	gotLow, _ := persistLatencyScan(t, high, lowStats, now, flipLow, false)
	assert.Equal(t, models.LatencyAlertLow, gotLow.NormalizedLatencyAlertState())
	assert.False(t, gotLow.LatencyIncidentNotified)
	assert.Equal(t, 100.0, *gotLow.AdaptiveBaselineMs)

	later := now.Add(15 * time.Second)
	okStats := coveredLatencyStats(100, 81, 119, 18, later.Add(-5*time.Minute), later)
	silentLow := evaluateLatencyAnomaly(gotLow, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationSilentEnd, silentLow.Action)
	endedLow, sends := persistLatencyScan(t, gotLow, okStats, later, silentLow, true)
	assert.Equal(t, 0, sends)
	assert.Equal(t, models.LatencyAlertNormal, endedLow.NormalizedLatencyAlertState())
	assert.Equal(t, 100.0, *endedLow.AdaptiveBaselineMs)

	low := adaptiveLatencyRule(100)
	low.LatencyAlertState = models.LatencyAlertLow
	low.LatencyIncidentNotified = true
	low.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	low.LatencyLastNotified = &stale
	highStats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	flipHigh := evaluateLatencyAnomaly(low, highStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationFlipHigh, flipHigh.Action)
	gotHigh, _ := persistLatencyScan(t, low, highStats, now, flipHigh, false)
	assert.Equal(t, models.LatencyAlertHigh, gotHigh.NormalizedLatencyAlertState())
	assert.False(t, gotHigh.LatencyIncidentNotified)
	silentHigh := evaluateLatencyAnomaly(gotHigh, okStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationSilentEnd, silentHigh.Action)
	endedHigh, sends := persistLatencyScan(t, gotHigh, okStats, later, silentHigh, true)
	assert.Equal(t, 0, sends)
	assert.Equal(t, models.LatencyAlertNormal, endedHigh.NormalizedLatencyAlertState())
	assert.Equal(t, 100.0, *endedHigh.AdaptiveBaselineMs)
}

func TestPersistFailureKeepsIncidentDelivered(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyCooldownSeconds = 60
	highStats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	first := evaluateLatencyAnomaly(rule, highStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	delivered, _ := persistLatencyScan(t, rule, highStats, now, first, true)
	assert.True(t, delivered.LatencyIncidentNotified)

	later := now.Add(2 * time.Minute)
	persist := evaluateLatencyAnomaly(delivered, highStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationPersist, persist.Action)
	failed, _ := persistLatencyScan(t, delivered, highStats, later, persist, false)
	assert.True(t, failed.LatencyIncidentNotified)
	require.NotNil(t, failed.LatencyLastNotified)
	assert.Equal(t, now.UTC(), failed.LatencyLastNotified.UTC())

	okNow := later.Add(15 * time.Second)
	okStats := coveredLatencyStats(100, 81, 119, 18, okNow.Add(-5*time.Minute), okNow)
	recovery := evaluateLatencyAnomaly(failed, okStats, metricstore.PingBaselineCandidate{}, okNow.Add(-5*time.Minute), okNow, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, recovery.Action)
	ended, sends := persistLatencyScan(t, failed, okStats, okNow, recovery, true)
	assert.Equal(t, 1, sends)
	assert.Equal(t, models.LatencyAlertNormal, ended.NormalizedLatencyAlertState())
	assert.False(t, ended.LatencyIncidentNotified)
}

func persistLossScan(
	t *testing.T,
	rule models.PingLossNotification,
	stats pingLossStats,
	now time.Time,
	action pingLossNotificationAction,
	sendOK bool,
) (models.PingLossNotification, int) {
	t.Helper()
	sends := 0
	previous := sendPingHealthEvent
	sendPingHealthEvent = func(models.EventMessage) error {
		sends++
		if !sendOK {
			return errors.New("send failed")
		}
		return nil
	}
	t.Cleanup(func() { sendPingHealthEvent = previous })
	sent := true
	if pingLossActionRequiresSend(action) {
		if err := sendPingLossNotification(rule, stats, now, action); err != nil {
			sent = false
		}
	}
	got := persistPingHealthRule(t, rule, pingHealthPersistentUpdates(rule, now, action, sent, pingLatencyEvaluation{Notification: rule}, false, true))
	return got, sends
}

func persistLatencyScan(
	t *testing.T,
	rule models.PingLossNotification,
	stats metricstore.PingHealthStats,
	now time.Time,
	eval pingLatencyEvaluation,
	sendOK bool,
) (models.PingLossNotification, int) {
	t.Helper()
	sends := 0
	previous := sendPingHealthEvent
	sendPingHealthEvent = func(models.EventMessage) error {
		sends++
		if !sendOK {
			return errors.New("send failed")
		}
		return nil
	}
	t.Cleanup(func() { sendPingHealthEvent = previous })
	sent := true
	if pingLatencyActionRequiresSend(eval.Action) {
		if err := sendPingLatencyNotification(eval.Notification, stats, now, eval.Action); err != nil {
			sent = false
		}
	}
	got := persistPingHealthRule(t, rule, pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, sent))
	return got, sends
}
