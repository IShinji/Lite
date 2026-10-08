package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
)

// A bucket made only of failed probes (value -1) is aggregated by the store
// over zero valid samples, which yields Value 0 with Count equal to the number
// of failed probes. This documents how a 0 reaches the ping stats.
func TestPingLatencyBucketOfOnlyFailuresAggregatesToZero(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	points := []metric.Point{
		{MetricName: "ping.latency_ms", EntityID: "node-a", Timestamp: base, Value: -1},
		{MetricName: "ping.latency_ms", EntityID: "node-a", Timestamp: base.Add(time.Second), Value: -1},
	}
	got, err := metric.AggregatePoints(points, metric.AggregateQuery{
		Query:       metric.Query{MetricName: "ping.latency_ms", EntityID: "node-a", Start: base, End: base.Add(time.Hour)},
		Aggregation: metric.AggAvg,
		Interval:    time.Minute,
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(got) != 1 || got[0].Value != 0 || got[0].Count != 2 {
		t.Fatalf("expected one zero-valued bucket with count 2, got %#v", got)
	}
}

func TestPublicPingStatsFromAggregateGroupsAllLostOmitsLatency(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	zero := map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "Tokyo ICMP", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricAggregateGroups{
		Avg: zero, Min: zero, Max: zero, Last: zero, P50: zero, P99: zero, StdDev: zero,
		Loss:          map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 1}}},
		LossAvailable: true,
	}
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	got := stats[0]
	if got.Valid != 0 || got.Loss != 100 {
		t.Fatalf("expected valid=0 loss=100, got %#v", got)
	}
	if got.Avg != nil || got.Latest != nil || got.P50 != nil || got.P99 != nil ||
		got.Min != nil || got.Max != nil || got.StdDev != nil {
		t.Fatalf("latency fields must be omitted when every probe failed: %#v", got)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"avg"`, `"latest"`, `"p50"`, `"p99"`, `"min"`, `"max"`, `"std_dev"`} {
		if strings.Contains(string(payload), key) {
			t.Fatalf("%s must be omitted from %s", key, payload)
		}
	}
}

func TestPublicPingStatsFromAggregateGroupsKeepsZeroLatencyWithValidSamples(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	zero := map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "LAN", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricAggregateGroups{
		Avg: zero, Min: zero, Max: zero, Last: zero, P50: zero, P99: zero, StdDev: zero,
		Loss:          map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}},
		LossAvailable: true,
	}
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	got := stats[0]
	if got.Valid != 4 || got.Loss != 0 {
		t.Fatalf("unexpected totals: %#v", got)
	}
	for name, value := range map[string]*float64{
		"avg": got.Avg, "latest": got.Latest, "p50": got.P50, "p99": got.P99,
		"min": got.Min, "max": got.Max, "stddev": got.StdDev,
	} {
		if value == nil || *value != 0 {
			t.Fatalf("%s: sub-millisecond latency 0 must be preserved, got %v", name, value)
		}
	}
}
