package migrations

import (
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/database/models"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/expiry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const expiryBeijingMidnightMigrationKey = "internal_expiry_beijing_midnight_migrated"

// MigrateExpiryBeijingMidnight rewrites finite expiries to Beijing midnight
// of their China calendar date. It must run after AutoMigrate (so expiry_timezone
// exists) and before EnsureInitialPriceVersions.
func MigrateExpiryBeijingMidnight(db *gorm.DB) error {
	if expiryBeijingMidnightMigrationDone(db) {
		return nil
	}
	if !db.Migrator().HasTable(&models.Client{}) || !db.Migrator().HasColumn(&models.Client{}, "expired_at") {
		return markExpiryBeijingMidnightMigrationDone(db)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var clients []models.Client
		if err := tx.Find(&clients).Error; err != nil {
			return fmt.Errorf("list clients for expiry midnight migration: %w", err)
		}
		now := time.Now().UTC()
		hasPriceVersions := tx.Migrator().HasTable(&models.BillingPriceVersion{})
		for _, client := range clients {
			zone, err := expiry.NormalizeTimezone(client.ExpiryTimezone)
			if err != nil {
				zone = expiry.DefaultTimezone
			}
			updates := map[string]interface{}{}
			if client.ExpiryTimezone != zone {
				updates["expiry_timezone"] = zone
			}
			if expiry.IsFinite(client.ExpiredAt) {
				next := expiry.BeijingMidnightUTC(*client.ExpiredAt)
				if !next.Equal(client.ExpiredAt.UTC()) {
					updates["expired_at"] = next
				}
			}
			if len(updates) == 0 {
				continue
			}
			if _, changed := updates["expired_at"]; changed && hasPriceVersions {
				if err := billing.CapturePriceVersion(tx, client, updates, billing.PriceSourceMigration, now); err != nil {
					return fmt.Errorf("capture expiry migration version for %s: %w", client.UUID, err)
				}
			}
			if err := tx.Model(&models.Client{}).Where("uuid = ?", client.UUID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update expiry for %s: %w", client.UUID, err)
			}
		}
		return markExpiryBeijingMidnightMigrationDone(tx)
	})
}

func expiryBeijingMidnightMigrationDone(db *gorm.DB) bool {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		return false
	}
	var item appconfig.ConfigItem
	if err := db.Where("key = ?", expiryBeijingMidnightMigrationKey).First(&item).Error; err != nil {
		return false
	}
	return item.Value == "true"
}

func markExpiryBeijingMidnightMigrationDone(db *gorm.DB) error {
	if !db.Migrator().HasTable(&appconfig.ConfigItem{}) {
		if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
			return fmt.Errorf("create config item table: %w", err)
		}
	}
	item := appconfig.ConfigItem{Key: expiryBeijingMidnightMigrationKey, Value: "true"}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&item).Error
}
