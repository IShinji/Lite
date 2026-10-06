package scheduledexec

import (
	"os"
	"strings"
	"testing"
)

func TestDispatchUsesRemoteExecGate(t *testing.T) {
	src, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	fn := text
	if start := strings.Index(text, "func Dispatch"); start >= 0 {
		fn = text[start:]
	}
	for _, want := range []string{
		"remote.RemoteManagementEnabled()",
		"remote.AgentRemoteAllowed(",
		"bindThenDeliver(",
		"agent.DispatchV2ExecEvent(",
		"tasks.SaveTaskResults(",
		"v2.RemoteManagementClosedTaskResult",
	} {
		if !strings.Contains(fn, want) {
			t.Fatalf("Dispatch missing %s", want)
		}
	}
	if !strings.Contains(text, "agent.GuardRemoteDelivery(") {
		t.Fatal("delivery no longer uses the remote exec gate")
	}
	if strings.Contains(fn, "GetConnectedClients()") {
		t.Fatal("Dispatch copies the full connected client map")
	}
}

func TestCredentialChangeStopsScheduledExec(t *testing.T) {
	src, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "accounts.AddUserSecurityListener(") {
		t.Fatal("scheduled exec does not subscribe to credential changes")
	}
	if strings.Contains(string(src), "AddRevokeAllListener(") {
		t.Fatal("scheduled exec replaces the MCP revoke-all listener")
	}
	if strings.Contains(string(src), "busyWindow") {
		t.Fatal("overlap guard still expires after a fixed window")
	}
	if !strings.Contains(string(src), "ClaimScheduledExec(id, expected, next, now)") {
		t.Fatal("claim does not keep the due time that was selected")
	}
}

func TestClaimAndBusyMatchAudit(t *testing.T) {
	src, err := os.ReadFile("../../database/tasks/scheduled_exec.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "sameInstant(*row.NextRunAt, expectedNext)") {
		t.Fatal("claim does not require the observed next_run_at")
	}
	if strings.Contains(text, "next_run_at <= ?") || strings.Contains(text, "next_run_at = ?") {
		t.Fatal("due check still compares timestamp text")
	}
	busy := text
	if start := strings.Index(text, "func ScheduledExecBusy"); start >= 0 {
		busy = text[start:]
	}
	if strings.Contains(busy, "started_at >=") {
		t.Fatal("unfinished runs stop blocking after a time window")
	}
	security, err := os.ReadFile("security.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(security), "remotectl.RevokeUser(") {
		t.Fatal("credential stop does not revoke exec grants inside the schedule lock")
	}
	if strings.Contains(string(security), "StopForSecurity") {
		t.Fatal("session revocation still has a schedule-wide stop")
	}
}
