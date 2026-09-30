package migrations

import (
	"fmt"

	"github.com/nuomiiiii/lite/database/models"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const pingHealthLossEnabledMigrationKey = "internal_ping_health_loss_enabled_migrated"

// MigratePingHealthLossEnabled marks existing ping-loss rules as packet-loss
// alerts so upgrades keep the previous behavior. New rows must set the flag
// explicitly. The migration is idempotent.
func MigratePingHealthLossEnabled(db *gorm.DB) error {
	if pingHealthLossEnabledMigrationDone(db) {
		return nil
	}
	if !db.Migrator().HasTable(&models.PingLossNotification{}) ||
		!db.Migrator().HasColumn(&models.PingLossNotification{}, "loss_enabled") {
		return markPingHealthLossEnabledMigrationDone(db)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.PingLossNotification{}).
			Where("1 = 1").
			Update("loss_enabled", true).Error; err != nil {
			return fmt.Errorf("migrate existing ping loss rules: %w", err)
		}
		return markPingHealthLossEnabledMigrationDone(tx)
	})
}

func pingHealthLossEnabledMigrationDone(db *gorm.DB) bool {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		return false
	}
	var item appconfig.ConfigItem
	if err := db.Where("key = ?", pingHealthLossEnabledMigrationKey).First(&item).Error; err != nil {
		return false
	}
	return item.Value == "true"
}

func markPingHealthLossEnabledMigrationDone(db *gorm.DB) error {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
			return fmt.Errorf("create config item table: %w", err)
		}
	}
	item := appconfig.ConfigItem{Key: pingHealthLossEnabledMigrationKey, Value: "true"}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&item).Error
}

const pingHealthIncidentNotifiedMigrationKey = "internal_ping_health_incident_notified_migrated"

// MigratePingHealthIncidentNotified marks in-progress incidents that already
// have a successful notification timestamp as delivered, so upgrades keep
// recovery messages. New events start as not delivered.
func MigratePingHealthIncidentNotified(db *gorm.DB) error {
	if pingHealthIncidentNotifiedMigrationDone(db) {
		return nil
	}
	if !db.Migrator().HasTable(&models.PingLossNotification{}) ||
		!db.Migrator().HasColumn(&models.PingLossNotification{}, "loss_incident_notified") ||
		!db.Migrator().HasColumn(&models.PingLossNotification{}, "latency_incident_notified") {
		return markPingHealthIncidentNotifiedMigrationDone(db)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.PingLossNotification{}).
			Where("alert_active = ? AND last_notified IS NOT NULL", true).
			Update("loss_incident_notified", true).Error; err != nil {
			return fmt.Errorf("migrate loss incident delivery flags: %w", err)
		}
		if err := tx.Model(&models.PingLossNotification{}).
			Where("latency_alert_state IN ? AND latency_last_notified IS NOT NULL", []string{models.LatencyAlertHigh, models.LatencyAlertLow}).
			Update("latency_incident_notified", true).Error; err != nil {
			return fmt.Errorf("migrate latency incident delivery flags: %w", err)
		}
		return markPingHealthIncidentNotifiedMigrationDone(tx)
	})
}

func pingHealthIncidentNotifiedMigrationDone(db *gorm.DB) bool {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		return false
	}
	var item appconfig.ConfigItem
	if err := db.Where("key = ?", pingHealthIncidentNotifiedMigrationKey).First(&item).Error; err != nil {
		return false
	}
	return item.Value == "true"
}

func markPingHealthIncidentNotifiedMigrationDone(db *gorm.DB) error {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
			return fmt.Errorf("create config item table: %w", err)
		}
	}
	item := appconfig.ConfigItem{Key: pingHealthIncidentNotifiedMigrationKey, Value: "true"}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&item).Error
}
