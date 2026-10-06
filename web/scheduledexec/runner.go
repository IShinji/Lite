package scheduledexec

import (
	"errors"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/corn"
	"github.com/nuomiiiii/lite/pkg/execschedule"
	"github.com/nuomiiiii/lite/pkg/timeutil"
	"github.com/nuomiiiii/lite/utils"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/web/api/remote"
	"gorm.io/gorm"
)

var tickMu sync.Mutex

func init() {
	accounts.AddUserSecurityListener(func(userUUID string) {
		if err := StopForOwner(userUUID); err != nil {
			logger.Errorf("schedule", "failed to stop scheduled exec after a credential change: %v", err)
		}
	})
}

// Start polls saved tasks. Each due task is claimed before it is sent, and the
// send uses the same remote-exec gate as a manual command.
func Start() {
	if err := corn.AddFunc("exec:schedules", "@every 15s", Tick); err != nil {
		logger.Errorf("schedule", "failed to register scheduled exec: %v", err)
	}
}

// Tick runs tasks whose next time has arrived. It does nothing while remote
// management is off. A task is skipped when its owner no longer exists.
func Tick() {
	if !remote.RemoteManagementEnabled() {
		return
	}
	tickMu.Lock()
	defer tickMu.Unlock()
	if !remote.RemoteManagementEnabled() {
		return
	}
	now := time.Now().UTC()
	due, err := tasks.ListDueScheduledExecs(now)
	if err != nil {
		logger.Errorf("schedule", "failed to list scheduled exec: %v", err)
		return
	}
	for _, row := range due {
		fire(row.ID, now, "", "")
	}
}

func fire(id string, now time.Time, actor string, ip string) {
	if !remote.RemoteManagementEnabled() {
		return
	}
	row, err := tasks.GetScheduledExec(id)
	if err != nil {
		logger.Errorf("schedule", "failed to load scheduled exec %s: %v", id, err)
		return
	}
	if row == nil || !row.Enabled {
		return
	}
	missing, err := ownerMissing(row.OwnerUserUUID)
	if err != nil {
		logger.Errorf("schedule", "failed to check scheduled exec owner %s: %v", id, err)
		return
	}
	if missing {
		if stopErr := StopForOwner(row.OwnerUserUUID); stopErr != nil {
			logger.Errorf("schedule", "failed to disable scheduled exec %s for a missing owner: %v", id, stopErr)
		}
		return
	}
	var (
		snapshot models.ScheduledExec
		next     time.Time
		claimed  bool
		busy     bool
		invalid  bool
	)
	err = WithMutation(func() error {
		current, err := tasks.GetScheduledExec(id)
		if err != nil {
			return err
		}
		if current == nil || !current.Enabled || !remote.RemoteManagementEnabled() {
			return nil
		}
		if current.NextRunAt == nil || current.NextRunAt.After(now) {
			return nil
		}
		spec := execschedule.Spec{
			Kind:            current.Kind,
			IntervalMinutes: current.IntervalMinutes,
			TimeOfDay:       current.TimeOfDay,
			Weekday:         current.Weekday,
			Weekdays:        execschedule.ParseWeekdays(current.Weekdays),
			MonthDay:        current.MonthDay,
		}
		next, err = execschedule.Next(spec, now, timeutil.BeijingLocation)
		if err != nil {
			invalid = true
			logger.Errorf("schedule", "scheduled exec %s has an invalid schedule: %v", id, err)
			return tasks.SetScheduledExecEnabled(id, false, "", "", nil)
		}
		busy, err = tasks.ScheduledExecBusy(id)
		if err != nil {
			return err
		}
		expected := current.NextRunAt.UTC()
		claimed, err = tasks.ClaimScheduledExec(id, expected, next, now)
		if err != nil {
			return err
		}
		snapshot = *current
		snapshot.Clients = append(models.StringArray(nil), current.Clients...)
		return nil
	})
	if err != nil {
		logger.Errorf("schedule", "failed to claim scheduled exec %s: %v", id, err)
	}
	if invalid {
		if cancelErr := CancelPendingDelivery([]string{id}); cancelErr != nil {
			logger.Errorf("schedule", "failed to cancel queued command for invalid scheduled exec %s: %v", id, cancelErr)
		}
		return
	}
	if err != nil || !claimed {
		return
	}
	if busy {
		record(&snapshot, Outcome{Status: models.ScheduledExecRunSkipped, Note: NoteStillRunning}, now)
		return
	}
	command := snapshot.Command
	clients := append([]string(nil), snapshot.Clients...)
	claimedNext := next.UTC()
	var run *models.ScheduledExecRun
	outcome, dispatchErr := Dispatch(command, clients, func() bool {
		current, err := tasks.GetScheduledExec(id)
		if err != nil || current == nil || !current.Enabled {
			return false
		}
		if current.Command != command || !sameClients([]string(current.Clients), clients) {
			return false
		}
		if current.NextRunAt == nil || current.NextRunAt.UTC().Unix() != claimedNext.Unix() {
			return false
		}
		return true
	}, func(taskID string) error {
		nextRun := &models.ScheduledExecRun{
			ID:         utils.GenerateRandomString(16),
			ScheduleID: snapshot.ID,
			TaskID:     taskID,
			Status:     models.ScheduledExecRunDispatched,
			StartedAt:  now.UTC(),
		}
		if nextRun.ID == "" {
			return errors.New("failed to allocate a run id")
		}
		if err := tasks.InsertScheduledExecRun(nextRun); err != nil {
			return err
		}
		run = nextRun
		return nil
	})
	if dispatchErr != nil {
		logger.Errorf("schedule", "scheduled exec %s dispatch: %v", id, dispatchErr)
	}
	if run != nil {
		applyRun(run, outcome)
		if err := tasks.UpdateScheduledExecRun(run); err != nil {
			logger.Errorf("schedule", "failed to update scheduled exec run %s: %v", id, err)
		}
		if err := tasks.PruneScheduledExecRuns(snapshot.ID, execschedule.RunHistory); err != nil {
			logger.Errorf("schedule", "failed to prune scheduled exec runs %s: %v", id, err)
		}
	} else if dispatchErr == nil {
		record(&snapshot, outcome, now)
	}
	if outcome.Delivered {
		auditlog.Event(ip, actor, "warn", "audit.scheduled_exec_run", map[string]string{
			"name": snapshot.Name,
			"id":   outcome.TaskID,
		})
	}
}

func applyRun(run *models.ScheduledExecRun, outcome Outcome) {
	if outcome.Status != "" {
		run.Status = outcome.Status
	}
	run.Note = outcome.Note
	run.Sent = outcome.Sent
	run.Queued = outcome.Queued
	run.Offline = outcome.Offline
	run.Failed = outcome.Failed
}

func record(row *models.ScheduledExec, outcome Outcome, now time.Time) {
	if outcome.Status == "" {
		return
	}
	run := &models.ScheduledExecRun{
		ID:         utils.GenerateRandomString(16),
		ScheduleID: row.ID,
		TaskID:     outcome.TaskID,
		Status:     outcome.Status,
		Note:       outcome.Note,
		Sent:       outcome.Sent,
		Queued:     outcome.Queued,
		Offline:    outcome.Offline,
		Failed:     outcome.Failed,
		StartedAt:  now.UTC(),
	}
	if run.ID == "" {
		logger.Errorf("schedule", "failed to allocate a run id for scheduled exec %s", row.ID)
		return
	}
	if err := tasks.InsertScheduledExecRun(run); err != nil {
		logger.Errorf("schedule", "failed to save scheduled exec run %s: %v", row.ID, err)
		return
	}
	if err := tasks.PruneScheduledExecRuns(row.ID, execschedule.RunHistory); err != nil {
		logger.Errorf("schedule", "failed to prune scheduled exec runs %s: %v", row.ID, err)
	}
}

func sameClients(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// RunNow sends the saved command once. It does not move the next run and does
// not enable a stopped task. captured is the stop generation observed when the
// grant was consumed. allow is checked inside the delivery gate after that.
func RunNow(id string, command string, clientIDs []string, actor string, ip string, captured uint64, allow func() bool) (*models.ScheduledExecRun, error) {
	if !remote.RemoteManagementEnabled() {
		return nil, remoteDisabled
	}
	row, err := tasks.GetScheduledExec(id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errNotFound
	}
	now := time.Now().UTC()
	var run *models.ScheduledExecRun
	outcome, dispatchErr := Dispatch(command, clientIDs, func() bool {
		return runNowMayDeliver(id, captured, allow)
	}, func(taskID string) error {
		nextRun := &models.ScheduledExecRun{
			ID:         utils.GenerateRandomString(16),
			ScheduleID: row.ID,
			TaskID:     taskID,
			Status:     models.ScheduledExecRunDispatched,
			StartedAt:  now,
		}
		if nextRun.ID == "" {
			return errors.New("failed to allocate a run id")
		}
		if err := tasks.InsertScheduledExecRun(nextRun); err != nil {
			return err
		}
		run = nextRun
		return nil
	})
	if run != nil {
		applyRun(run, outcome)
		if err := tasks.UpdateScheduledExecRun(run); err != nil {
			return run, err
		}
		if err := tasks.PruneScheduledExecRuns(row.ID, execschedule.RunHistory); err != nil {
			logger.Errorf("schedule", "failed to prune scheduled exec runs %s: %v", row.ID, err)
		}
	} else if outcome.Status != "" && dispatchErr == nil {
		run = &models.ScheduledExecRun{
			ID:         utils.GenerateRandomString(16),
			ScheduleID: row.ID,
			Status:     outcome.Status,
			Note:       outcome.Note,
			StartedAt:  now,
		}
		if run.ID == "" {
			return nil, errors.New("failed to allocate a run id")
		}
		if err := tasks.InsertScheduledExecRun(run); err != nil {
			return nil, err
		}
	}
	if outcome.Delivered {
		auditlog.Event(ip, actor, "warn", "audit.scheduled_exec_run", map[string]string{
			"name": row.Name,
			"id":   outcome.TaskID,
		})
	}
	if dispatchErr != nil {
		return run, dispatchErr
	}
	return run, nil
}

func runNowMayDeliver(id string, captured uint64, allow func() bool) bool {
	if !RunStillCurrent(id, captured) {
		return false
	}
	return allow == nil || allow()
}

func ownerMissing(userUUID string) (bool, error) {
	if userUUID == "" {
		return true, nil
	}
	_, err := accounts.GetUserByUUID(userUUID)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	return false, err
}
