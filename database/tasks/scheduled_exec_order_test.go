package tasks

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestReorderScheduledExecsStoresDisplayOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:scheduled-exec-order?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.ScheduledExec{}))

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	rows := []models.ScheduledExec{
		{ID: "a", Name: "a", Command: "true", OwnerUserUUID: "owner", Kind: "interval", CreatedAt: now, UpdatedAt: now},
		{ID: "b", Name: "b", Command: "true", OwnerUserUUID: "owner", Kind: "interval", CreatedAt: now.Add(time.Minute), UpdatedAt: now},
		{ID: "c", Name: "c", Command: "true", OwnerUserUUID: "owner", Kind: "interval", CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now},
	}
	require.NoError(t, db.Create(&rows).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return reorderScheduledExecs(tx, []string{"c", "a", "b"})
	}))

	var ordered []string
	require.NoError(t, db.Model(&models.ScheduledExec{}).Order("sort_order asc, id asc").Pluck("id", &ordered).Error)
	require.Equal(t, []string{"c", "a", "b"}, ordered)

	err = db.Transaction(func(tx *gorm.DB) error {
		return reorderScheduledExecs(tx, []string{"a", "a", "b"})
	})
	require.ErrorIs(t, err, ErrScheduledExecOrder)

	err = db.Transaction(func(tx *gorm.DB) error {
		return reorderScheduledExecs(tx, []string{"a", "b"})
	})
	require.ErrorIs(t, err, ErrScheduledExecOrder)
}

func TestCreateScheduledExecPutsNewTaskFirst(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:scheduled-exec-create-order?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.ScheduledExec{}))

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	first := models.ScheduledExec{ID: "old", Name: "old", Command: "true", OwnerUserUUID: "owner", Kind: "interval", SortOrder: 4, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&first).Error)

	next := models.ScheduledExec{ID: "new", Name: "new", Command: "true", OwnerUserUUID: "owner", Kind: "interval", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return createScheduledExec(tx, &next)
	}))
	require.Equal(t, 3, next.SortOrder)
}
