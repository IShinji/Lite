package scheduledexec

import (
	"errors"
	"sync"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/web/remotectl"
)

var (
	errNotFound    = errString("scheduled task not found")
	remoteDisabled = errString("站点未启用远程管理")
	// ErrAbort stops a mutation without treating it as a storage failure.
	ErrAbort = errors.New("scheduled exec mutation aborted")
)

type errString string

func (e errString) Error() string { return string(e) }

// mutateMu serializes saves and stops. Take it before the remote delivery
// mutex, and never hold it across Dispatch or GuardRemoteDelivery.
var (
	mutateMu sync.Mutex
	stopGen  = map[string]uint64{}
)

// PeekStopGen is the stop generation observed while mutateMu is held.
// Run-now captures it in the same critical section as grant consumption.
func PeekStopGen(id string) uint64 {
	return stopGen[id]
}

// NoteStopped invalidates run-now requests that captured an older generation.
// The caller must already hold WithMutation.
func NoteStopped(id string) {
	if id == "" {
		return
	}
	stopGen[id]++
}

// RunStillCurrent reports whether a run-now captured before this stop is still
// the latest generation. A run-now started after the stop captures the new value.
func RunStillCurrent(id string, captured uint64) bool {
	mutateMu.Lock()
	defer mutateMu.Unlock()
	return stopGen[id] == captured
}

func ResetStopGenForTest() {
	mutateMu.Lock()
	stopGen = map[string]uint64{}
	mutateMu.Unlock()
}

// WithMutation runs fn while schedule saves and security stops are exclusive.
func WithMutation(fn func() error) error {
	mutateMu.Lock()
	defer mutateMu.Unlock()
	return fn()
}

// StopForOwner disables tasks owned by this administrator and drops commands
// that have not reached an agent. An empty id disables only tasks that were
// never bound to an account. A database error is returned and does not imply
// the account is gone. Revoking login sessions does not call this.
func StopForOwner(userUUID string) error {
	var names []string
	err := WithMutation(func() error {
		remotectl.RevokeUser(userUUID)
		var disableErr error
		names, disableErr = tasks.DisableScheduledExecs(userUUID, false, models.ScheduledExecReasonSecurity)
		if disableErr != nil {
			return disableErr
		}
		ids, listErr := tasks.ListScheduledExecIDs(userUUID, false)
		if listErr != nil {
			return listErr
		}
		for _, id := range ids {
			NoteStopped(id)
		}
		return nil
	})
	auditStopped(names)
	ids, listErr := tasks.ListScheduledExecIDs(userUUID, false)
	var cancelErr error
	if listErr == nil {
		cancelErr = CancelPendingDelivery(ids)
	}
	if err != nil {
		return err
	}
	if listErr != nil {
		return listErr
	}
	return cancelErr
}

// StopForRemoteOff disables every task when the site remote-management switch is turned off.
func StopForRemoteOff() error {
	var names []string
	err := WithMutation(func() error {
		var disableErr error
		names, disableErr = tasks.StopScheduledExecsForRemoteOff(models.ScheduledExecReasonRemoteOff)
		if disableErr != nil {
			return disableErr
		}
		ids, listErr := tasks.ListScheduledExecIDs("", true)
		if listErr != nil {
			return listErr
		}
		for _, id := range ids {
			NoteStopped(id)
		}
		return nil
	})
	auditStopped(names)
	ids, listErr := tasks.ListScheduledExecIDs("", true)
	var cancelErr error
	if listErr == nil {
		cancelErr = CancelPendingDelivery(ids)
	}
	if err != nil {
		return err
	}
	if listErr != nil {
		return listErr
	}
	return cancelErr
}

func auditStopped(names []string) {
	for _, name := range names {
		if name == "" {
			continue
		}
		auditlog.Event("", "", "warn", "audit.scheduled_exec_stop", map[string]string{"name": name})
	}
}
