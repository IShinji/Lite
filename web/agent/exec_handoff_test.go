package agent

import (
	"testing"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func TestQueuedExecIsUndeliveredUntilPulledOrSent(t *testing.T) {
	const uuid = "handoff-queued"
	const taskID = "handoff-task-queued"
	t.Cleanup(func() { RemoveExecEventsByTaskIDs([]string{taskID}) })

	event := EnqueueV2Event(uuid, v2.MethodAgentExec, v2.ExecParams{TaskID: taskID, Command: "true"})
	if event.ID == "" {
		t.Fatal("event was not queued")
	}
	removed := RemoveExecEventsByTaskIDs([]string{taskID})
	if len(removed) != 1 {
		t.Fatalf("removed %d events", len(removed))
	}
	if removed[0].Event.HandedOff {
		t.Fatal("a copy that was only queued was marked handed off")
	}
}

func TestPullMarksExecHandedOffBeforeAck(t *testing.T) {
	const uuid = "handoff-pull"
	const taskID = "handoff-task-pull"
	t.Cleanup(func() { RemoveExecEventsByTaskIDs([]string{taskID}) })

	if event := EnqueueV2Event(uuid, v2.MethodAgentExec, v2.ExecParams{TaskID: taskID, Command: "true"}); event.ID == "" {
		t.Fatal("event was not queued")
	}
	pulled := TakeV2Events(uuid, nil, 8)
	if len(pulled) != 1 || !pulled[0].HandedOff {
		t.Fatalf("pull copy = %+v", pulled)
	}
	removed := RemoveExecEventsByTaskIDs([]string{taskID})
	if len(removed) != 1 || !removed[0].Event.HandedOff {
		t.Fatalf("queued copy after pull = %+v", removed)
	}
}

func TestDirectSendMarkSurvivesUntilRemove(t *testing.T) {
	const uuid = "handoff-send"
	const taskID = "handoff-task-send"
	t.Cleanup(func() { RemoveExecEventsByTaskIDs([]string{taskID}) })

	event := EnqueueV2Event(uuid, v2.MethodAgentExec, v2.ExecParams{TaskID: taskID, Command: "true"})
	if event.ID == "" {
		t.Fatal("event was not queued")
	}
	markV2EventHandedOff(uuid, event.ID)
	removed := RemoveExecEventsByTaskIDs([]string{taskID})
	if len(removed) != 1 || !removed[0].Event.HandedOff {
		t.Fatalf("sent copy = %+v", removed)
	}
}

func TestExpiredHandedOffExecDoesNotFinish(t *testing.T) {
	const handedUUID = "expire-handed"
	const plainUUID = "expire-plain"
	const mcpUUID = "expire-mcp"
	t.Cleanup(func() {
		RemoveV2EventQueue(handedUUID)
		RemoveV2EventQueue(plainUUID)
		RemoveV2EventQueue(mcpUUID)
	})
	previous := expiredV2ExecHandler
	var got []string
	expiredV2ExecHandler = func(uuid, taskID string) {
		got = append(got, uuid+"/"+taskID)
	}
	t.Cleanup(func() { expiredV2ExecHandler = previous })

	handed := EnqueueV2Event(handedUUID, v2.MethodAgentExec, v2.ExecParams{TaskID: "handed-task", Command: "true"})
	plain := EnqueueV2Event(plainUUID, v2.MethodAgentExec, v2.ExecParams{TaskID: "plain-task", Command: "true"})
	mcp := EnqueueV2Event(mcpUUID, v2.MethodAgentMCPExec, v2.MCPExecParams{TaskID: "mcp-task", Command: "true"})
	if handed.ID == "" || plain.ID == "" || mcp.ID == "" {
		t.Fatal("events were not queued")
	}
	markV2EventHandedOff(handedUUID, handed.ID)
	markV2EventHandedOff(mcpUUID, mcp.ID)
	expireQueue := func(uuid string) {
		v2EventMu.Lock()
		defer v2EventMu.Unlock()
		q := v2EventQueues[uuid]
		for i := range q.events {
			q.events[i].ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	expireQueue(handedUUID)
	expireQueue(plainUUID)
	expireQueue(mcpUUID)
	SweepExpiredV2Events()

	seen := map[string]int{}
	for _, item := range got {
		seen[item]++
	}
	if seen[handedUUID+"/handed-task"] != 0 {
		t.Fatal("handed-off exec was finished by expiry")
	}
	if seen[plainUUID+"/plain-task"] != 1 {
		t.Fatalf("undelivered exec expiry calls = %d", seen[plainUUID+"/plain-task"])
	}
	if seen[mcpUUID+"/mcp-task"] != 1 {
		t.Fatalf("mcp expiry calls = %d", seen[mcpUUID+"/mcp-task"])
	}
	if left := RemoveExecEventsByTaskIDs([]string{"handed-task", "plain-task", "mcp-task"}); len(left) != 0 {
		t.Fatalf("expired copies stayed queued: %+v", left)
	}
}
