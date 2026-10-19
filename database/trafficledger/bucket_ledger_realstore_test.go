package trafficledger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// realStoreSim runs the bucket ledger against a real SQLite metric store set
// up like a fresh deployment: NetTotal* counters, Traffic* gauges, the default
// rollup policy and the default 1 day metric retention. A simulated clock
// drives writes, compaction, retention, the read path and Maintain.
type realStoreSim struct {
	t     *testing.T
	store *metric.Store
	db    *gorm.DB
	now   time.Time
	agent *fakeAgent
}

func newRealStoreSim(t *testing.T, name string) *realStoreSim {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "metrics.db") + "?mode=rwc&_txlock=immediate"
	store, err := metricstore.OpenStoreForMigrationWithProgress(context.Background(),
		&metricstore.MetricStoreConfig{Driver: "sqlite", DSN: dsn}, 1, nil)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	defs, err := store.ListMetrics(context.Background())
	require.NoError(t, err)
	checked := 0
	for _, def := range defs {
		switch def.Name {
		case metricstore.MetricNetTotalUp, metricstore.MetricNetTotalDown:
			require.Equal(t, metric.TypeCounter, def.Type)
		case metricstore.MetricTrafficUp, metricstore.MetricTrafficDown:
			require.Equal(t, metric.TypeGauge, def.Type)
		default:
			continue
		}
		require.Equal(t, 1, def.RetentionDays, def.Name)
		checked++
	}
	require.Equal(t, 4, checked)

	sim := &realStoreSim{t: t, store: store, agent: &fakeAgent{truth: map[int64]Usage{}}}
	t.Cleanup(metricstore.SwapStoreForTest(store))
	t.Cleanup(metricstore.SwapTrafficQueryClockForTest(func() time.Time { return sim.now }))
	sim.db = openLedgerTestDB(t, name)
	require.NoError(t, sim.db.AutoMigrate(&models.TrafficBucketLedger{}))
	resetBucketSkip("client-a")
	return sim
}

// report writes one hour of per-minute agent reports starting at hourStart.
func (s *realStoreSim) report(hourStart time.Time) {
	var points []metric.Point
	for m := 0; m < 60; m++ {
		points = append(points, s.agent.points("client-a", hourStart.Add(time.Duration(m)*time.Minute))...)
	}
	require.NoError(s.t, s.store.WriteBatch(context.Background(), points))
}

// maintain runs the hourly jobs (metric compaction + retention, ledger) at now.
func (s *realStoreSim) maintain(now time.Time) {
	ctx := context.Background()
	s.now = now
	_, err := s.store.Compact(ctx, now.UTC())
	require.NoError(s.t, err)
	_, err = s.store.CleanupExpired(ctx, now.UTC())
	require.NoError(s.t, err)
	require.NoError(s.t, Maintain(ctx, s.db, now.UTC()))
}

func (s *realStoreSim) dayRows(day time.Time) ([]models.TrafficBucketLedger, Usage) {
	var rows []models.TrafficBucketLedger
	require.NoError(s.t, s.db.Where("client = ? AND bucket_start >= ? AND bucket_start < ?",
		"client-a", day.Unix(), day.AddDate(0, 0, 1).Unix()).Order("bucket_start").Find(&rows).Error)
	var sum Usage
	for _, row := range rows {
		sum.Up += row.UpBytes
		sum.Down += row.DownBytes
	}
	return rows, sum
}

// fakeAgent mimics report.go: one report per minute with cumulative counters
// and the per-report delta stored in the Traffic gauges (0 on the first one).
type fakeAgent struct {
	up, down int64
	started  bool
	truth    map[int64]Usage // bucket start -> traffic reported in it
}

func (a *fakeAgent) points(clientID string, ts time.Time) []metric.Point {
	minute := ts.Unix() / 60
	du := 1000 + (minute%15)*37 + (minute%7)*11
	dd := 3*du + minute%5
	var tu, td int64
	if a.started {
		tu, td = du, dd
	}
	a.up += du
	a.down += dd
	a.started = true
	key := BucketStart(ts)
	u := a.truth[key]
	u.Up += tu
	u.Down += td
	a.truth[key] = u
	t := ts.UTC()
	return []metric.Point{
		{MetricName: metricstore.MetricNetTotalUp, EntityID: clientID, Timestamp: t, Value: float64(a.up)},
		{MetricName: metricstore.MetricNetTotalDown, EntityID: clientID, Timestamp: t, Value: float64(a.down)},
		{MetricName: metricstore.MetricTrafficUp, EntityID: clientID, Timestamp: t, Value: float64(tu)},
		{MetricName: metricstore.MetricTrafficDown, EntityID: clientID, Timestamp: t, Value: float64(td)},
	}
}

func (a *fakeAgent) sum(from, to time.Time) Usage {
	var total Usage
	for key, u := range a.truth {
		if key >= from.Unix() && key < to.Unix() {
			total.Up += u.Up
			total.Down += u.Down
		}
	}
	return total
}

func resetBucketSkip(clientID string) {
	ensureMu.Lock()
	delete(bucketSkipUntil, clientID)
	ensureMu.Unlock()
}

// Production default: metrics are kept for 1 day. The agent reports every
// minute; metric compaction/retention and ledger maintenance run hourly for
// four days. Every ended Beijing day must end up with 96 buckets whose sum is
// the traffic actually reported that day, while the Beijing daily ledger
// settled in the same runs has data too.
func TestMaintainBucketLedgerWithOneDayMetricRetention(t *testing.T) {
	sim := newRealStoreSim(t, "bucket-ledger-realstore-1d")
	start := time.Date(2026, 10, 7, 10, 0, 0, 0, BeijingLocation)
	var lastNow time.Time
	for hour := 0; hour < 4*24; hour++ {
		hourStart := start.Add(time.Duration(hour) * time.Hour)
		sim.report(hourStart)
		lastNow = hourStart.Add(time.Hour + 2*time.Minute)
		sim.maintain(lastNow)
	}

	today := BeijingDay(lastNow)
	for day := BeijingDay(start); day.Before(today); day = day.AddDate(0, 0, 1) {
		label := day.Format(time.DateOnly)
		rows, sum := sim.dayRows(day)
		require.Len(t, rows, BucketsPerBeijingDay, label)
		// Exact: buckets written incrementally while the day was still inside
		// the 1 day metric retention are kept when the day is settled; the
		// rest is recomputed from rollups that are still retained.
		assert.Equal(t, sim.agent.sum(day, day.AddDate(0, 0, 1)), sum, "%s buckets vs reported traffic", label)

		var daily models.TrafficDailyLedger
		require.NoError(t, sim.db.Where("client = ? AND day = ?", "client-a", label).Take(&daily).Error)
		t.Logf("%s buckets=%+v daily=%d/%d", label, sum, daily.UpBytes, daily.DownBytes)
		require.NotZero(t, daily.UpBytes, "%s: daily ledger has data in the same conditions", label)
		// The Beijing daily ledger (upstream logic, not changed here) settles
		// day D once, at the first run after D ends (D+1 00:02). With 1 day
		// retention the points before D 00:02 are gone by then, so it misses
		// the first minutes of D (2 minutes here; the gap grows with the
		// delay of that first run). The bucket ledger kept them from the
		// incremental writes. Allowed gap: the first bucket (15 minutes) of
		// D. It must never exceed the bucket ledger.
		allowance := sim.agent.sum(day, day.Add(15*time.Minute))
		assert.LessOrEqual(t, daily.UpBytes, sum.Up, label)
		assert.LessOrEqual(t, daily.DownBytes, sum.Down, label)
		assert.LessOrEqual(t, sum.Up-daily.UpBytes, allowance.Up, label)
		assert.LessOrEqual(t, sum.Down-daily.DownBytes, allowance.Down, label)
	}

	// Today: incremental rows for every ended bucket, never the 23:45 marker.
	todayRows, todaySum := sim.dayRows(today)
	require.NotEmpty(t, todayRows)
	assert.Less(t, todayRows[len(todayRows)-1].BucketStart, BucketStart(lastNow))
	assert.Equal(t, today.Unix(), todayRows[0].BucketStart)
	assert.Equal(t, sim.agent.sum(today, time.Unix(BucketStart(lastNow), 0)), todaySum)
}

// The settled marker (last bucket of the Beijing day) must never be written
// before the day ends, even by a run at 23:59:59.
func TestMaintainBucketLedgerNeverWritesTodayMarkerEarly(t *testing.T) {
	sim := newRealStoreSim(t, "bucket-ledger-realstore-marker")
	start := time.Date(2026, 10, 7, 22, 0, 0, 0, BeijingLocation)
	sim.report(start)
	sim.report(start.Add(time.Hour))
	sim.maintain(start.Add(time.Hour + 59*time.Minute + 59*time.Second))
	day := BeijingDay(start)
	rows, _ := sim.dayRows(day)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Less(t, row.BucketStart, day.AddDate(0, 0, 1).Unix()-BucketSeconds)
	}
	// After midnight the day settles with all 96 buckets.
	sim.maintain(day.AddDate(0, 0, 1).Add(time.Minute))
	rows, sum := sim.dayRows(day)
	assert.Len(t, rows, BucketsPerBeijingDay)
	assert.Equal(t, sim.agent.sum(day, day.AddDate(0, 0, 1)), sum)
}

// When the metric store reads empty on the first run (e.g. still starting),
// that must not mark the following days as unrecoverable.
func TestMaintainBucketLedgerRecoversAfterEmptyFirstRun(t *testing.T) {
	sim := newRealStoreSim(t, "bucket-ledger-realstore-empty-first")
	start := time.Date(2026, 10, 7, 10, 0, 0, 0, BeijingLocation)
	sim.maintain(start)
	var lastNow time.Time
	for hour := 0; hour < 3*24; hour++ {
		hourStart := start.Add(time.Duration(hour) * time.Hour)
		sim.report(hourStart)
		lastNow = hourStart.Add(time.Hour + 2*time.Minute)
		sim.maintain(lastNow)
	}
	for day := BeijingDay(start); day.Before(BeijingDay(lastNow)); day = day.AddDate(0, 0, 1) {
		rows, sum := sim.dayRows(day)
		assert.Len(t, rows, BucketsPerBeijingDay, day.Format(time.DateOnly))
		assert.Equal(t, sim.agent.sum(day, day.AddDate(0, 0, 1)), sum, day.Format(time.DateOnly))
	}
}

// With a 30 day metric retention a backlog of 30 days is settled in one run.
// Each 15 minute bucket must hold its own traffic, not an hour's worth
// stacked into one bucket by the downsampled 30 day scan.
func TestEnsureBucketRangeKeeps15MinuteResolutionOver30Days(t *testing.T) {
	sim := newRealStoreSim(t, "bucket-ledger-realstore-30d")
	ctx := context.Background()
	for _, name := range []string{metricstore.MetricNetTotalUp, metricstore.MetricNetTotalDown, metricstore.MetricTrafficUp, metricstore.MetricTrafficDown} {
		def, err := sim.store.GetMetric(ctx, name)
		require.NoError(t, err)
		def.RetentionDays = 30
		require.NoError(t, sim.store.UpsertMetric(ctx, def))
	}
	today := time.Date(2026, 10, 7, 0, 0, 0, 0, BeijingLocation)
	first := today.AddDate(0, 0, -30)
	for ts := first; ts.Before(today.Add(time.Hour)); ts = ts.Add(time.Hour) {
		sim.report(ts)
	}
	sim.now = today.Add(time.Hour + 2*time.Minute)
	_, err := sim.store.Compact(ctx, sim.now.UTC())
	require.NoError(t, err)
	require.NoError(t, EnsureBucketRange(ctx, sim.db, []string{"client-a"}, first, today))

	// Days older than the 5 minute rollup tier (14 days) only have hourly
	// rollups; 15 minute resolution is only possible for the recent ones.
	for day := today.AddDate(0, 0, -13); day.Before(today); day = day.AddDate(0, 0, 1) {
		rows, _ := sim.dayRows(day)
		require.Len(t, rows, BucketsPerBeijingDay, day.Format(time.DateOnly))
		for _, row := range rows {
			want := sim.agent.truth[row.BucketStart]
			require.Equal(t, want.Up, row.UpBytes, "bucket %s", time.Unix(row.BucketStart, 0).In(BeijingLocation))
			require.Equal(t, want.Down, row.DownBytes, "bucket %s", time.Unix(row.BucketStart, 0).In(BeijingLocation))
		}
	}
}
