package tasks

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/execschedule"
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

func TestDueScheduledExecUsesBeijingInstant(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:scheduled-exec-due?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.ScheduledExec{}))

	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	after := time.Date(2026, 10, 6, 12, 0, 0, 0, beijing)
	cases := []struct {
		id   string
		spec execschedule.Spec
	}{
		{id: "daily", spec: execschedule.Spec{Kind: execschedule.KindDaily, TimeOfDay: "00:37"}},
		{id: "weekly", spec: execschedule.Spec{Kind: execschedule.KindWeekly, TimeOfDay: "23:59", Weekdays: []int{int(time.Sunday)}}},
		{id: "monthly", spec: execschedule.Spec{Kind: execschedule.KindMonthly, TimeOfDay: "03:00", MonthDay: 31}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			dueAt, err := execschedule.Next(tc.spec, after, beijing)
			require.NoError(t, err)
			require.Equal(t, beijing.String(), dueAt.Location().String())
			row := models.ScheduledExec{
				ID: tc.id, Name: tc.id, Command: "ip add", OwnerUserUUID: "owner",
				Kind: tc.spec.Kind, Enabled: true, NextRunAt: &dueAt, CreatedAt: after, UpdatedAt: after,
			}
			require.NoError(t, db.Create(&row).Error)

			var stored string
			require.NoError(t, db.Raw("SELECT next_run_at FROM scheduled_execs WHERE id = ?", row.ID).Scan(&stored).Error)
			require.Contains(t, stored, "+08:00")

			now := dueAt.Add(time.Second)
			var textMatches int64
			require.NoError(t, db.Model(&models.ScheduledExec{}).
				Where("id = ? AND enabled = ? AND next_run_at <= ?", row.ID, true, now.UTC()).
				Count(&textMatches).Error)
			require.Zero(t, textMatches)

			var loaded []models.ScheduledExec
			require.NoError(t, db.Where("id = ?", row.ID).Find(&loaded).Error)
			due := filterDueScheduledExecs(loaded, now)
			require.Len(t, due, 1)

			following, err := execschedule.Next(tc.spec, now, beijing)
			require.NoError(t, err)
			claimed, err := claimScheduledExec(db, row.ID, *due[0].NextRunAt, following, now)
			require.NoError(t, err)
			require.True(t, claimed)

			var advanced models.ScheduledExec
			require.NoError(t, db.Where("id = ?", row.ID).First(&advanced).Error)
			require.True(t, advanced.NextRunAt.Equal(following))
			later := following.Add(time.Second)
			require.Len(t, filterDueScheduledExecs([]models.ScheduledExec{advanced}, later), 1)
			claimed, err = claimScheduledExec(db, row.ID, *advanced.NextRunAt, following.Add(24*time.Hour), later)
			require.NoError(t, err)
			require.True(t, claimed)
		})
	}
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
