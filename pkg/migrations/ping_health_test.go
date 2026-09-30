package migrations

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func pingHealthMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ping-health-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&appconfig.ConfigItem{}, &models.Client{}, &models.PingTask{}, &models.PingLossNotification{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigratePingHealthLossEnabledMarksLegacyRulesOnce(t *testing.T) {
	db := pingHealthMigrationDB(t)
	if err := db.Create(&models.Client{UUID: "client-a", Token: "token-a", Name: "A"}).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "DNS", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "1.1.1.1", Interval: 10}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	legacy := models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true,
		WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
		AlertActive: true,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&legacy).Update("loss_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePingHealthLossEnabled(db); err != nil {
		t.Fatal(err)
	}
	var got models.PingLossNotification
	if err := db.First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if !got.LossEnabled {
		t.Fatal("legacy rule should enable packet-loss after migration")
	}
	if got.LatencyEnabled || got.AdaptiveBaselineEnabled {
		t.Fatalf("legacy rule should leave latency off: %+v", got)
	}
	if !got.AlertActive {
		t.Fatal("packet-loss activity must be preserved")
	}

	if err := db.Model(&got).Updates(map[string]any{
		"loss_enabled":              false,
		"latency_enabled":           true,
		"adaptive_baseline_enabled": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePingHealthLossEnabled(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.LossEnabled || !got.LatencyEnabled || !got.AdaptiveBaselineEnabled {
		t.Fatalf("repeat migration must not overwrite user config: %+v", got)
	}
}

func TestMigratePingHealthIncidentNotifiedMarksDeliveredIncidentsOnce(t *testing.T) {
	db := pingHealthMigrationDB(t)
	if err := db.Create([]models.Client{
		{UUID: "loss-delivered", Token: "token-loss-delivered", Name: "Loss delivered"},
		{UUID: "loss-pending", Token: "token-loss-pending", Name: "Loss pending"},
		{UUID: "loss-idle", Token: "token-loss-idle", Name: "Loss idle"},
		{UUID: "latency-delivered", Token: "token-latency-delivered", Name: "Latency delivered"},
		{UUID: "latency-pending", Token: "token-latency-pending", Name: "Latency pending"},
		{UUID: "latency-idle", Token: "token-latency-idle", Name: "Latency idle"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "DNS", Clients: models.StringArray{"loss-delivered"}, Type: "icmp", Target: "1.1.1.1", Interval: 10}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	notified := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	rules := []models.PingLossNotification{
		{Client: "loss-delivered", TaskId: task.Id, Enable: true, AlertActive: true, LastNotified: &notified, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "loss-pending", TaskId: task.Id, Enable: true, AlertActive: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "loss-idle", TaskId: task.Id, Enable: true, AlertActive: false, LastNotified: &notified, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "latency-delivered", TaskId: task.Id, Enable: true, LatencyAlertState: models.LatencyAlertHigh, LatencyLastNotified: &notified, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "latency-pending", TaskId: task.Id, Enable: true, LatencyAlertState: models.LatencyAlertLow, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "latency-idle", TaskId: task.Id, Enable: true, LatencyAlertState: models.LatencyAlertNormal, LatencyLastNotified: &notified, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}
	if err := db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigratePingHealthIncidentNotified(db); err != nil {
		t.Fatal(err)
	}
	got := map[string]models.PingLossNotification{}
	var rows []models.PingLossNotification
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		got[row.Client] = row
	}
	if !got["loss-delivered"].LossIncidentNotified || got["loss-delivered"].LatencyIncidentNotified {
		t.Fatalf("delivered loss incident = %+v", got["loss-delivered"])
	}
	if got["loss-pending"].LossIncidentNotified || got["latency-pending"].LatencyIncidentNotified {
		t.Fatal("undelivered incidents must stay unmarked")
	}
	if got["loss-idle"].LossIncidentNotified || got["latency-idle"].LatencyIncidentNotified {
		t.Fatal("finished events must stay unmarked")
	}
	if !got["latency-delivered"].LatencyIncidentNotified || got["latency-delivered"].LossIncidentNotified {
		t.Fatalf("delivered latency incident = %+v", got["latency-delivered"])
	}

	if err := db.Model(&models.PingLossNotification{}).Where("client = ?", "loss-pending").Update("loss_incident_notified", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePingHealthIncidentNotified(db); err != nil {
		t.Fatal(err)
	}
	var pending models.PingLossNotification
	if err := db.Where("client = ?", "loss-pending").First(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if !pending.LossIncidentNotified {
		t.Fatal("repeat migration must not overwrite the delivery flag")
	}
}
