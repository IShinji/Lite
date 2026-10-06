package metric

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestClosedHourTierMatchesMinuteFold(t *testing.T) {
	for _, retentionDays := range []int{30, 1} {
		t.Run(fmt.Sprintf("%dd", retentionDays), func(t *testing.T) {
			testClosedHourTierMatchesMinuteFold(t, retentionDays)
		})
	}
}

func testClosedHourTierMatchesMinuteFold(t *testing.T, retentionDays int) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 48 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 14 * 24 * time.Hour},
			{Interval: time.Hour, Retention: 14 * 24 * time.Hour},
		},
	}
	store := newRollupStore(t, policy)
	if err := store.CreateMetric(ctx, Definition{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: retentionDays}); err != nil {
		t.Fatalf("create metric: %v", err)
	}

	now := time.Date(2026, 10, 6, 21, 10, 0, 0, time.UTC)
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 10, 6, hour, minute, 0, 0, time.UTC)
	}
	points := []Point{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(18, 10), Value: 10000, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(18, 31), Value: 20, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(18, 40), Value: 10, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(19, 5), Value: 30, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(19, 6), Value: -1, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(20, 30), Value: 50, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(20, 58), Value: 90, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: at(21, 5), Value: 70, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-b", Timestamp: at(19, 20), Value: 80, Tags: map[string]string{"task_id": "two"}},
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatalf("compact: %v", err)
	}

	start := at(18, 30)
	base := AggregateQuery{
		Query:          Query{MetricName: sqliteMergedPingLatencyMetric, Start: start, End: now, Order: OrderAsc},
		Aggregation:    AggAvg,
		Interval:       time.Hour,
		PreserveSeries: true,
	}
	closed := base
	closed.ClosedBucketsFromMatchingTier = true

	before, err := store.Series(ctx, base, now)
	if err != nil {
		t.Fatalf("minute fold: %v", err)
	}
	after, err := store.DashboardSeries(ctx, closed, now)
	if err != nil {
		t.Fatalf("closed hour tier: %v", err)
	}
	want := []AggregatePoint{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Bucket: at(18, 0), Value: 15, Count: 2, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Bucket: at(19, 0), Value: 30, Count: 2, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-b", Bucket: at(19, 0), Value: 80, Count: 1, Tags: map[string]string{"task_id": "two"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Bucket: at(20, 0), Value: 70, Count: 2, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Bucket: at(21, 0), Value: 70, Count: 1, Tags: map[string]string{"task_id": "one"}},
	}
	if !sameAggregates(before, want) {
		t.Fatalf("minute fold changed\ngot=%#v", before)
	}
	if !sameAggregates(after, want) {
		t.Fatalf("closed hour tier diverged\ngot=%#v", after)
	}

	minute := base
	minute.Interval = time.Minute
	minute.ClosedBucketsFromMatchingTier = true
	minuteOn, err := store.Series(ctx, minute, now)
	if err != nil {
		t.Fatalf("flagged minute query: %v", err)
	}
	minute.ClosedBucketsFromMatchingTier = false
	minuteOff, err := store.Series(ctx, minute, now)
	if err != nil {
		t.Fatalf("plain minute query: %v", err)
	}
	if !sameAggregates(minuteOn, minuteOff) {
		t.Fatalf("minute interval changed\non=%#v\noff=%#v", minuteOn, minuteOff)
	}

	for _, table := range []string{store.tables.rollupBlocks, store.tables.rollupValues} {
		if _, err := store.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE resolution_nano = ?", time.Minute.Nanoseconds()); err != nil {
			t.Fatalf("drop minute rollups from %s: %v", table, err)
		}
	}
	withoutMinutes, err := store.Series(ctx, base, now)
	if err != nil {
		t.Fatalf("minute fold after drop: %v", err)
	}
	fromHourTier, err := store.DashboardSeries(ctx, closed, now)
	if err != nil {
		t.Fatalf("hour tier after drop: %v", err)
	}
	if aggregateAt(withoutMinutes, "node-a", at(19, 0)) != nil {
		t.Fatalf("minute fold still returned the closed hour after minute rollups were removed: %#v", withoutMinutes)
	}
	closedHour := aggregateAt(fromHourTier, "node-a", at(19, 0))
	if closedHour == nil || closedHour.Value != 30 || closedHour.Count != 2 {
		t.Fatalf("closed hour should still come from the hour tier, got %#v", fromHourTier)
	}
	if aggregateAt(fromHourTier, "node-a", at(18, 0)) != nil {
		t.Fatalf("leading partial hour must not take the whole hour bucket, got %#v", fromHourTier)
	}
	openHour := aggregateAt(fromHourTier, "node-a", at(20, 0))
	if openHour == nil || openHour.Value != 90 || openHour.Count != 1 {
		t.Fatalf("open hour should keep the raw tail, got %#v", fromHourTier)
	}
}

func sameAggregates(got, want []AggregatePoint) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index].MetricName != want[index].MetricName || got[index].EntityID != want[index].EntityID || !got[index].Bucket.Equal(want[index].Bucket) || got[index].Value != want[index].Value || got[index].Count != want[index].Count || got[index].Tags["task_id"] != want[index].Tags["task_id"] {
			return false
		}
	}
	return true
}

func aggregateAt(points []AggregatePoint, entity string, bucket time.Time) *AggregatePoint {
	for index := range points {
		if points[index].EntityID == entity && points[index].Bucket.Equal(bucket) {
			return &points[index]
		}
	}
	return nil
}
