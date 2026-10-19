package trafficledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBucketStartAlignsTo15Minutes(t *testing.T) {
	tm := time.Date(2026, 7, 1, 10, 44, 59, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 7, 1, 10, 30, 0, 0, time.UTC).Unix(), BucketStart(tm))
}

func TestBucketsFromRecordsMatchDailyUsage(t *testing.T) {
	base := time.Date(2026, 7, 31, 15, 50, 0, 0, time.UTC) // Beijing 23:50
	records := []DeltaRecord{
		{Time: base.Add(5 * time.Minute), NetTotalUp: 110, NetTotalDown: 210, TrafficUp: 10, TrafficDown: 10, TrafficUpSet: true, TrafficDownSet: true},
		{Time: base.Add(20 * time.Minute), NetTotalUp: 150, NetTotalDown: 260, TrafficUp: 40, TrafficDown: 50, TrafficUpSet: true, TrafficDownSet: true},
	}
	prev := &DeltaRecord{Time: base, NetTotalUp: 100, NetTotalDown: 200}
	buckets := bucketsFromRecords(records, prev)
	assert.Equal(t, Usage{Up: 10, Down: 10}, buckets[BucketStart(records[0].Time)])
	assert.Equal(t, Usage{Up: 40, Down: 50}, buckets[BucketStart(records[1].Time)])

	start := time.Date(2026, 7, 31, 0, 0, 0, 0, BeijingLocation)
	days := usagesByDayFromRecords(start, start.AddDate(0, 0, 2), records, prev)
	var sum Usage
	for _, u := range buckets {
		sum.Up += u.Up
		sum.Down += u.Down
	}
	assert.Equal(t, Usage{Up: days["2026-07-31"].Up + days["2026-08-01"].Up, Down: days["2026-07-31"].Down + days["2026-08-01"].Down}, sum)
}

func TestEnsureBucketRangeWritesFullDaysIdempotently(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-basic")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	day := time.Date(2026, 7, 30, 0, 0, 0, 0, BeijingLocation)
	calls := 0
	calc := func(_ context.Context, _ string, start, end time.Time) (BucketResult, error) {
		calls++
		assert.LessOrEqual(t, end.Sub(start), 2*24*time.Hour)
		return BucketResult{HasBaseline: true, Usage: map[int64]Usage{start.Unix() + 900: {Up: 5, Down: 7}}}, nil
	}
	resetBucketSkip("client-a")
	end := day.AddDate(0, 0, 2)
	require.NoError(t, ensureBucketRange(context.Background(), db, []string{"client-a"}, day, end, calc))
	// One probe over the backlog, then each day in 6 hour pieces.
	assert.Equal(t, 1+2*4, calls)
	require.NoError(t, ensureBucketRange(context.Background(), db, []string{"client-a"}, day, end, calc))
	assert.Equal(t, 1+2*4, calls, "settled days must not be rescanned")
	var count int64
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Count(&count).Error)
	assert.Equal(t, int64(2*BucketsPerBeijingDay), count)
	var row models.TrafficBucketLedger
	require.NoError(t, db.Where("client = ? AND bucket_start = ?", "client-a", day.Unix()+900).Take(&row).Error)
	assert.Equal(t, int64(5), row.UpBytes)
	assert.Equal(t, int64(7), row.DownBytes)
}

func TestEnsureBucketRangeDoesNotFabricateDaysWithoutSource(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-nosource")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, BeijingLocation)
	// The first retained record is on day 3 and nothing was written while
	// day 3 was in progress, so day 3 is incomplete and only days 4.. are
	// written.
	first := day.AddDate(0, 0, 3).Add(6 * time.Hour)
	calc := func(_ context.Context, _ string, start, end time.Time) (BucketResult, error) {
		if !start.After(first) {
			result := BucketResult{Usage: map[int64]Usage{}}
			if first.Before(end) {
				result.FirstRecord = first
				result.Usage[BucketStart(first)] = Usage{Up: 1}
			}
			return result, nil
		}
		return BucketResult{HasBaseline: true, FirstRecord: start, Usage: map[int64]Usage{BucketStart(start): {Up: 1}}}, nil
	}
	resetBucketSkip("client-a")
	require.NoError(t, ensureBucketRange(context.Background(), db, []string{"client-a"}, day, day.AddDate(0, 0, 6), calc))
	var minStart int64
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Select("MIN(bucket_start)").Scan(&minStart).Error)
	assert.Equal(t, day.AddDate(0, 0, 4).Unix(), minStart)
	var n int64
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Count(&n).Error)
	assert.Equal(t, int64(2*BucketsPerBeijingDay), n)
	// Days 0..3 are known to be unrecoverable now and are not read again.
	assert.Equal(t, day.AddDate(0, 0, 4), bucketSkipUntil["client-a"])

	// A client with no records at all gets nothing.
	require.NoError(t, db.Create(&models.Client{UUID: "client-b", Token: "token-b"}).Error)
	empty := func(_ context.Context, _ string, _, _ time.Time) (BucketResult, error) { return BucketResult{}, nil }
	resetBucketSkip("client-b")
	require.NoError(t, ensureBucketRange(context.Background(), db, []string{"client-b"}, day, day.AddDate(0, 0, 6), empty))
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Where("client = ?", "client-b").Count(&n).Error)
	assert.Zero(t, n)
	// An empty read is not evidence that the days are unrecoverable.
	_, skipped := bucketSkipUntil["client-b"]
	assert.False(t, skipped)
}

// A day whose start aged out of the metric store (no baseline) but that has
// incremental rows is settled without overwriting them: buckets up to the
// first retained record are only inserted when missing, later ones replaced.
func TestEnsureBucketRangeKeepsIncrementalRowsWithoutBaseline(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-incremental")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, BeijingLocation)
	first := day.Add(time.Hour + 2*time.Minute)
	require.NoError(t, db.Create(&[]models.TrafficBucketLedger{
		{Client: "client-a", BucketStart: day.Unix(), UpBytes: 100, DownBytes: 200},
		{Client: "client-a", BucketStart: BucketStart(first), UpBytes: 110, DownBytes: 210},
		{Client: "client-a", BucketStart: day.Add(5 * time.Hour).Unix(), UpBytes: 1, DownBytes: 1},
	}).Error)
	calc := func(_ context.Context, _ string, start, end time.Time) (BucketResult, error) {
		result := BucketResult{Usage: map[int64]Usage{}}
		if start.After(first) {
			result.HasBaseline = true
		} else if first.Before(end) {
			result.FirstRecord = first
		}
		for key := start.Unix(); key < end.Unix(); key += BucketSeconds {
			if key >= BucketStart(first) {
				result.Usage[key] = Usage{Up: 10, Down: 20}
			}
		}
		return result, nil
	}
	resetBucketSkip("client-a")
	require.NoError(t, ensureBucketRange(context.Background(), db, []string{"client-a"}, day, day.AddDate(0, 0, 1), calc))
	var rows []models.TrafficBucketLedger
	require.NoError(t, db.Where("client = ?", "client-a").Order("bucket_start").Find(&rows).Error)
	require.Len(t, rows, BucketsPerBeijingDay)
	byStart := map[int64]models.TrafficBucketLedger{}
	for _, row := range rows {
		byStart[row.BucketStart] = row
	}
	assert.Equal(t, int64(100), byStart[day.Unix()].UpBytes, "incremental row before the first record is kept")
	assert.Equal(t, int64(110), byStart[BucketStart(first)].UpBytes, "the truncated first-record bucket keeps the incremental value")
	assert.Zero(t, byStart[day.Add(15*time.Minute).Unix()].UpBytes, "missing early buckets are filled with zero")
	assert.Equal(t, int64(10), byStart[day.Add(5*time.Hour).Unix()].UpBytes, "buckets after the first record are recomputed")
	assert.Equal(t, int64(20), byStart[day.Add(24*time.Hour-15*time.Minute).Unix()].DownBytes)
}

// Today's ended buckets are written on every run; the 23:45 marker never is.
func TestEnsureTodayBucketsStopsBeforeCurrentBucketAndMarker(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-today")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, BeijingLocation)
	calc := func(_ context.Context, _ string, start, end time.Time) (BucketResult, error) {
		assert.LessOrEqual(t, end.Sub(start), bucketChunk)
		result := BucketResult{HasBaseline: true, Usage: map[int64]Usage{}}
		for key := start.Unix(); key < end.Unix()+3600; key += BucketSeconds {
			result.Usage[key] = Usage{Up: 1, Down: 2}
		}
		return result, nil
	}
	require.NoError(t, ensureTodayBuckets(context.Background(), db, []string{"client-a"}, day.Add(10*time.Hour+20*time.Minute), calc))
	var maxStart int64
	var n int64
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Count(&n).Error)
	assert.Equal(t, int64(41), n) // 00:00 .. 10:00
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Select("MAX(bucket_start)").Scan(&maxStart).Error)
	assert.Equal(t, day.Add(10*time.Hour).Unix(), maxStart)

	require.NoError(t, ensureTodayBuckets(context.Background(), db, []string{"client-a"}, day.Add(24*time.Hour-time.Second), calc))
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Count(&n).Error)
	assert.Equal(t, int64(BucketsPerBeijingDay-1), n)
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Select("MAX(bucket_start)").Scan(&maxStart).Error)
	assert.Equal(t, day.Add(24*time.Hour-30*time.Minute).Unix(), maxStart)
}

func TestMaintainBucketLedgerCleansExpiredAndOrphanRows(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-clean")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	today := time.Date(2026, 8, 20, 0, 0, 0, 0, BeijingLocation)
	old := today.AddDate(0, 0, -BucketLedgerRetentionDays-1).Unix()
	require.NoError(t, db.Create(&models.TrafficBucketLedger{Client: "client-a", BucketStart: old}).Error)
	calc := func(_ context.Context, _ string, _, _ time.Time) (BucketResult, error) { return BucketResult{}, nil }
	require.NoError(t, maintainBucketLedgerWith(context.Background(), db, []string{"client-a"}, today, calc))
	var n int64
	require.NoError(t, db.Model(&models.TrafficBucketLedger{}).Count(&n).Error)
	assert.Zero(t, n)
}

func TestScanBucketLedgerReadsRange(t *testing.T) {
	db := openLedgerTestDB(t, "bucket-ledger-scan")
	require.NoError(t, db.AutoMigrate(&models.TrafficBucketLedger{}))
	require.NoError(t, db.Create(&[]models.TrafficBucketLedger{
		{Client: "client-a", BucketStart: 900, UpBytes: 1, DownBytes: 2},
		{Client: "client-a", BucketStart: 1800, UpBytes: 3, DownBytes: 4},
	}).Error)
	got := map[int64]Usage{}
	require.NoError(t, ScanBucketLedger(context.Background(), db, time.Unix(1000, 0), time.Unix(3000, 0), func(_ string, b int64, u Usage) { got[b] = u }))
	assert.Equal(t, map[int64]Usage{1800: {Up: 3, Down: 4}}, got)
}

func TestParseTZ(t *testing.T) {
	for _, bad := range []string{"", "Local", "Not/AZone", "a b", "../etc", fmt.Sprintf("%0100d", 1), "UTC;drop"} {
		_, name, ok := ParseTZ(bad)
		assert.Equal(t, DefaultTZName, name, bad)
		assert.False(t, ok, bad)
	}
	loc, name, ok := ParseTZ("America/New_York")
	require.True(t, ok)
	assert.Equal(t, "America/New_York", name)
	assert.Equal(t, "America/New_York", loc.String())
	_, _, ok = ParseTZ("Asia/Shanghai")
	assert.True(t, ok)
}

func TestTZWindowBoundariesAcrossZones(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		zone      string
		todayKey  string
		todayUTC  time.Time
		dayLength time.Duration
	}{
		"UTC-12":  {"Etc/GMT+12", "2026-07-10", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC), 24 * time.Hour},
		"UTC+14":  {"Pacific/Kiritimati", "2026-07-11", time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC), 24 * time.Hour},
		"+5:30":   {"Asia/Kolkata", "2026-07-10", time.Date(2026, 7, 9, 18, 30, 0, 0, time.UTC), 24 * time.Hour},
		"+5:45":   {"Asia/Kathmandu", "2026-07-10", time.Date(2026, 7, 9, 18, 15, 0, 0, time.UTC), 24 * time.Hour},
		"+12:45":  {"Pacific/Chatham", "2026-07-11", time.Date(2026, 7, 10, 11, 15, 0, 0, time.UTC), 24 * time.Hour},
		"Beijing": {"Asia/Shanghai", "2026-07-10", time.Date(2026, 7, 9, 16, 0, 0, 0, time.UTC), 24 * time.Hour},
	}
	for name, c := range cases {
		loc, _, ok := ParseTZ(c.zone)
		require.True(t, ok, name)
		starts, keys := TZWindow(loc, now, 30)
		require.Len(t, starts, 31, name)
		require.Len(t, keys, 30, name)
		assert.Equal(t, c.todayKey, keys[29], name)
		assert.True(t, starts[29].Equal(c.todayUTC), "%s: today start %s want %s", name, starts[29].UTC(), c.todayUTC)
		assert.Equal(t, c.dayLength, starts[30].Sub(starts[29]), name)
		assert.Zero(t, starts[29].Unix()%BucketSeconds, "%s start must be bucket aligned", name)
	}
}

func TestTZWindowDSTDaysAre23And25Hours(t *testing.T) {
	loc, _, _ := ParseTZ("America/New_York")
	spring := time.Date(2026, 3, 8, 20, 0, 0, 0, time.UTC)
	starts, keys := TZWindow(loc, spring, 3)
	assert.Equal(t, []string{"2026-03-06", "2026-03-07", "2026-03-08"}, keys)
	assert.Equal(t, 23*time.Hour, starts[3].Sub(starts[2]))
	fall := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	starts, keys = TZWindow(loc, fall, 3)
	assert.Equal(t, "2026-11-01", keys[2])
	assert.Equal(t, 25*time.Hour, starts[3].Sub(starts[2]))
}

func TestTZTodayAndYesterdayBoundary(t *testing.T) {
	loc, _, _ := ParseTZ("America/New_York")
	justBefore := time.Date(2026, 7, 10, 3, 59, 59, 0, time.UTC) // 23:59:59 on 9th
	justAfter := time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC)    // 00:00 on 10th
	_, k1 := TZWindow(loc, justBefore, 2)
	_, k2 := TZWindow(loc, justAfter, 2)
	assert.Equal(t, "2026-07-09", k1[1])
	assert.Equal(t, "2026-07-10", k2[1])
}

func TestDayAlignedWithBeijing(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for zone, want := range map[string]bool{"Asia/Shanghai": true, "Asia/Singapore": true, "UTC": false, "Asia/Kolkata": false} {
		loc, _, _ := ParseTZ(zone)
		starts, _ := TZWindow(loc, now, 2)
		assert.Equal(t, want, DayAlignedWithBeijing(starts[0], starts[1]), zone)
	}
}

func TestSplitAdjustmentConservesTotalsAndAlignsOneToOne(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	bj := time.Date(2026, 7, 5, 0, 0, 0, 0, BeijingLocation)
	adj := SignedUsage{Up: 1001, Down: -333}

	loc, _, _ := ParseTZ("Asia/Shanghai")
	starts, keys := TZWindow(loc, now, 30)
	pieces := SplitAdjustment(bj, adj, starts)
	var idx int
	for i, k := range keys {
		if k == "2026-07-05" {
			idx = i
		}
	}
	assert.Equal(t, map[int]SignedUsage{idx: adj}, pieces)

	loc, _, _ = ParseTZ("America/New_York")
	starts, _ = TZWindow(loc, now, 30)
	pieces = SplitAdjustment(bj, adj, starts)
	require.Len(t, pieces, 2)
	var sum SignedUsage
	for _, p := range pieces {
		sum.Up += p.Up
		sum.Down += p.Down
	}
	assert.Equal(t, adj, sum)

	// Beijing day outside the window is clamped, never dropped.
	far := time.Date(2026, 6, 1, 0, 0, 0, 0, BeijingLocation)
	pieces = SplitAdjustment(far, adj, starts)
	assert.Equal(t, map[int]SignedUsage{0: adj}, pieces)
}
