package migrations

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/expiry"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func expiryMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:expiry-midnight-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&appconfig.ConfigItem{}, &models.Client{}, &models.BillingPriceVersion{}, &models.BillingFXSnapshot{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateExpiryBeijingMidnightRewritesFiniteInstantsOnce(t *testing.T) {
	db := expiryMigrationDB(t)
	old := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	longTerm := time.Date(2226, 5, 14, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Client{UUID: "finite", Token: "token-finite", Name: "finite", ExpiredAt: &old}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Client{UUID: "long", Token: "token-long", Name: "long", ExpiredAt: &longTerm}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Client{UUID: "empty", Token: "token-empty", Name: "empty"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateExpiryBeijingMidnight(db); err != nil {
		t.Fatal(err)
	}

	var finite models.Client
	if err := db.First(&finite, "uuid = ?", "finite").Error; err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC)
	if finite.ExpiredAt == nil || !finite.ExpiredAt.UTC().Equal(want) {
		t.Fatalf("finite expired_at = %v want %s", finite.ExpiredAt, want)
	}
	if finite.ExpiryTimezone != expiry.DefaultTimezone {
		t.Fatalf("timezone = %q", finite.ExpiryTimezone)
	}

	var versions []models.BillingPriceVersion
	if err := db.Where("client = ?", "finite").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].ExpiredAt == nil || !versions[0].ExpiredAt.UTC().Equal(want) {
		t.Fatalf("price version = %+v", versions)
	}

	precise := time.Date(2026, 10, 1, 1, 30, 45, 0, time.UTC)
	if err := db.Model(&models.Client{}).Where("uuid = ?", "finite").Update("expired_at", precise).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateExpiryBeijingMidnight(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&finite, "uuid = ?", "finite").Error; err != nil {
		t.Fatal(err)
	}
	if !finite.ExpiredAt.UTC().Equal(precise) {
		t.Fatalf("second run rewrote %s", finite.ExpiredAt)
	}

	var long models.Client
	if err := db.First(&long, "uuid = ?", "long").Error; err != nil {
		t.Fatal(err)
	}
	if long.ExpiredAt == nil || !long.ExpiredAt.UTC().Equal(longTerm) {
		t.Fatalf("long-term rewritten: %v", long.ExpiredAt)
	}
	var empty models.Client
	if err := db.First(&empty, "uuid = ?", "empty").Error; err != nil {
		t.Fatal(err)
	}
	if empty.ExpiredAt != nil {
		t.Fatalf("null expiry rewritten: %v", empty.ExpiredAt)
	}
}

func TestMigrateExpiryBeijingMidnightClosesExistingCurrentVersion(t *testing.T) {
	db := expiryMigrationDB(t)
	old := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Client{
		UUID: "finite", Token: "token-finite", Name: "finite",
		Price: 10, BillingCycle: 30, Currency: "CNY", ExpiredAt: &old,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.BillingPriceVersion{
		Client: "finite", ClientName: "finite", PriceMicros: 10_000_000, Currency: "CNY", CurrencyValid: true,
		BillingCycleDays: 30, ExpiredAt: &old, EffectiveFrom: from, Source: "migration",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateExpiryBeijingMidnight(db); err != nil {
		t.Fatal(err)
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "finite").Error; err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC)
	if client.ExpiredAt == nil || !client.ExpiredAt.UTC().Equal(want) {
		t.Fatalf("finite expired_at = %v want %s", client.ExpiredAt, want)
	}

	var versions []models.BillingPriceVersion
	if err := db.Where("client = ?", "finite").Order("id").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("version count = %d", len(versions))
	}
	if versions[0].EffectiveTo == nil {
		t.Fatal("original current version should be closed")
	}
	if versions[1].EffectiveTo != nil {
		t.Fatal("migration should leave exactly one current version")
	}
	if versions[1].ExpiredAt == nil || !versions[1].ExpiredAt.UTC().Equal(want) {
		t.Fatalf("current version expired_at = %v", versions[1].ExpiredAt)
	}

	if err := MigrateExpiryBeijingMidnight(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client = ?", "finite").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("second run created versions: %d", len(versions))
	}
}
