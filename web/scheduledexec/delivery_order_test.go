package scheduledexec

import (
	"sync/atomic"
	"testing"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent "github.com/nuomiiiii/lite/web/agent"
)

func TestBindIsVisibleBeforeEnqueueAndCancelRemovesIt(t *testing.T) {
	const uuid = "order-client"
	const taskID = "order-task"
	t.Cleanup(func() { agent.RemoveExecEventsByTaskIDs([]string{taskID}) })

	entered := make(chan struct{})
	release := make(chan struct{})
	var bound atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := bindThenDeliver(func() error {
			bound.Store(true)
			return nil
		}, func() bool { return true }, func() {
			close(entered)
			<-release
			agent.DispatchV2ExecEvent(uuid, v2.ExecParams{TaskID: taskID, Command: "true"})
		})
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue did not reach the delivery lock")
	}
	if !bound.Load() {
		t.Fatal("command reached the delivery lock before the task association was published")
	}
	removedCh := make(chan []agent.RemovedV2Event, 1)
	go func() {
		removedCh <- agent.DrainRemoteDelivery(func() []agent.RemovedV2Event {
			return agent.RemoveExecEventsByTaskIDs([]string{taskID})
		})
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	removed := <-removedCh
	if len(removed) != 1 || agent.ExecTaskID(removed[0].Event) != taskID {
		t.Fatalf("cancel removed %+v", removed)
	}
	if again := agent.RemoveExecEventsByTaskIDs([]string{taskID}); len(again) != 0 {
		t.Fatalf("command stayed queued after cancel: %+v", again)
	}
}

func TestStopBeforeDeliveryDoesNotEnqueue(t *testing.T) {
	const uuid = "order-stopped-client"
	const taskID = "order-stopped-task"
	t.Cleanup(func() { agent.RemoveExecEventsByTaskIDs([]string{taskID}) })

	started := make(chan struct{})
	release := make(chan struct{})
	stopped := atomic.Bool{}
	enqueued := atomic.Bool{}
	done := make(chan bool, 1)
	go func() {
		ok, err := bindThenDeliver(func() error {
			close(started)
			<-release
			return nil
		}, func() bool { return !stopped.Load() }, func() {
			enqueued.Store(true)
			agent.DispatchV2ExecEvent(uuid, v2.ExecParams{TaskID: taskID, Command: "true"})
		})
		if err != nil {
			t.Errorf("bind: %v", err)
		}
		done <- ok
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("bind did not start")
	}
	stopped.Store(true)
	close(release)
	if ok := <-done; ok {
		t.Fatal("delivery continued after the stop was visible")
	}
	if enqueued.Load() {
		t.Fatal("stop completed before the gate, but the command was queued")
	}
}

func TestBindFailureSkipsEnqueue(t *testing.T) {
	enqueued := false
	ok, err := bindThenDeliver(func() error { return errNotFound }, func() bool { return true }, func() { enqueued = true })
	if err == nil || ok || enqueued {
		t.Fatalf("ok=%v err=%v enqueued=%v", ok, err, enqueued)
	}
}
