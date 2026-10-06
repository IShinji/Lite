package jsonrpc

import (
	"os"
	"strings"
	"testing"
)

func TestTaskReadsDenyAPIKey(t *testing.T) {
	src, err := os.ReadFile("admin.task.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, name := range []string{
		"func adminGetTasks(",
		"func adminGetTaskById(",
		"func adminGetTasksByClientId(",
		"func adminGetSpecificTaskResult(",
		"func adminGetTaskResultsByTaskId(",
	} {
		start := strings.Index(text, name)
		if start < 0 {
			t.Fatalf("missing %s", name)
		}
		body := text[start:]
		if next := strings.Index(body[len(name):], "\nfunc "); next >= 0 {
			body = body[:len(name)+next]
		}
		if !strings.Contains(body, "denyAPIKey(ctx)") {
			t.Fatalf("%s accepts API keys", name)
		}
	}
}

func TestAdminDispatchRechecksHumanSession(t *testing.T) {
	src, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "accounts.SessionStillValid(") {
		t.Fatal("admin dispatch does not recheck the login session")
	}
	fn := text
	if start := strings.Index(text, "func adminSessionRejected"); start >= 0 {
		fn = text[start:]
	}
	if !strings.Contains(fn, "IsAPIKey") || !strings.Contains(fn, "SessionToken") {
		t.Fatal("session recheck does not keep API keys and internal calls")
	}
}

func TestDeleteAllSessionsDoesNotStopSchedules(t *testing.T) {
	src, err := os.ReadFile("admin.misc.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "func adminDeleteAllSessions")
	if start < 0 {
		t.Fatal("missing adminDeleteAllSessions")
	}
	body := text[start:]
	if next := strings.Index(body[1:], "\nfunc "); next >= 0 {
		body = body[:1+next]
	}
	if strings.Contains(body, "StopForSecurity") || strings.Contains(body, "scheduledexec.Stop") {
		t.Fatal("revoking login sessions still stops scheduled tasks")
	}
}
