package scheduledexec

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestDisableBetweenBindAndEnqueueBlocksOldRunNow(t *testing.T) {
	ResetStopGenForTest()
	const id = "sched-inflight"
	var captured uint64
	if err := WithMutation(func() error {
		captured = PeekStopGen(id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enqueued atomic.Bool
	done := make(chan bool, 1)
	go func() {
		ok, err := bindThenDeliver(func() error {
			close(entered)
			<-release
			return nil
		}, func() bool {
			return runNowMayDeliver(id, captured, func() bool { return true })
		}, func() {
			enqueued.Store(true)
		})
		if err != nil {
			t.Errorf("bind: %v", err)
		}
		done <- ok
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("run-now did not reach the association step")
	}
	if err := WithMutation(func() error {
		NoteStopped(id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if ok := <-done; ok {
		t.Fatal("run-now started before disable was delivered after the stop")
	}
	if enqueued.Load() {
		t.Fatal("run-now started before disable enqueued a command")
	}

	var fresh uint64
	if err := WithMutation(func() error {
		fresh = PeekStopGen(id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if fresh == captured {
		t.Fatal("disable did not move the stop generation")
	}
	var again atomic.Bool
	ok, err := bindThenDeliver(func() error { return nil }, func() bool {
		return runNowMayDeliver(id, fresh, func() bool { return true })
	}, func() {
		again.Store(true)
	})
	if err != nil || !ok || !again.Load() {
		t.Fatalf("new run-now after disable ok=%v enqueued=%v err=%v", ok, again.Load(), err)
	}
}
