package jsonrpc

// Local patch (not upstream): dashboard traffic aggregated on the viewer's
// time zone. Entry points are tiny hooks in admin.dashboard*.go; everything
// else lives here so upstream rebases only conflict on those hooks.
//
// Data sources (all UTC, 15 minute buckets):
//   - settled Beijing days  -> traffic_bucket_ledgers
//   - the unsettled tail    -> live metric scan from today's Beijing midnight
//
// Partial strategy: a zone day is "complete" for a client only when the bucket
// ledger holds every bucket of it that lies before today's Beijing midnight
// (the live tail is always complete). When it is not complete:
//   - zone day == one Beijing day (e.g. Asia/Singapore): use the Beijing daily
//     ledger value, exact, not partial;
//   - otherwise we never splice the daily ledger into bucket data. The day
//     keeps the buckets we have and is flagged partial=true if the missing
//     range could have held traffic (an overlapping Beijing daily ledger row is
//     non-zero or missing); zero-traffic gaps (e.g. a client that did not
//     exist yet) do not flag.
//
// Calibration (traffic_calibration_adjustments) is stored per Beijing day. It
// is split across the zone days it overlaps proportionally to time overlap
// (1:1 for Beijing-aligned zones), conserving the total; see
// trafficledger.SplitAdjustment. Today's hourly curve applies the zone-today
// share with the existing ApplyHourlyAdjustment rule.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/rpc"
	publicweb "github.com/nuomiiiii/lite/web/public"
	"gorm.io/gorm"
)

// dashboardLiveBuckets is swappable in tests.
var dashboardLiveBuckets = trafficledger.MetricUsageByBucketBatch

type dashboardTZ struct {
	loc      *time.Location
	name     string
	explicit bool // the request carried a tz value (echoed back)
}

// legacy reports the Beijing path: behavior identical to before the patch.
func (t dashboardTZ) legacy() bool { return t.name == trafficledger.DefaultTZName }

func parseDashboardTZ(raw string) dashboardTZ {
	loc, name, _ := trafficledger.ParseTZ(raw)
	return dashboardTZ{loc: loc, name: name, explicit: strings.TrimSpace(raw) != ""}
}

func dashboardTZFromRequest(req *rpc.JsonRpcRequest) dashboardTZ {
	raw, _ := rpc.GetParamAs[string](req, "tz")
	return parseDashboardTZ(raw)
}

func (t dashboardTZ) echo() string {
	if t.explicit {
		return t.name
	}
	return ""
}

type dashboardTZContextKey struct{}

func withDashboardTZ(ctx context.Context, tz dashboardTZ) context.Context {
	return context.WithValue(ctx, dashboardTZContextKey{}, tz)
}

func dashboardTZFromContext(ctx context.Context) dashboardTZ {
	if tz, ok := ctx.Value(dashboardTZContextKey{}).(dashboardTZ); ok && tz.loc != nil {
		return tz
	}
	return parseDashboardTZ("")
}

// dashboardTZCacheSuffix keeps module-cache entries of different zones apart.
// The Beijing/default key stays unchanged.
func dashboardTZCacheSuffix(ctx context.Context) string {
	tz := dashboardTZFromContext(ctx)
	if tz.legacy() {
		return ""
	}
	return "|tz=" + tz.name
}

func dashboardTodayCacheKey(tz dashboardTZ, day string) string {
	if tz.legacy() {
		return day
	}
	return tz.name + "|" + day
}

// dashboardTZTodayPartial remembers the partial flag next to the shared
// today-items snapshot (keyed like it, including the zone).
var dashboardTZTodayPartial sync.Map

type tzTrafficData struct {
	tz        dashboardTZ
	now       time.Time
	starts    []time.Time
	keys      []string
	usage     map[string][]trafficledger.Usage // client -> per zone day
	partial   []bool
	hourly    map[string][]trafficledger.HourlyUsage
	adjust    map[string]trafficledger.SignedUsage // client\x00zoneDay
	ledgerOK  bool                                 // Beijing daily ledger complete for the window
	loIdx     int
	hiIdx     int
	dayWindow int
}

func floorIndex(starts []time.Time, unix int64) int {
	n := len(starts) - 1
	if unix < starts[0].Unix() || unix >= starts[n].Unix() {
		return -1
	}
	return sort.Search(n, func(i int) bool { return starts[i+1].Unix() > unix })
}

// loadDashboardTZData aggregates zone days loIdx..hiIdx (window of 30 days).
func loadDashboardTZData(ctx context.Context, db *gorm.DB, clientList []models.Client, now time.Time, tz dashboardTZ, loIdx, hiIdx int) (*tzTrafficData, error) {
	n := trafficledger.DashboardHistoryDays
	starts, keys := trafficledger.TZWindow(tz.loc, now, n)
	if loIdx < 0 {
		loIdx = 0
	}
	if hiIdx > n-1 {
		hiIdx = n - 1
	}
	b0 := trafficledger.BeijingDay(now)
	ids := make([]string, 0, len(clientList))
	known := make(map[string]struct{}, len(clientList))
	for _, c := range clientList {
		if c.UUID != "" {
			ids = append(ids, c.UUID)
			known[c.UUID] = struct{}{}
		}
	}

	// Settle the most recent Beijing days on demand so the gap between the
	// hourly maintenance runs does not show up as missing buckets.
	if metricstore.GetStore() != nil {
		_ = trafficledger.EnsureBucketRange(ctx, db, ids, b0.AddDate(0, 0, -2), b0)
	}

	data := &tzTrafficData{
		tz: tz, now: now, starts: starts, keys: keys,
		usage:   make(map[string][]trafficledger.Usage, len(ids)),
		partial: make([]bool, n),
		hourly:  make(map[string][]trafficledger.HourlyUsage),
		adjust:  make(map[string]trafficledger.SignedUsage),
		loIdx:   loIdx, hiIdx: hiIdx, dayWindow: n,
	}
	counts := make(map[string][]int32, len(ids))
	hourSlots := make(map[string]map[int]*trafficledger.HourlyUsage)
	todayStart := starts[n-1].Unix()
	add := func(client string, bs int64, u trafficledger.Usage, ledger bool) {
		if _, ok := known[client]; !ok {
			return
		}
		idx := floorIndex(starts, bs)
		if idx < loIdx || idx > hiIdx {
			return
		}
		days := data.usage[client]
		if days == nil {
			days = make([]trafficledger.Usage, n)
			data.usage[client] = days
			counts[client] = make([]int32, n)
		}
		days[idx].Up += u.Up
		days[idx].Down += u.Down
		if ledger {
			counts[client][idx]++
		}
		if bs >= todayStart && idx == n-1 {
			bt := time.Unix(bs, 0).UTC()
			slot := bt.In(tz.loc).Hour()
			slots := hourSlots[client]
			if slots == nil {
				slots = make(map[int]*trafficledger.HourlyUsage)
				hourSlots[client] = slots
			}
			h := slots[slot]
			if h == nil {
				h = &trafficledger.HourlyUsage{Hour: bt}
				slots[slot] = h
			}
			h.Up += u.Up
			h.Down += u.Down
		}
	}

	ledgerHi := starts[hiIdx+1]
	if ledgerHi.After(b0) {
		ledgerHi = b0
	}
	if ledgerHi.After(starts[loIdx]) {
		if err := trafficledger.ScanBucketLedger(ctx, db, starts[loIdx], ledgerHi, func(client string, bs int64, u trafficledger.Usage) {
			add(client, bs, u, true)
		}); err != nil {
			return nil, err
		}
	}
	if starts[hiIdx+1].After(b0) && len(ids) > 0 {
		live, err := dashboardLiveBuckets(ctx, ids, b0.UTC(), now.UTC())
		if err != nil {
			return nil, fmt.Errorf("read live dashboard traffic: %w", err)
		}
		for client, buckets := range live {
			for bs, u := range buckets {
				add(client, bs, u, false)
			}
		}
	}
	for client, slots := range hourSlots {
		list := make([]trafficledger.HourlyUsage, 0, len(slots))
		for _, h := range slots {
			list = append(list, *h)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Hour.Before(list[j].Hour) })
		data.hourly[client] = list
	}

	// Beijing daily ledger: used for exact aligned days, the partial test and
	// the history-ready flag.
	firstBeijing := trafficledger.BeijingDay(starts[loIdx])
	var ledgerRows []models.TrafficDailyLedger
	if firstBeijing.Before(b0) {
		if err := db.WithContext(ctx).
			Select("client", "day", "up_bytes", "down_bytes").
			Where("day >= ? AND day < ?", firstBeijing.Format(time.DateOnly), b0.Format(time.DateOnly)).
			Find(&ledgerRows).Error; err != nil {
			return nil, fmt.Errorf("read dashboard traffic ledger: %w", err)
		}
	}
	ledger := make(map[string]trafficledger.Usage, len(ledgerRows))
	seenRows := 0
	for _, row := range ledgerRows {
		if _, ok := known[row.Client]; !ok {
			continue
		}
		ledger[row.Client+"\x00"+row.Day] = trafficledger.Usage{Up: row.UpBytes, Down: row.DownBytes}
		seenRows++
	}
	beijingDays := 0
	for d := firstBeijing; d.Before(b0); d = d.AddDate(0, 0, 1) {
		beijingDays++
	}
	data.ledgerOK = seenRows == len(ids)*beijingDays

	for i := loIdx; i <= hiIdx; i++ {
		covered := starts[i+1]
		if covered.After(b0) {
			covered = b0
		}
		expected := int32(0)
		if covered.After(starts[i]) {
			expected = int32(covered.Sub(starts[i]) / (time.Duration(trafficledger.BucketSeconds) * time.Second))
		}
		aligned := trafficledger.DayAlignedWithBeijing(starts[i], starts[i+1])
		for _, id := range ids {
			var have int32
			if c := counts[id]; c != nil {
				have = c[i]
			}
			if have >= expected {
				continue
			}
			if aligned {
				if row, ok := ledger[id+"\x00"+starts[i].In(trafficledger.BeijingLocation).Format(time.DateOnly)]; ok {
					if data.usage[id] == nil {
						data.usage[id] = make([]trafficledger.Usage, n)
					}
					data.usage[id][i] = row
					continue
				}
				data.partial[i] = true
				continue
			}
			for bd := trafficledger.BeijingDay(starts[i]); bd.Before(covered); bd = bd.AddDate(0, 0, 1) {
				row, ok := ledger[id+"\x00"+bd.Format(time.DateOnly)]
				if !ok || row.Up != 0 || row.Down != 0 {
					data.partial[i] = true
					break
				}
			}
		}
	}

	// Calibration split from Beijing days onto zone days.
	adjStart := trafficledger.BeijingDay(starts[0])
	adjEnd := trafficledger.BeijingDay(starts[n].Add(-time.Second)).AddDate(0, 0, 1)
	adjustments, err := trafficledger.DailyAdjustments(ctx, db, adjStart, adjEnd)
	if err != nil {
		return nil, fmt.Errorf("read dashboard traffic calibration: %w", err)
	}
	for key, adj := range adjustments {
		sep := strings.IndexByte(key, 0)
		if sep < 0 {
			continue
		}
		bd, err := time.ParseInLocation(time.DateOnly, key[sep+1:], trafficledger.BeijingLocation)
		if err != nil {
			continue
		}
		for idx, piece := range trafficledger.SplitAdjustment(bd, adj, starts) {
			k := key[:sep] + "\x00" + keys[idx]
			cur := data.adjust[k]
			cur.Up += piece.Up
			cur.Down += piece.Down
			data.adjust[k] = cur
		}
	}
	return data, nil
}

func loadDashboardTrafficTZ(ctx context.Context, clientList []models.Client, now time.Time, rankingLimit int, tz dashboardTZ) (dashboardTrafficSummary, error) {
	data, err := loadDashboardTZData(ctx, dbcore.GetDBInstance(), clientList, now, tz, 0, trafficledger.DashboardHistoryDays-1)
	if err != nil {
		return dashboardTrafficSummary{}, err
	}
	return summarizeDashboardTrafficTZ(clientList, data, rankingLimit), nil
}

func summarizeDashboardTrafficTZ(clientList []models.Client, data *tzTrafficData, rankingLimit int) dashboardTrafficSummary {
	n := data.dayWindow
	todayKey := data.keys[n-1]
	summary := dashboardTrafficSummary{
		Daily:  make([]dashboardTrafficDay, n),
		Hourly: make([]dashboardTrafficHour, data.now.In(data.tz.loc).Hour()+1),
	}
	for hour := range summary.Hourly {
		summary.Hourly[hour].Hour = fmt.Sprintf("%02d:00", hour)
	}
	for i := range summary.Daily {
		summary.Daily[i] = dashboardTrafficDay{Day: data.keys[i], Partial: data.partial[i]}
	}
	todayItems := make([]dashboardTrafficRankItem, 0, len(clientList))
	for _, client := range clientList {
		days := data.usage[client.UUID]
		for i := 0; i < n; i++ {
			var raw trafficledger.Usage
			if days != nil {
				raw = days[i]
			}
			usage := trafficledger.ApplyAdjustment(raw, data.adjust[client.UUID+"\x00"+data.keys[i]])
			summary.Daily[i].Up += usage.Up
			summary.Daily[i].Down += usage.Down
			billable := int64(0)
			if client.Price > 0 {
				billable = trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
				summary.Daily[i].Billable += billable
			}
			if i != n-1 {
				continue
			}
			summary.TodayUp += usage.Up
			summary.TodayDown += usage.Down
			summary.TodayBillable += billable
			rankingBillable := trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
			item := dashboardTrafficRankBase(client)
			item.Up, item.Down, item.Billable = usage.Up, usage.Down, rankingBillable
			todayItems = append(todayItems, item)
			if rankingBillable > 0 {
				summary.Ranking = dashboardTopTraffic(summary.Ranking, item, rankingLimit)
			}
		}
		adjToday := data.adjust[client.UUID+"\x00"+todayKey]
		for _, hourly := range trafficledger.ApplyHourlyAdjustment(data.hourly[client.UUID], adjToday, data.now) {
			hour := hourly.Hour.In(data.tz.loc).Hour()
			if hour >= 0 && hour < len(summary.Hourly) {
				summary.Hourly[hour].Up += hourly.Up
				summary.Hourly[hour].Down += hourly.Down
			}
		}
	}
	for hour := 1; hour < len(summary.Hourly); hour++ {
		summary.Hourly[hour].Up += summary.Hourly[hour-1].Up
		summary.Hourly[hour].Down += summary.Hourly[hour-1].Down
	}
	anyPartial := false
	lastPartial := -1
	for i, p := range data.partial {
		if p {
			anyPartial = true
			lastPartial = i
		}
	}
	complete := !anyPartial
	summary.HistoryComplete = &complete
	if anyPartial && lastPartial < n-1 {
		since := data.keys[lastPartial+1]
		summary.LedgerSince = &since
	}
	summary.HistoryReady = data.ledgerOK && complete
	fillDashboardTrafficPeriod(&summary)
	dashboardTZTodayPartial.Store(dashboardTodayCacheKey(data.tz, todayKey), data.partial[n-1])
	storeDashboardTodayTraffic(dashboardTodayCacheKey(data.tz, todayKey), todayItems)
	return summary
}

// adminGetDashboardTrafficDayTZ serves getDashboardTrafficDay for a non-Beijing zone.
func adminGetDashboardTrafficDayTZ(ctx context.Context, db *gorm.DB, rawDay string, tz dashboardTZ, now time.Time) (any, *rpc.JsonRpcError) {
	rawDay = strings.TrimSpace(rawDay)
	if rawDay == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "day is required", nil)
	}
	if _, err := time.Parse(time.DateOnly, rawDay); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "invalid day", nil)
	}
	n := trafficledger.DashboardHistoryDays
	_, keys := trafficledger.TZWindow(tz.loc, now, n)
	idx := -1
	for i, key := range keys {
		if key == rawDay {
			idx = i
		}
	}
	if idx < 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "day is outside the dashboard window", nil)
	}
	clientList, err := listDashboardTrafficClientsFrom(ctx, db)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, fmt.Sprintf("list dashboard traffic clients: %v", err), nil)
	}
	var items []dashboardTrafficRankItem
	partial := false
	cacheKey := dashboardTodayCacheKey(tz, rawDay)
	cached := false
	if idx == n-1 {
		items, cached = loadCachedDashboardTodayTraffic(cacheKey, now)
		if cached {
			if v, ok := dashboardTZTodayPartial.Load(cacheKey); ok {
				partial, _ = v.(bool)
			}
		}
	}
	if !cached {
		data, err := loadDashboardTZData(ctx, db, clientList, now, tz, idx, idx)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
		}
		usage := make(map[string]trafficledger.Usage, len(clientList))
		for client, days := range data.usage {
			usage[client] = days[idx]
		}
		items = dashboardTrafficDayItems(clientList, usage, data.adjust, rawDay)
		partial = data.partial[idx]
	}
	navigation := publicweb.ActiveThemeNavigation()
	for index := range items {
		items[index].DetailURL = navigation.ServerDetailURL(items[index].UUID, 0)
	}
	return dashboardTrafficDayResponse{
		Day: rawDay, Items: items, GeneratedAt: now, TZ: tz.name, Partial: partial,
	}, nil
}

func addDashboardTZFields(result map[string]any, tz dashboardTZ, summary dashboardTrafficSummary) {
	if name := tz.echo(); name != "" {
		result["tz"] = name
	}
	if summary.HistoryComplete != nil {
		result["history_complete"] = *summary.HistoryComplete
	}
	if summary.LedgerSince != nil {
		result["ledger_since"] = *summary.LedgerSince
	}
}

func listDashboardTrafficClientsFrom(ctx context.Context, db *gorm.DB) ([]models.Client, error) {
	var list []models.Client
	err := db.WithContext(ctx).
		Select("uuid", "name", "price", "traffic_limit_type", "ipv4", "ipv6", "group", "tags").
		Order("name ASC").Order("uuid ASC").
		Find(&list).Error
	return list, err
}
