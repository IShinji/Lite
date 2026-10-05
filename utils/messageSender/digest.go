package messageSender

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	logger "github.com/nuomiiiii/lite/utils/log"
)

const (
	DefaultDigestSeconds = 5
	MinDigestSeconds     = 1
	MaxDigestSeconds     = 3600
)

var (
	scheduleDigest = func(wait time.Duration, fn func()) func() {
		timer := time.AfterFunc(wait, fn)
		return func() { timer.Stop() }
	}
	digestMu      sync.Mutex
	digestBatches = map[string]*digestBatch{}
)

type digestBatch struct {
	event   models.EventMessage
	seen    map[string]struct{}
	stop    func()
	waiters []chan error
}

func digestFrom(cfg map[string]any) (bool, time.Duration) {
	enabled, ok := cfg[config.NotificationDigestEnabledKey].(bool)
	if !ok || !enabled {
		return false, 0
	}
	seconds := intFromAny(cfg[config.NotificationDigestSecondsKey])
	if seconds < MinDigestSeconds || seconds > MaxDigestSeconds {
		seconds = DefaultDigestSeconds
	}
	return true, time.Duration(seconds) * time.Second
}

func intFromAny(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return int(n)
	default:
		return 0
	}
}

// NormalizeDigestSeconds checks the settings value for the batch window.
func NormalizeDigestSeconds(raw any) (int, error) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, errDigestWindow
	}
	seconds := int(value)
	if seconds < MinDigestSeconds || seconds > MaxDigestSeconds {
		return 0, errDigestWindow
	}
	return seconds, nil
}

var errDigestWindow = errors.New("digest window must be a whole number of seconds from 1 to 3600")

func enqueueDigest(event models.EventMessage, window time.Duration) <-chan error {
	key := digestKey(event)
	waiter := make(chan error, 1)
	digestMu.Lock()
	batch := digestBatches[key]
	start := false
	if batch == nil {
		copied := event
		copied.Clients = nil
		batch = &digestBatch{event: copied, seen: map[string]struct{}{}}
		digestBatches[key] = batch
		start = true
	}
	batch.waiters = append(batch.waiters, waiter)
	mergeDigestClients(batch, event)
	if event.Time.After(batch.event.Time) {
		batch.event.Time = event.Time
	}
	digestMu.Unlock()
	if start {
		stop := scheduleDigest(window, func() { flushDigestKey(key) })
		digestMu.Lock()
		current := digestBatches[key]
		if current == nil {
			digestMu.Unlock()
			stop()
		} else if current.stop == nil {
			current.stop = stop
			digestMu.Unlock()
		} else {
			digestMu.Unlock()
			stop()
		}
	}
	if afterDigestEnqueue != nil {
		afterDigestEnqueue()
	}
	return waiter
}

// afterDigestEnqueue lets tests observe that a caller is waiting on the batch.
var afterDigestEnqueue func()

func digestKey(event models.EventMessage) string {
	return event.Kind + "\x00" + event.Event + "\x00" + event.Message + "\x00" + event.Emoji
}

func mergeDigestClients(batch *digestBatch, event models.EventMessage) {
	for _, client := range event.Clients {
		id := strings.TrimSpace(client.UUID)
		if id == "" {
			id = strings.TrimSpace(client.Name)
		}
		if id != "" {
			if _, ok := batch.seen[id]; ok {
				continue
			}
			batch.seen[id] = struct{}{}
		}
		batch.event.Clients = append(batch.event.Clients, client)
	}
}

func flushDigestKey(key string) {
	digestMu.Lock()
	batch := digestBatches[key]
	if batch == nil {
		digestMu.Unlock()
		return
	}
	delete(digestBatches, key)
	stop := batch.stop
	event := batch.event
	waiters := batch.waiters
	digestMu.Unlock()
	if stop != nil {
		stop()
	}
	err := deliverEvent(event)
	if err != nil {
		logger.Errorf("message-sender", "Failed to send batched %s: %v", event.Event, err)
	}
	for _, waiter := range waiters {
		waiter <- err
	}
}

func flushDigestBatches() {
	digestMu.Lock()
	keys := make([]string, 0, len(digestBatches))
	for key := range digestBatches {
		keys = append(keys, key)
	}
	digestMu.Unlock()
	for _, key := range keys {
		flushDigestKey(key)
	}
}
