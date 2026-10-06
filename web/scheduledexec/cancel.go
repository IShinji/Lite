package scheduledexec

import (
	"errors"

	"github.com/nuomiiiii/lite/database/tasks"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent "github.com/nuomiiiii/lite/web/agent"
)

var loadScheduledExecTaskIDs = tasks.ScheduledExecTaskIDs

func SetScheduledExecTaskIDsForTest(fn func([]string) ([]string, error)) func() {
	previous := loadScheduledExecTaskIDs
	if fn == nil {
		loadScheduledExecTaskIDs = tasks.ScheduledExecTaskIDs
	} else {
		loadScheduledExecTaskIDs = fn
	}
	return func() { loadScheduledExecTaskIDs = previous }
}

// CancelPendingDelivery removes queued copies for these schedules. A result is
// marked not delivered only when the copy was never sent and never pulled.
// A sent or pulled command keeps its unfinished result and may still finish.
func CancelPendingDelivery(scheduleIDs []string) error {
	taskIDs, err := loadScheduledExecTaskIDs(scheduleIDs)
	if err != nil || len(taskIDs) == 0 {
		return err
	}
	removed := agent.DrainRemoteDelivery(func() []agent.RemovedV2Event {
		return agent.RemoveExecEventsByTaskIDs(taskIDs)
	})
	var errs []error
	for _, item := range removed {
		if item.Event.HandedOff {
			continue
		}
		taskID := agent.ExecTaskID(item.Event)
		if taskID == "" || item.UUID == "" {
			continue
		}
		if err := tasks.CancelUndeliveredTaskResult(taskID, item.UUID, v2.ScheduledExecStoppedTaskResult); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DropQueuedExec removes queued commands after the schedule row is gone.
// The task rows are deleted with the schedule, so no cancel result is stored.
func DropQueuedExec(taskIDs []string) {
	if len(taskIDs) == 0 {
		return
	}
	agent.DrainRemoteDelivery(func() []agent.RemovedV2Event {
		return agent.RemoveExecEventsByTaskIDs(taskIDs)
	})
}
