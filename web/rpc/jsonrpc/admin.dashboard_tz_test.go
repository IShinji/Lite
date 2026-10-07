package jsonrpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTZTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.TrafficDailyLedger{}, &models.TrafficBucketLedger{}, &models.TrafficCalibrationAdjustment{}))
	require.NoError(t, db.Create(&models.Client{UUID: "c1", Token: "t1", Name: "one", Price: 1, TrafficLimitType: "sum"}).Error)
	return db
}

// seedBuckets writes every 15 minute bucket (1 up, 2 down) for Beijing days
// [from, to) plus the matching daily ledger rows.
func seedBuckets(t *testing.T, db *gorm.DB, from, to time.Time) {
	t.Helper()
	var rows []models.TrafficBucketLedger
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		for i := 0; i < trafficledger.BucketsPerBeijingDay; i++ {
			rows = append(rows, models.TrafficBucketLedger{Client: "c1", BucketStart: d.Unix() + int64(i)*900, UpBytes: 1, DownBytes: 2})
		}
		require.NoError(t, db.Create(&models.TrafficDailyLedger{Client: "c1", Day: d.Format(time.DateOnly), UpBytes: 96, DownBytes: 192}).Error)
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)
}

func stubLive(t *testing.T, live map[int64]trafficledger.Usage) {
	t.Helper()
	previous := dashboardLiveBuckets
	dashboardLiveBuckets = func(_ context.Context, ids []string, _, _ time.Time) (map[string]map[int64]trafficledger.Usage, error) {
		return map[string]map[int64]trafficledger.Usage{"c1": live}, nil
	}
	t.Cleanup(func() { dashboardLiveBuckets = previous })
}

// stubLiveFull emulates the metric store: 1 up / 2 down in every bucket of
// the requested live range, like the seeded ledger.
func stubLiveFull(t *testing.T) {
	t.Helper()
	previous := dashboardLiveBuckets
	dashboardLiveBuckets = func(_ context.Context, _ []string, start, end time.Time) (map[string]map[int64]trafficledger.Usage, error) {
		out := map[int64]trafficledger.Usage{}
		for b := trafficledger.BucketStart(start); b <= end.Unix(); b += 900 {
			out[b] = trafficledger.Usage{Up: 1, Down: 2}
		}
		return map[string]map[int64]trafficledger.Usage{"c1": out}, nil
	}
	t.Cleanup(func() { dashboardLiveBuckets = previous })
}

func tzData(t *testing.T, db *gorm.DB, zone string, now time.Time) (*tzTrafficData, dashboardTrafficSummary) {
	t.Helper()
	tz := parseDashboardTZ(zone)
	clientList, err := listDashboardTrafficClientsFrom(context.Background(), db)
	require.NoError(t, err)
	data, err := loadDashboardTZData(context.Background(), db, clientList, now, tz, 0, trafficledger.DashboardHistoryDays-1)
	require.NoError(t, err)
	return data, summarizeDashboardTrafficTZ(clientList, data, 5)
}

func TestDashboardTZDailyUsesViewerDayBoundaries(t *testing.T) {
	db := openTZTestDB(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	seedBuckets(t, db, b0.AddDate(0, 0, -33), b0)
	stubLiveFull(t)
	for _, zone := range []string{"Etc/GMT+12", "Pacific/Kiritimati", "Asia/Kolkata", "Asia/Kathmandu", "Pacific/Chatham", "America/New_York", "UTC"} {
		data, summary := tzData(t, db, zone, now)
		require.Len(t, summary.Daily, trafficledger.DashboardHistoryDays, zone)
		for i := 0; i < trafficledger.DashboardHistoryDays-1; i++ {
			want := int64(data.starts[i+1].Sub(data.starts[i]) / (15 * time.Minute))
			assert.Equal(t, want, summary.Daily[i].Up, "%s day %s", zone, summary.Daily[i].Day)
			assert.Equal(t, 2*want, summary.Daily[i].Down, zone)
			assert.False(t, summary.Daily[i].Partial, zone)
		}
		require.NotNil(t, summary.HistoryComplete, zone)
		assert.True(t, *summary.HistoryComplete, zone)
		assert.Nil(t, summary.LedgerSince, zone)
	}
}

func TestDashboardTZDSTDayHas23And25Hours(t *testing.T) {
	db := openTZTestDB(t)
	b := func(now time.Time) time.Time { return trafficledger.BeijingDay(now) }
	spring := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	seedBuckets(t, db, b(spring).AddDate(0, 0, -33), b(spring))
	stubLiveFull(t)
	_, summary := tzData(t, db, "America/New_York", spring)
	var found bool
	for _, d := range summary.Daily {
		if d.Day == "2026-03-08" {
			found = true
			assert.Equal(t, int64(23*4), d.Up)
		}
	}
	assert.True(t, found)
}

func TestDashboardTZTodayAndYesterdayBoundary(t *testing.T) {
	db := openTZTestDB(t)
	// 04:30 UTC on the 10th: New York is 00:30 on the 10th.
	now := time.Date(2026, 7, 10, 4, 30, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	seedBuckets(t, db, b0.AddDate(0, 0, -33), b0)
	midnight := time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC).Unix()
	live := map[int64]trafficledger.Usage{
		midnight - 900: {Up: 100, Down: 1}, // 23:45 yesterday (NY)
		midnight:       {Up: 7, Down: 3},   // 00:00 today
		midnight + 900: {Up: 5, Down: 2},   // 00:15 today
	}
	stubLive(t, live)
	data, summary := tzData(t, db, "America/New_York", now)
	assert.Equal(t, "2026-07-10", summary.Daily[29].Day)
	assert.Equal(t, int64(12), summary.TodayUp)
	assert.Equal(t, int64(5), summary.TodayDown)
	// NY 07-09 = 48 seeded buckets before Beijing midnight + the 100 live bytes at 23:45.
	assert.Equal(t, int64(148), data.usage["c1"][28].Up)
	assert.Len(t, summary.Hourly, 1)
	assert.Equal(t, int64(12), summary.Hourly[0].Up)
}

func TestDashboardTZPartialStrategy(t *testing.T) {
	db := openTZTestDB(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	// Bucket ledger only covers the last 5 Beijing days; the daily ledger has
	// non-zero history for all 33 days.
	seedBuckets(t, db, b0.AddDate(0, 0, -33), b0)
	require.NoError(t, db.Where("bucket_start < ?", b0.AddDate(0, 0, -5).Unix()).Delete(&models.TrafficBucketLedger{}).Error)
	stubLiveFull(t)

	// Non-aligned zone: buckets kept as-is, old days flagged, never spliced.
	data, summary := tzData(t, db, "America/New_York", now)
	assert.True(t, summary.Daily[0].Partial)
	assert.Equal(t, int64(0), summary.Daily[0].Up, "no daily ledger value may be spliced into a non-aligned zone")
	assert.False(t, summary.Daily[29].Partial)
	require.NotNil(t, summary.HistoryComplete)
	assert.False(t, *summary.HistoryComplete)
	require.NotNil(t, summary.LedgerSince)
	assert.Equal(t, data.keys[summary.indexOfLedgerSince(data.keys)], *summary.LedgerSince)
	assert.False(t, summary.HistoryReady)

	// Beijing-aligned zone: exact daily ledger value, not partial.
	_, aligned := tzData(t, db, "Asia/Singapore", now)
	assert.False(t, aligned.Daily[0].Partial)
	assert.Equal(t, int64(96), aligned.Daily[0].Up)
	require.NotNil(t, aligned.HistoryComplete)
	assert.True(t, *aligned.HistoryComplete)
}

func (s dashboardTrafficSummary) indexOfLedgerSince(keys []string) int {
	for i, k := range keys {
		if s.LedgerSince != nil && k == *s.LedgerSince {
			return i
		}
	}
	return 0
}

func TestDashboardTZEmptyHistoryIsNotPartial(t *testing.T) {
	db := openTZTestDB(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	// Daily ledger exists but is all zero (new client): nothing could be lost.
	for d := b0.AddDate(0, 0, -33); d.Before(b0); d = d.AddDate(0, 0, 1) {
		require.NoError(t, db.Create(&models.TrafficDailyLedger{Client: "c1", Day: d.Format(time.DateOnly)}).Error)
	}
	stubLiveFull(t)
	_, summary := tzData(t, db, "UTC", now)
	for _, d := range summary.Daily {
		assert.False(t, d.Partial, d.Day)
	}
}

func TestDashboardTZCalibrationSplitConservesTotal(t *testing.T) {
	db := openTZTestDB(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	seedBuckets(t, db, b0.AddDate(0, 0, -33), b0)
	stubLiveFull(t)
	_, base := tzData(t, db, "America/New_York", now)
	require.NoError(t, db.Create(&models.TrafficCalibrationAdjustment{
		CalibrationID: "cal1", Client: "c1", Cycle: "x", Day: b0.AddDate(0, 0, -5).Format(time.DateOnly), UpDelta: 1001, DownDelta: 0,
	}).Error)
	_, adjusted := tzData(t, db, "America/New_York", now)
	assert.Equal(t, base.PeriodUp+1001, adjusted.PeriodUp)

	// Beijing-aligned zone: lands entirely on the same day.
	_, sg := tzData(t, db, "Asia/Singapore", now)
	key := b0.AddDate(0, 0, -5).Format(time.DateOnly)
	for _, d := range sg.Daily {
		if d.Day == key {
			assert.Equal(t, int64(96+1001), d.Up)
		}
	}
}

func TestDashboardTZTrafficDayParsesDayInViewerZone(t *testing.T) {
	db := openTZTestDB(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	b0 := trafficledger.BeijingDay(now)
	seedBuckets(t, db, b0.AddDate(0, 0, -33), b0)
	stubLiveFull(t)
	tz := parseDashboardTZ("Pacific/Kiritimati")
	config.SetDb(db)

	resp, rerr := adminGetDashboardTrafficDayTZ(context.Background(), db, "2026-07-05", tz, now)
	require.Nil(t, rerr)
	got := resp.(dashboardTrafficDayResponse)
	assert.Equal(t, "2026-07-05", got.Day)
	assert.Equal(t, "Pacific/Kiritimati", got.TZ)
	require.Len(t, got.Items, 1)
	assert.Equal(t, int64(96), got.Items[0].Up)

	// In Kiritimati (UTC+14) it is already the 11th.
	_, rerr = adminGetDashboardTrafficDayTZ(context.Background(), db, "2026-07-11", tz, now)
	require.Nil(t, rerr)
	// Window is 30 calendar days of that zone.
	_, rerr = adminGetDashboardTrafficDayTZ(context.Background(), db, "2026-06-01", tz, now)
	require.NotNil(t, rerr)
	_, rerr = adminGetDashboardTrafficDayTZ(context.Background(), db, "nope", tz, now)
	require.NotNil(t, rerr)
	assert.Equal(t, rpc.InvalidParams, rerr.Code)
}

func TestDashboardTZParamAndCacheIsolation(t *testing.T) {
	assert.True(t, parseDashboardTZ("").legacy())
	assert.True(t, parseDashboardTZ("garbage zone!").legacy())
	assert.Equal(t, "", parseDashboardTZ("").echo())
	assert.Equal(t, "Asia/Shanghai", parseDashboardTZ("bogus!").echo(), "invalid tz falls back and is echoed")
	assert.Equal(t, "America/New_York", parseDashboardTZ("America/New_York").echo())

	ctxNY := withDashboardTZ(context.Background(), parseDashboardTZ("America/New_York"))
	ctxUTC := withDashboardTZ(context.Background(), parseDashboardTZ("UTC"))
	assert.Equal(t, "", dashboardTZCacheSuffix(context.Background()))
	assert.Equal(t, "", dashboardTZCacheSuffix(withDashboardTZ(context.Background(), parseDashboardTZ("Asia/Shanghai"))))
	assert.NotEqual(t, dashboardTZCacheSuffix(ctxNY), dashboardTZCacheSuffix(ctxUTC))

	var cache dashboardModuleCache[int]
	now := time.Now().UTC()
	calls := 0
	load := func() (int, error) { calls++; return calls, nil }
	a, _ := cache.get(context.Background(), now, "5"+dashboardTZCacheSuffix(ctxNY), time.Minute, load)
	b, _ := cache.get(context.Background(), now, "5"+dashboardTZCacheSuffix(ctxUTC), time.Minute, load)
	assert.NotEqual(t, a, b)

	storeDashboardTodayTraffic(dashboardTodayCacheKey(parseDashboardTZ("America/New_York"), "2026-07-10"), []dashboardTrafficRankItem{{UUID: "x"}})
	_, ok := loadCachedDashboardTodayTraffic(dashboardTodayCacheKey(parseDashboardTZ("UTC"), "2026-07-10"), time.Now().UTC())
	assert.False(t, ok, "today snapshot of another zone must not be served")
	_, ok = loadCachedDashboardTodayTraffic(dashboardTodayCacheKey(parseDashboardTZ("America/New_York"), "2026-07-10"), time.Now().UTC())
	assert.True(t, ok)
}

func TestDashboardWithoutTZUsesLegacyPath(t *testing.T) {
	// No tz in the context must select the untouched Beijing implementation.
	assert.True(t, dashboardTZFromContext(context.Background()).legacy())
	assert.True(t, dashboardTZFromRequest(&rpc.JsonRpcRequest{}).legacy())
	assert.Equal(t, "", dashboardTodayCacheKey(parseDashboardTZ(""), "d")[:0])
	assert.Equal(t, "2026-07-10", dashboardTodayCacheKey(parseDashboardTZ(""), "2026-07-10"))
}
