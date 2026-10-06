package tasks

import (
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRunsPastHistoryKeepsUnfinishedExecution(t *testing.T) {
	start := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	runs := make([]models.ScheduledExecRun, 0, 52)
	runs = append(runs, models.ScheduledExecRun{ID: "active", TaskID: "task-active", Status: models.ScheduledExecRunDispatched, StartedAt: start})
	for i := 0; i < 51; i++ {
		runs = append(runs, models.ScheduledExecRun{
			ID:        "skip-" + pruneTestID(i),
			Status:    models.ScheduledExecRunSkipped,
			StartedAt: start.Add(time.Duration(i+1) * time.Minute),
		})
	}
	// Newest first, matching the prune query.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	stale := runsPastHistory(runs, map[string]struct{}{"task-active": {}}, 50)
	for _, run := range stale {
		if run.ID == "active" || run.TaskID == "task-active" {
			t.Fatalf("unfinished execution was pruned: %+v", run)
		}
	}
	if len(stale) != 1 {
		t.Fatalf("pruned %d finished runs, want the one past 50", len(stale))
	}
}

func TestRunsPastHistoryDropsFinishedExecutionBeyondKeep(t *testing.T) {
	start := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	runs := []models.ScheduledExecRun{
		{ID: "new", TaskID: "task-new", StartedAt: start.Add(time.Minute)},
		{ID: "old", TaskID: "task-old", StartedAt: start},
	}
	stale := runsPastHistory(runs, nil, 1)
	if len(stale) != 1 || stale[0].ID != "old" {
		t.Fatalf("stale = %+v", stale)
	}
}

func TestUnfinishedTaskIDsKeepsOnlyOpenResults(t *testing.T) {
	dsn := fmt.Sprintf("file:prune-active-%d?mode=memory&cache=shared&_foreign_keys=off", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE task_results (
		task_id varchar(36),
		client varchar(36),
		result text,
		exit_code integer,
		finished_at datetime,
		created_at datetime
	)`).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&models.TaskResult{TaskId: "live", Client: "a", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&models.TaskResult{TaskId: "done", Client: "a", FinishedAt: &now, CreatedAt: now}).Error)
	active, err := unfinishedTaskIDs(db, []models.ScheduledExecRun{{TaskID: "live"}, {TaskID: "done"}, {}})
	require.NoError(t, err)
	if _, ok := active["live"]; !ok {
		t.Fatal("open result was not treated as unfinished")
	}
	if _, ok := active["done"]; ok {
		t.Fatal("finished result still blocked overlap")
	}
}

func pruneTestID(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
