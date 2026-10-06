package jsonrpc

import (
	"context"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/web/remotectl"
	"github.com/nuomiiiii/lite/web/scheduledexec"
)

func TestInFlightRunNowAfterDisableDoesNotDeliver(t *testing.T) {
	scheduledexec.ResetStopGenForTest()
	remotectl.ResetForTest()
	restoreTasks := scheduledexec.SetScheduledExecTaskIDsForTest(func([]string) ([]string, error) {
		return nil, nil
	})
	previousLoad := loadScheduledExec
	previousSave := saveScheduledExecEnabled
	previousRun := runScheduledNow
	previousBefore := beforeScheduledRunNow
	previousRemote := remoteManagementEnabled
	t.Cleanup(func() {
		restoreTasks()
		loadScheduledExec = previousLoad
		saveScheduledExecEnabled = previousSave
		runScheduledNow = previousRun
		beforeScheduledRunNow = previousBefore
		remoteManagementEnabled = previousRemote
		scheduledexec.ResetStopGenForTest()
		remotectl.ResetForTest()
	})

	const user = "run-now-user"
	const session = "run-now-session"
	const page = "scheduled-exec"
	const id = "sched-inflight"
	row := &models.ScheduledExec{
		ID:      id,
		Command: "echo hi",
		Clients: models.StringArray{"node-a"},
		Enabled: true,
		Name:    "inflight",
	}
	loadScheduledExec = func(string) (*models.ScheduledExec, error) {
		return row, nil
	}
	saveScheduledExecEnabled = func(string, bool, string, string, *time.Time) error {
		row.Enabled = false
		return nil
	}
	remoteManagementEnabled = func() bool { return true }

	var allowed []bool
	runScheduledNow = func(runID, command string, clients []string, actor, ip string, captured uint64, allow func() bool) (*models.ScheduledExecRun, error) {
		allowed = append(allowed, allow())
		return &models.ScheduledExecRun{ID: "run-" + runID, ScheduleID: runID, Status: models.ScheduledExecRunSkipped}, nil
	}
	passes := 0
	beforeScheduledRunNow = func() {
		passes++
		if passes != 1 {
			return
		}
		if err := markScheduledExecStopped(id); err != nil {
			t.Errorf("disable: %v", err)
		}
		if err := scheduledexec.CancelPendingDelivery([]string{id}); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}

	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal:    &rpc.Principal{Type: rpc.PrincipalUser, UserUUID: user},
		SessionToken: session,
		UserUUID:     user,
	})
	first, _, err := remotectl.IssueGrant(user, session, remotectl.ScopeExec, page)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := remotectl.IssueGrant(user, session, remotectl.ScopeExec, page)
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := adminRunScheduledExec(ctx, &rpc.JsonRpcRequest{Params: map[string]any{
		"id": id, "grant": first, "page_id": page,
	}}); rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	if _, rpcErr := adminRunScheduledExec(ctx, &rpc.JsonRpcRequest{Params: map[string]any{
		"id": id, "grant": second, "page_id": page,
	}}); rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	if len(allowed) != 2 || allowed[0] || !allowed[1] {
		t.Fatalf("delivery callback = %v, want [false true]", allowed)
	}
	if row.Enabled {
		t.Fatal("disable did not mark the schedule stopped")
	}
}
