package trafficledger

// Local patch (not upstream): 15-minute bucket ledger used to aggregate the
// dashboard on an arbitrary viewer time zone. Kept in its own file so that
// rebasing onto upstream only conflicts on the one-line hook in Maintain.
//
// Settlement rules
//   - Only complete Beijing days are written (same boundary as the daily
//     ledger), always as all 96 buckets, so "last bucket of the day exists"
//     is a reliable per-day settled marker.
//   - Days whose source metrics are already gone are NOT fabricated: when the
//     scan has no baseline record before the range, only days after the one
//     containing the first retained record are written.
//   - Buckets come from the same delta state machine as MetricUsagesByDay, so
//     the 96 buckets of a Beijing day add up to its daily ledger row whenever
//     both were settled from the same metric range.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BucketSeconds = int64(15 * 60)
	// BucketsPerBeijingDay is the number of buckets of one 24h Beijing day.
	BucketsPerBeijingDay = int(24 * 60 * 60 / BucketSeconds)
	// BucketLedgerRetentionDays keeps one extra day so a viewer time zone that
	// is a day behind Beijing still has a full 30 day window.
	BucketLedgerRetentionDays = DashboardLedgerRetentionDays + 1
)

// BucketResult is the outcome of scanning metrics for a bucket range.
type BucketResult struct {
	Usage       map[int64]Usage // bucket start (unix seconds) -> usage
	HasBaseline bool            // a record before the range exists
	FirstRecord time.Time       // first record inside the range (zero if none)
}

type bucketCalculator func(ctx context.Context, clientID string, start, end time.Time) (BucketResult, error)

// BucketStart floors t to its UTC-aligned 15 minute bucket (unix seconds).
func BucketStart(t time.Time) int64 {
	unix := t.Unix()
	return unix - ((unix%BucketSeconds)+BucketSeconds)%BucketSeconds
}

func bucketsFromRecords(records []DeltaRecord, previous *DeltaRecord) map[int64]Usage {
	hasPrevious := previous != nil
	var previousUp, previousDown int64
	if previous != nil {
		previousUp, previousDown = previous.NetTotalUp, previous.NetTotalDown
	}
	forced := correlatedCounterDiscontinuities(records, hasPrevious, previousUp, previousDown)
	upDeltas := trafficDeltasByRecord(records, hasPrevious, previousUp,
		func(r DeltaRecord) int64 { return r.NetTotalUp },
		func(r DeltaRecord) int64 { return r.TrafficUp },
		func(r DeltaRecord) bool { return r.TrafficUpSet }, forced)
	downDeltas := trafficDeltasByRecord(records, hasPrevious, previousDown,
		func(r DeltaRecord) int64 { return r.NetTotalDown },
		func(r DeltaRecord) int64 { return r.TrafficDown },
		func(r DeltaRecord) bool { return r.TrafficDownSet }, forced)
	result := make(map[int64]Usage)
	for i, record := range records {
		key := BucketStart(record.Time)
		usage := result[key]
		usage.Up += upDeltas[i]
		usage.Down += downDeltas[i]
		result[key] = usage
	}
	return result
}

// MetricUsageByBucketBatch computes live 15 minute buckets for [start, end]
// for several clients in one metric scan (used for the not yet settled tail).
func MetricUsageByBucketBatch(ctx context.Context, clientIDs []string, start, end time.Time) (map[string]map[int64]Usage, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("traffic metric range end precedes start")
	}
	records, baselines, err := metricstore.GetTrafficRecordsByClientsAndTime(ctx, clientIDs, start, end)
	if err != nil {
		return nil, err
	}
	byClient := make(map[string][]DeltaRecord, len(clientIDs))
	for _, record := range records {
		byClient[record.Client] = append(byClient[record.Client], DeltaRecord{
			Time: record.Time, NetTotalUp: record.NetTotalUp, NetTotalDown: record.NetTotalDown,
			TrafficUp: record.TrafficUp, TrafficDown: record.TrafficDown,
			TrafficUpSet: record.TrafficUpSet, TrafficDownSet: record.TrafficDownSet,
		})
	}
	result := make(map[string]map[int64]Usage, len(clientIDs))
	for _, clientID := range clientIDs {
		if clientID == "" {
			continue
		}
		var previous *DeltaRecord
		if baseline, ok := baselines[clientID]; ok {
			previous = &DeltaRecord{Time: baseline.Time, NetTotalUp: baseline.NetTotalUp, NetTotalDown: baseline.NetTotalDown}
		}
		result[clientID] = bucketsFromRecords(byClient[clientID], previous)
	}
	return result, nil
}

func metricUsageByBucketRange(ctx context.Context, clientID string, start, end time.Time) (BucketResult, error) {
	records, previous, err := metricRecordsAndBaseline(ctx, clientID, start.UTC(), end.UTC().Add(-time.Nanosecond))
	if err != nil {
		return BucketResult{}, err
	}
	result := BucketResult{Usage: bucketsFromRecords(records, previous), HasBaseline: previous != nil}
	if len(records) > 0 {
		result.FirstRecord = records[0].Time
	}
	return result, nil
}

// skipUntil remembers (per process, guarded by ensureMu) the first Beijing day
// that can possibly be settled for a client, so unrecoverable days are not
// rescanned every hour.
var bucketSkipUntil = map[string]time.Time{}

// EnsureBucketRange settles missing Beijing days in [startDay, endDay) into
// the bucket ledger. It is idempotent and shares ensureMu with the daily ledger.
func EnsureBucketRange(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time) error {
	return ensureBucketRange(ctx, db, clientIDs, startDay, endDay, metricUsageByBucketRange)
}

func ensureBucketRange(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time, calculate bucketCalculator) error {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(clientIDs))
	seen := map[string]struct{}{}
	for _, id := range clientIDs {
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)

	ensureMu.Lock()
	defer ensureMu.Unlock()

	var days []time.Time
	sentinels := make([]int64, 0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day)
		sentinels = append(sentinels, day.AddDate(0, 0, 1).Unix()-BucketSeconds)
	}
	var existingRows []models.TrafficBucketLedger
	if err := db.WithContext(ctx).Select("client", "bucket_start").
		Where("client IN ? AND bucket_start IN ?", ids, sentinels).
		Find(&existingRows).Error; err != nil {
		return fmt.Errorf("list settled traffic buckets: %w", err)
	}
	settled := make(map[string]struct{}, len(existingRows))
	for _, row := range existingRows {
		settled[fmt.Sprintf("%s\x00%d", row.Client, row.BucketStart)] = struct{}{}
	}

	for _, clientID := range ids {
		isSettled := func(day time.Time) bool {
			_, ok := settled[fmt.Sprintf("%s\x00%d", clientID, day.AddDate(0, 0, 1).Unix()-BucketSeconds)]
			return ok
		}
		from := start
		if floor, ok := bucketSkipUntil[clientID]; ok && floor.After(from) {
			from = floor
		}
		for from.Before(end) && isSettled(from) {
			from = from.AddDate(0, 0, 1)
		}
		if !from.Before(end) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := calculate(ctx, clientID, from, end)
		if err != nil {
			return fmt.Errorf("settle traffic buckets for client %s: %w", clientID, err)
		}
		available := from
		if !result.HasBaseline {
			if result.FirstRecord.IsZero() {
				bucketSkipUntil[clientID] = end
				continue
			}
			available = BeijingDay(result.FirstRecord).AddDate(0, 0, 1)
			if available.After(from) {
				bucketSkipUntil[clientID] = available
			}
		}
		var rows []models.TrafficBucketLedger
		for day := from; day.Before(end); day = day.AddDate(0, 0, 1) {
			if day.Before(available) || isSettled(day) {
				continue
			}
			for i := 0; i < BucketsPerBeijingDay; i++ {
				key := day.Unix() + int64(i)*BucketSeconds
				usage := result.Usage[key]
				rows = append(rows, models.TrafficBucketLedger{Client: clientID, BucketStart: key, UpBytes: usage.Up, DownBytes: usage.Down})
			}
		}
		if len(rows) == 0 {
			continue
		}
		if err := db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}, {Name: "bucket_start"}},
			DoUpdates: clause.AssignmentColumns([]string{"up_bytes", "down_bytes"}),
		}).CreateInBatches(&rows, 500).Error; err != nil {
			return fmt.Errorf("write traffic buckets for client %s: %w", clientID, err)
		}
	}
	return nil
}

// maintainBucketLedger is the single hook called from Maintain. It is a no-op
// without a metric store so unit tests of the daily ledger are unaffected.
func maintainBucketLedger(ctx context.Context, db *gorm.DB, clientIDs []string, today time.Time) error {
	if metricstore.GetStore() == nil {
		return nil
	}
	return maintainBucketLedgerWith(ctx, db, clientIDs, today, metricUsageByBucketRange)
}

func maintainBucketLedgerWith(ctx context.Context, db *gorm.DB, clientIDs []string, today time.Time, calculate bucketCalculator) error {
	if len(clientIDs) > 0 {
		windowStart := today.AddDate(0, 0, -(BucketLedgerRetentionDays - 1))
		if err := ensureBucketRange(ctx, db, clientIDs, windowStart, today, calculate); err != nil {
			return err
		}
	}
	cutoff := today.AddDate(0, 0, -BucketLedgerRetentionDays).Unix()
	if err := db.WithContext(ctx).Where("bucket_start < ?", cutoff).Delete(&models.TrafficBucketLedger{}).Error; err != nil {
		return fmt.Errorf("clean expired traffic buckets: %w", err)
	}
	if len(clientIDs) == 0 {
		return db.WithContext(ctx).Where("client <> ?", "").Delete(&models.TrafficBucketLedger{}).Error
	}
	return db.WithContext(ctx).Where("client NOT IN ?", clientIDs).Delete(&models.TrafficBucketLedger{}).Error
}

// ScanBucketLedger streams bucket rows with start <= bucket_start < end.
func ScanBucketLedger(ctx context.Context, db *gorm.DB, start, end time.Time, fn func(client string, bucketStart int64, usage Usage)) error {
	rows, err := db.WithContext(ctx).Model(&models.TrafficBucketLedger{}).
		Select("client", "bucket_start", "up_bytes", "down_bytes").
		Where("bucket_start >= ? AND bucket_start < ?", start.Unix(), end.Unix()).
		Rows()
	if err != nil {
		return fmt.Errorf("read traffic bucket ledger: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			client      string
			bucketStart int64
			usage       Usage
		)
		if err := rows.Scan(&client, &bucketStart, &usage.Up, &usage.Down); err != nil {
			return fmt.Errorf("scan traffic bucket ledger: %w", err)
		}
		fn(client, bucketStart, usage)
	}
	return rows.Err()
}
