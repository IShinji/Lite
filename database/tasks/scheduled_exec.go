package tasks

import (
	"errors"
	"sort"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

var errScheduleGone = errors.New("scheduled task not found")

// ErrScheduledExecOrder means the submitted id list is not the current set of tasks.
var ErrScheduledExecOrder = errors.New("scheduled task order is invalid")

func ListScheduledExecs() ([]models.ScheduledExec, error) {
	var rows []models.ScheduledExec
	err := dbcore.GetDBInstance().Order("sort_order asc, created_at desc, id desc").Find(&rows).Error
	return rows, err
}

func GetScheduledExec(id string) (*models.ScheduledExec, error) {
	var row models.ScheduledExec
	err := dbcore.GetDBInstance().Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func CountScheduledExecs() (int64, error) {
	var count int64
	err := dbcore.GetDBInstance().Model(&models.ScheduledExec{}).Count(&count).Error
	return count, err
}

func CreateScheduledExec(row *models.ScheduledExec) error {
	return dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		return createScheduledExec(tx, row)
	})
}

// createScheduledExec puts a new task at the front. Rows that have never been
// reordered share sort 0 and stay newest-first via created_at.
func createScheduledExec(tx *gorm.DB, row *models.ScheduledExec) error {
	var first []int
	if err := tx.Model(&models.ScheduledExec{}).Order("sort_order asc").Limit(1).Pluck("sort_order", &first).Error; err != nil {
		return err
	}
	if len(first) > 0 {
		row.SortOrder = first[0] - 1
	}
	return tx.Create(row).Error
}

// ReorderScheduledExecs stores the full display order. The id list must be
// exactly the tasks that exist now.
func ReorderScheduledExecs(ids []string) error {
	return dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		return reorderScheduledExecs(tx, ids)
	})
}

func reorderScheduledExecs(tx *gorm.DB, ids []string) error {
	var existing []string
	if err := tx.Model(&models.ScheduledExec{}).Pluck("id", &existing).Error; err != nil {
		return err
	}
	if len(existing) != len(ids) {
		return ErrScheduledExecOrder
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return ErrScheduledExecOrder
		}
		if _, ok := seen[id]; ok {
			return ErrScheduledExecOrder
		}
		seen[id] = struct{}{}
	}
	for _, id := range existing {
		if _, ok := seen[id]; !ok {
			return ErrScheduledExecOrder
		}
	}
	for index, id := range ids {
		if err := tx.Model(&models.ScheduledExec{}).Where("id = ?", id).Update("sort_order", index).Error; err != nil {
			return err
		}
	}
	return nil
}

func SaveScheduledExec(row *models.ScheduledExec) error {
	return dbcore.GetDBInstance().Save(row).Error
}

func ListDueScheduledExecs(now time.Time) ([]models.ScheduledExec, error) {
	var rows []models.ScheduledExec
	err := dbcore.GetDBInstance().
		Where("enabled = ? AND next_run_at IS NOT NULL", true).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return filterDueScheduledExecs(rows, now), nil
}

// SQLite stores time.Time as text with the value's own offset. Daily, weekly,
// and monthly slots are saved in Beijing, so the text ends in "+08:00", while
// a UTC parameter ends in "+00:00". Text order is not instant order, and exact
// text equality never matches, so the due check and the claim both happen on
// the parsed instant. The schedule kind is not part of that comparison.
func filterDueScheduledExecs(rows []models.ScheduledExec, now time.Time) []models.ScheduledExec {
	due := make([]models.ScheduledExec, 0, len(rows))
	for _, row := range rows {
		if row.NextRunAt != nil && !row.NextRunAt.After(now) {
			due = append(due, row)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextRunAt.Equal(*due[j].NextRunAt) {
			return due[i].ID < due[j].ID
		}
		return due[i].NextRunAt.Before(*due[j].NextRunAt)
	})
	return due
}

func sameInstant(a, b time.Time) bool {
	return !a.IsZero() && !b.IsZero() && a.UTC().Equal(b.UTC())
}

// ClaimScheduledExec moves the next run forward only when the task is still
// enabled and still due at the observed time. A save that already changed
// next_run_at does not take this round.
func ClaimScheduledExec(id string, expectedNext time.Time, next time.Time, last time.Time) (bool, error) {
	var claimed bool
	err := dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		ok, err := claimScheduledExec(tx, id, expectedNext, next, last)
		claimed = ok
		return err
	})
	return claimed, err
}

func claimScheduledExec(tx *gorm.DB, id string, expectedNext time.Time, next time.Time, last time.Time) (bool, error) {
	var row models.ScheduledExec
	err := tx.Where("id = ? AND enabled = ?", id, true).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.NextRunAt == nil || !sameInstant(*row.NextRunAt, expectedNext) {
		return false, nil
	}
	result := tx.Model(&models.ScheduledExec{}).Where("id = ?", id).Updates(map[string]any{
		"next_run_at": next.UTC(),
		"last_run_at": last.UTC(),
		"updated_at":  time.Now().UTC(),
	})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func SetScheduledExecEnabled(id string, enabled bool, reason string, owner string, next *time.Time) error {
	updates := map[string]any{
		"enabled":         enabled,
		"disabled_reason": reason,
		"updated_at":      time.Now().UTC(),
	}
	if owner != "" {
		updates["owner_user_uuid"] = owner
	}
	if next != nil {
		updates["next_run_at"] = next.UTC()
	}
	result := dbcore.GetDBInstance().Model(&models.ScheduledExec{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DisableScheduledExecs turns off every matching enabled task and returns their names.
// all ignores owner. An empty owner with all false matches only unbound tasks.
func DisableScheduledExecs(owner string, all bool, reason string) ([]string, error) {
	db := dbcore.GetDBInstance()
	query := db.Model(&models.ScheduledExec{}).Where("enabled = ?", true)
	if !all {
		query = query.Where("owner_user_uuid = ?", owner)
	}
	var rows []models.ScheduledExec
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		names = append(names, row.Name)
	}
	err := db.Model(&models.ScheduledExec{}).Where("id IN ?", ids).Updates(map[string]any{
		"enabled":         false,
		"disabled_reason": reason,
		"updated_at":      time.Now().UTC(),
	}).Error
	if err != nil {
		return nil, err
	}
	return names, nil
}

// StopScheduledExecsForRemoteOff disables every task, including ones already stopped,
// so the displayed reason matches the site switch.
func StopScheduledExecsForRemoteOff(reason string) ([]string, error) {
	db := dbcore.GetDBInstance()
	var rows []models.ScheduledExec
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Enabled || row.DisabledReason != reason {
			ids = append(ids, row.ID)
			if row.Enabled {
				names = append(names, row.Name)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	err := db.Model(&models.ScheduledExec{}).Where("id IN ?", ids).Updates(map[string]any{
		"enabled":         false,
		"disabled_reason": reason,
		"updated_at":      time.Now().UTC(),
	}).Error
	if err != nil {
		return nil, err
	}
	return names, nil
}

// DeleteScheduledExec removes the schedule, its runs, and the exec tasks those
// runs point at. Task IDs are read inside the same transaction as the delete,
// so a run saved just before this commit is included.
func DeleteScheduledExec(id string) ([]string, error) {
	var taskIDs []string
	err := dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		var runs []models.ScheduledExecRun
		if err := tx.Where("schedule_id = ?", id).Find(&runs).Error; err != nil {
			return err
		}
		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			if run.TaskID == "" {
				continue
			}
			ids = append(ids, run.TaskID)
			if err := deleteTaskTx(tx, run.TaskID); err != nil {
				return err
			}
		}
		taskIDs = ids
		if err := tx.Where("schedule_id = ?", id).Delete(&models.ScheduledExecRun{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&models.ScheduledExec{}).Error
	})
	return taskIDs, err
}

func InsertScheduledExecRun(run *models.ScheduledExecRun) error {
	if run == nil || run.ScheduleID == "" {
		return errors.New("scheduled run is missing")
	}
	db := dbcore.GetDBInstance()
	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.ScheduledExec{}).Where("id = ?", run.ScheduleID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return errScheduleGone
		}
		return tx.Create(run).Error
	})
}

func UpdateScheduledExecRun(run *models.ScheduledExecRun) error {
	if run == nil || run.ID == "" {
		return errors.New("scheduled run is missing")
	}
	return dbcore.GetDBInstance().Model(&models.ScheduledExecRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status":  run.Status,
		"note":    run.Note,
		"sent":    run.Sent,
		"queued":  run.Queued,
		"offline": run.Offline,
		"failed":  run.Failed,
	}).Error
}

func ListScheduledExecRuns(scheduleID string, limit int) ([]models.ScheduledExecRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []models.ScheduledExecRun
	err := dbcore.GetDBInstance().
		Where("schedule_id = ?", scheduleID).
		Order("started_at desc, id desc").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

func LatestScheduledExecRuns(ids []string) (map[string]models.ScheduledExecRun, error) {
	out := make(map[string]models.ScheduledExecRun, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []models.ScheduledExecRun
	err := dbcore.GetDBInstance().
		Where("schedule_id IN ?", ids).
		Order("started_at desc, id desc").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, ok := out[row.ScheduleID]; ok {
			continue
		}
		out[row.ScheduleID] = row
	}
	return out, nil
}

// ListScheduledExecIDs returns every saved task. all ignores owner.
func ListScheduledExecIDs(owner string, all bool) ([]string, error) {
	db := dbcore.GetDBInstance().Model(&models.ScheduledExec{})
	if !all {
		db = db.Where("owner_user_uuid = ?", owner)
	}
	var ids []string
	err := db.Pluck("id", &ids).Error
	return ids, err
}

// ScheduledExecTaskIDs returns exec task ids recorded for these schedules.
func ScheduledExecTaskIDs(scheduleIDs []string) ([]string, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	var ids []string
	err := dbcore.GetDBInstance().Model(&models.ScheduledExecRun{}).
		Where("schedule_id IN ? AND task_id <> ''", scheduleIDs).
		Distinct("task_id").
		Pluck("task_id", &ids).Error
	return ids, err
}

// ScheduledExecBusy reports whether any dispatched run still has unfinished output.
// Age does not clear the guard; a finished or cancelled result does.
func ScheduledExecBusy(scheduleID string) (bool, error) {
	var runs []models.ScheduledExecRun
	err := dbcore.GetDBInstance().
		Where("schedule_id = ? AND status = ? AND task_id <> ''", scheduleID, models.ScheduledExecRunDispatched).
		Find(&runs).Error
	if err != nil {
		return false, err
	}
	db := dbcore.GetDBInstance()
	for _, run := range runs {
		var pending int64
		if err := db.Model(&models.TaskResult{}).
			Where("task_id = ? AND finished_at IS NULL", run.TaskID).
			Count(&pending).Error; err != nil {
			return false, err
		}
		if pending > 0 {
			return true, nil
		}
	}
	return false, nil
}

func PruneScheduledExecRuns(scheduleID string, keep int) error {
	db := dbcore.GetDBInstance()
	var runs []models.ScheduledExecRun
	if err := db.Where("schedule_id = ?", scheduleID).
		Order("started_at desc, id desc").
		Find(&runs).Error; err != nil {
		return err
	}
	active, err := unfinishedTaskIDs(db, runs)
	if err != nil {
		return err
	}
	stale := runsPastHistory(runs, active, keep)
	if len(stale) == 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		ids := make([]string, 0, len(stale))
		for _, run := range stale {
			ids = append(ids, run.ID)
			if run.TaskID == "" {
				continue
			}
			if _, stillActive := active[run.TaskID]; stillActive {
				continue
			}
			if err := deleteTaskTx(tx, run.TaskID); err != nil {
				return err
			}
		}
		return tx.Where("id IN ?", ids).Delete(&models.ScheduledExecRun{}).Error
	})
}

func unfinishedTaskIDs(db *gorm.DB, runs []models.ScheduledExecRun) (map[string]struct{}, error) {
	active := map[string]struct{}{}
	ids := make([]string, 0, len(runs))
	seen := map[string]struct{}{}
	for _, run := range runs {
		if run.TaskID == "" {
			continue
		}
		if _, ok := seen[run.TaskID]; ok {
			continue
		}
		seen[run.TaskID] = struct{}{}
		ids = append(ids, run.TaskID)
	}
	if len(ids) == 0 {
		return active, nil
	}
	var pending []string
	err := db.Model(&models.TaskResult{}).
		Where("task_id IN ? AND finished_at IS NULL", ids).
		Distinct("task_id").
		Pluck("task_id", &pending).Error
	if err != nil {
		return nil, err
	}
	for _, id := range pending {
		active[id] = struct{}{}
	}
	return active, nil
}

// runsPastHistory keeps every unfinished execution and the newest `keep`
// finished runs. runs must be newest first. Unfinished runs do not use up keep.
func runsPastHistory(runs []models.ScheduledExecRun, active map[string]struct{}, keep int) []models.ScheduledExecRun {
	if keep < 0 {
		keep = 0
	}
	finished := make([]models.ScheduledExecRun, 0, len(runs))
	for _, run := range runs {
		if run.TaskID != "" {
			if _, ok := active[run.TaskID]; ok {
				continue
			}
		}
		finished = append(finished, run)
	}
	if len(finished) <= keep {
		return nil
	}
	return finished[keep:]
}

func DeleteTask(taskID string) error {
	if taskID == "" {
		return nil
	}
	return dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		return deleteTaskTx(tx, taskID)
	})
}

func deleteTaskTx(tx *gorm.DB, taskID string) error {
	if taskID == "" {
		return nil
	}
	if err := tx.Where("task_id = ?", taskID).Delete(&models.TaskResult{}).Error; err != nil {
		return err
	}
	return tx.Where("task_id = ?", taskID).Delete(&models.Task{}).Error
}
