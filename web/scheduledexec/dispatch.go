package scheduledexec

import (
	"errors"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	agent "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
)

const (
	NoteStillRunning = "still_running"
	NoteNoTargets    = "no_targets"
	NoteNoClients    = "no_clients"
	NoteCreateFailed = "create_failed"
	NoteRemoteClosed = "remote_closed"
	NotePersistError = "persist_error"
	NoteSuperseded   = "superseded"
)

// Outcome is one schedule attempt. A command is sent only when Delivered is true.
type Outcome struct {
	TaskID    string
	Status    string
	Note      string
	Sent      int
	Queued    int
	Offline   int
	Failed    int
	Delivered bool
}

// bindThenDeliver publishes the task association before taking the delivery
// lock. allow is checked inside that lock, and enqueue runs only after allow
// returns true. A stop that already finished therefore cannot be followed by
// a new queue entry; a stop that arrives during enqueue waits, then removes it.
func bindThenDeliver(bind func() error, allow func() bool, enqueue func()) (bool, error) {
	if bind != nil {
		if err := bind(); err != nil {
			return false, err
		}
	}
	return agent.GuardRemoteDelivery(allow, enqueue), nil
}

// Dispatch runs a stored command through the same delivery gate as a manual exec.
// Offline and unavailable servers are recorded and are not given the command.
// bind runs after the task exists and before the command can be queued.
// allow runs inside the gate. A false result cancels this attempt without sending.
func Dispatch(command string, clientIDs []string, allow func() bool, bind func(taskID string) error) (Outcome, error) {
	uuids := uniqueUUIDs(clientIDs)
	if len(uuids) == 0 {
		return Outcome{Status: models.ScheduledExecRunFailed, Note: NoteNoTargets}, nil
	}
	if !remote.RemoteManagementEnabled() {
		return Outcome{Status: models.ScheduledExecRunFailed, Note: NoteRemoteClosed}, nil
	}
	known, err := clients.GetClientsByUUIDs(uuids)
	if err != nil {
		return Outcome{}, err
	}
	live, queued, offline, unavailable := classifyTargets(uuids, known)
	taskID := utils.GenerateRandomString(16)
	if taskID == "" {
		return Outcome{}, errors.New("failed to create task id")
	}
	all := make([]string, 0, len(live)+len(queued)+len(offline)+len(unavailable))
	all = append(all, live...)
	all = append(all, queued...)
	all = append(all, offline...)
	all = append(all, unavailable...)
	if err := tasks.CreateTask(taskID, all, command); err != nil {
		return Outcome{Status: models.ScheduledExecRunFailed, Note: NoteCreateFailed}, err
	}
	outcome := Outcome{
		TaskID:  taskID,
		Status:  models.ScheduledExecRunDispatched,
		Offline: len(offline),
		Failed:  len(unavailable),
	}
	finished := time.Now().UTC()
	if len(live)+len(queued) == 0 {
		if bind != nil {
			if err := bind(taskID); err != nil {
				_ = tasks.DeleteTask(taskID)
				return Outcome{TaskID: taskID, Status: models.ScheduledExecRunFailed, Note: NoteCreateFailed}, err
			}
		}
		if err := persistTerminalResults(taskID, offline, nil, unavailable, finished); err != nil {
			outcome.Note = NotePersistError
			return outcome, err
		}
		outcome.Status = models.ScheduledExecRunSkipped
		outcome.Note = NoteNoClients
		return outcome, nil
	}

	var sent, actuallyQueued, deliveryFailed []string
	scheduleBlocked := false
	delivered, bindErr := bindThenDeliver(func() error {
		if bind == nil {
			return nil
		}
		return bind(taskID)
	}, func() bool {
		if !remote.RemoteManagementEnabled() {
			return false
		}
		if allow != nil && !allow() {
			scheduleBlocked = true
			return false
		}
		return true
	}, func() {
		for _, uuid := range append(append([]string{}, live...), queued...) {
			queuedEvent, notified := agent.DispatchV2ExecEvent(uuid, v2.ExecParams{TaskID: taskID, Command: command})
			if !queuedEvent {
				deliveryFailed = append(deliveryFailed, uuid)
				continue
			}
			if notified {
				sent = append(sent, uuid)
				continue
			}
			actuallyQueued = append(actuallyQueued, uuid)
		}
	})
	if bindErr != nil {
		_ = tasks.DeleteTask(taskID)
		return Outcome{TaskID: taskID, Status: models.ScheduledExecRunFailed, Note: NoteCreateFailed}, bindErr
	}
	if !delivered {
		resultText := v2.RemoteManagementClosedTaskResult
		outcome.Status = models.ScheduledExecRunFailed
		outcome.Note = NoteRemoteClosed
		if scheduleBlocked {
			resultText = v2.ScheduledExecStoppedTaskResult
			outcome.Status = models.ScheduledExecRunSkipped
			outcome.Note = NoteSuperseded
		}
		if err := cancelTaskClients(taskID, all, resultText); err != nil {
			return outcome, err
		}
		outcome.Sent = 0
		outcome.Queued = 0
		return outcome, nil
	}
	if err := persistTerminalResults(taskID, offline, deliveryFailed, unavailable, finished); err != nil {
		outcome.Note = NotePersistError
		outcome.Sent = len(sent)
		outcome.Queued = len(actuallyQueued)
		outcome.Failed = len(uniqueStrings(append(append([]string{}, unavailable...), deliveryFailed...), sent, actuallyQueued))
		outcome.Delivered = true
		return outcome, err
	}
	outcome.Sent = len(sent)
	outcome.Queued = len(actuallyQueued)
	outcome.Failed = len(uniqueStrings(append(append([]string{}, unavailable...), deliveryFailed...), sent, actuallyQueued))
	outcome.Delivered = len(sent)+len(actuallyQueued) > 0
	return outcome, nil
}

func persistTerminalResults(taskID string, offline, deliveryFailed, unavailable []string, finishedAt time.Time) error {
	var first error
	save := func(ids []string, result string) {
		if len(ids) == 0 {
			return
		}
		if err := tasks.SaveTaskResults(taskID, ids, result, -1, finishedAt); err != nil && first == nil {
			first = err
		}
	}
	save(offline, "Client offline!")
	save(deliveryFailed, "delivery failed")
	save(unavailable, "remote control unavailable")
	return first
}

func cancelTaskClients(taskID string, clients []string, result string) error {
	var errs []error
	for _, uuid := range clients {
		if uuid == "" {
			continue
		}
		if err := tasks.CancelUndeliveredTaskResult(taskID, uuid, result); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func classifyTargets(uuids []string, known map[string]models.Client) (live, queued, offline, unavailable []string) {
	for _, uuid := range uuids {
		client, ok := known[uuid]
		if !ok {
			unavailable = append(unavailable, uuid)
			continue
		}
		if err := remote.AgentRemoteAllowed(client); err != nil {
			unavailable = append(unavailable, uuid)
			continue
		}
		if agent.GetConnectedClient(uuid) != nil {
			live = append(live, uuid)
			continue
		}
		if agent.IsAgentOnline(uuid) {
			queued = append(queued, uuid)
			continue
		}
		offline = append(offline, uuid)
	}
	return live, queued, offline, unavailable
}

func uniqueUUIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueStrings(values []string, exclude ...[]string) []string {
	skip := make(map[string]struct{})
	for _, group := range exclude {
		for _, value := range group {
			skip[value] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := skip[value]; ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
