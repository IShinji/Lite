package trafficledger

// Local patch (not upstream): 15-minute bucket ledger used to aggregate the
// dashboard on an arbitrary viewer time zone. Kept in its own file so that
// rebasing onto upstream only conflicts on the one-line hook in Maintain.
//
// Settlement rules
//   - Every hourly run upserts today's (Beijing) buckets that already ended.
//     With the default 1 day metric retention this is the only moment the
//     start of a day can still be read with a baseline.
//   - A Beijing day is settled once it ended, always as all 96 buckets, so
//     "last bucket of the day exists" is a reliable per-day settled marker;
//     the incremental writes never touch that last bucket.
//   - Settlement without a baseline only inserts missing buckets up to the
//     first retained record (it never overwrites incremental rows with a
//     truncated value) and upserts the rest.
//   - Days whose source metrics are already gone are NOT fabricated: a day
//     without baseline and without any incremental row is skipped.
//   - Metrics are read in pieces of at most 6 hours so the downsampled read
//     keeps a resolution that divides 15 minutes.
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

// bucketChunk bounds one metric read. GetTrafficRecordsByClientAndTime
// downsamples to 500 points, so a 30 day read comes back hourly and would
// stack four 15 minute buckets into one. Six hours stay at 1 minute (or the
// 5 minute rollup for older data), both of which divide 15 minutes.
const bucketChunk = 6 * time.Hour

// bucketSkipUntil remembers (per process, guarded by ensureMu) the first
// Beijing day that can possibly be settled for a client, so unrecoverable days
// are not rescanned every hour. It only moves forward on evidence (a retained
// record without an older baseline), never because a read came back empty.
var bucketSkipUntil = map[string]time.Time{}

// EnsureBucketRange settles missing Beijing days in [startDay, endDay) into
// the bucket ledger. It is idempotent and shares ensureMu with the daily ledger.
func EnsureBucketRange(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time) error {
	return ensureBucketRange(ctx, db, clientIDs, startDay, endDay, metricUsageByBucketRange)
}

func uniqueClientIDs(clientIDs []string) []string {
	ids := make([]string, 0, len(clientIDs))
	seen := map[string]struct{}{}
	for _, id := range clientIDs {
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// calculateBuckets runs calculate over [from, to) in pieces of at most
// bucketChunk and merges them. HasBaseline describes from itself.
func calculateBuckets(ctx context.Context, calculate bucketCalculator, clientID string, from, to time.Time) (BucketResult, error) {
	merged := BucketResult{Usage: make(map[int64]Usage)}
	for pieceStart := from; pieceStart.Before(to); {
		pieceEnd := pieceStart.Add(bucketChunk)
		if pieceEnd.After(to) {
			pieceEnd = to
		}
		result, err := calculate(ctx, clientID, pieceStart, pieceEnd)
		if err != nil {
			return BucketResult{}, err
		}
		if pieceStart.Equal(from) {
			merged.HasBaseline = result.HasBaseline
		}
		if merged.FirstRecord.IsZero() && !result.FirstRecord.IsZero() {
			merged.FirstRecord = result.FirstRecord
		}
		for key, usage := range result.Usage {
			if key < pieceStart.Unix() || key >= pieceEnd.Unix() {
				continue
			}
			current := merged.Usage[key]
			current.Up += usage.Up
			current.Down += usage.Down
			merged.Usage[key] = current
		}
		pieceStart = pieceEnd
	}
	return merged, nil
}

// writeBuckets stores every bucket in [from, to). Without a baseline the
// buckets up to and including the first retained record's may be missing
// traffic that already aged out of the metric store, so they are only
// inserted when absent: a value written earlier (incrementally, while the
// source was complete) is never overwritten by a truncated recomputation.
// Later buckets are recomputed with a baseline and always upserted.
func writeBuckets(ctx context.Context, db *gorm.DB, clientID string, from, to time.Time, result BucketResult) error {
	uncertainUntil := from.Unix() - BucketSeconds
	if !result.HasBaseline {
		uncertainUntil = to.Unix()
		if !result.FirstRecord.IsZero() {
			uncertainUntil = BucketStart(result.FirstRecord)
		}
	}
	var keep, replace []models.TrafficBucketLedger
	for key := from.Unix(); key < to.Unix(); key += BucketSeconds {
		usage := result.Usage[key]
		row := models.TrafficBucketLedger{Client: clientID, BucketStart: key, UpBytes: usage.Up, DownBytes: usage.Down}
		if key <= uncertainUntil {
			keep = append(keep, row)
		} else {
			replace = append(replace, row)
		}
	}
	columns := []clause.Column{{Name: "client"}, {Name: "bucket_start"}}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(keep) > 0 {
			if err := tx.Clauses(clause.OnConflict{Columns: columns, DoNothing: true}).
				CreateInBatches(&keep, 500).Error; err != nil {
				return err
			}
		}
		if len(replace) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   columns,
				DoUpdates: clause.AssignmentColumns([]string{"up_bytes", "down_bytes"}),
			}).CreateInBatches(&replace, 500).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func hasBucketRows(ctx context.Context, db *gorm.DB, clientID string, from, to time.Time) (bool, error) {
	var rows []models.TrafficBucketLedger
	if err := db.WithContext(ctx).Select("bucket_start").
		Where("client = ? AND bucket_start >= ? AND bucket_start < ?", clientID, from.Unix(), to.Unix()).
		Limit(1).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("read traffic buckets for client %s: %w", clientID, err)
	}
	return len(rows) > 0, nil
}

func ensureBucketRange(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time, calculate bucketCalculator) error {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return err
	}
	ids := uniqueClientIDs(clientIDs)
	if len(ids) == 0 {
		return nil
	}

	ensureMu.Lock()
	defer ensureMu.Unlock()

	sentinels := make([]int64, 0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
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
		from := start
		if floor, ok := bucketSkipUntil[clientID]; ok && floor.After(from) {
			from = floor
		}
		var pending []time.Time
		for day := from; day.Before(end); day = day.AddDate(0, 0, 1) {
			if _, ok := settled[fmt.Sprintf("%s\x00%d", clientID, day.AddDate(0, 0, 1).Unix()-BucketSeconds)]; !ok {
				pending = append(pending, day)
			}
		}
		if len(pending) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// One coarse read finds where retained data starts, so a backlog of
		// days without any source is not read day by day every hour. An empty
		// read only means "nothing to compute now"; it never moves the floor.
		dataFrom := pending[0]
		floor := time.Time{}
		if len(pending) > 1 {
			probe, err := calculate(ctx, clientID, pending[0], end)
			if err != nil {
				return fmt.Errorf("settle traffic buckets for client %s: %w", clientID, err)
			}
			if !probe.HasBaseline {
				dataFrom = end
				if !probe.FirstRecord.IsZero() {
					dataFrom = BeijingDay(probe.FirstRecord)
					floor = dataFrom
				}
			}
		}
		for _, day := range pending {
			next := day.AddDate(0, 0, 1)
			result := BucketResult{}
			if !day.Before(dataFrom) {
				result, err = calculateBuckets(ctx, calculate, clientID, day, next)
				if err != nil {
					return fmt.Errorf("settle traffic buckets for client %s: %w", clientID, err)
				}
			}
			if !result.HasBaseline {
				// The start of this day aged out of the metric store. Settle it
				// only if buckets were written while the source was complete.
				incremental, err := hasBucketRows(ctx, db, clientID, day, next)
				if err != nil {
					return err
				}
				if !incremental {
					if !result.FirstRecord.IsZero() && next.After(floor) {
						floor = next
					}
					continue
				}
			}
			if err := writeBuckets(ctx, db, clientID, day, next, result); err != nil {
				return fmt.Errorf("write traffic buckets for client %s: %w", clientID, err)
			}
			if !result.HasBaseline && !result.FirstRecord.IsZero() && day.After(floor) {
				floor = day
			}
		}
		if current, ok := bucketSkipUntil[clientID]; !floor.IsZero() && (!ok || floor.After(current)) {
			bucketSkipUntil[clientID] = floor
		}
	}
	return nil
}

// ensureTodayBuckets upserts today's (Beijing) buckets that already ended.
// Today's start is less than a day old, so the baseline before it is still
// retained even with the default 1 day metric retention; these rows are what
// keeps the start of the day once it has aged out by settlement time. The
// last bucket of the day is the settled marker and is never written here.
func ensureTodayBuckets(ctx context.Context, db *gorm.DB, clientIDs []string, now time.Time, calculate bucketCalculator) error {
	today := BeijingDay(now)
	cut := time.Unix(BucketStart(now), 0)
	if marker := today.AddDate(0, 0, 1).Add(-time.Duration(BucketSeconds) * time.Second); cut.After(marker) {
		cut = marker
	}
	if !cut.After(today) {
		return nil
	}
	ids := uniqueClientIDs(clientIDs)
	ensureMu.Lock()
	defer ensureMu.Unlock()
	for _, clientID := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := calculateBuckets(ctx, calculate, clientID, today, cut)
		if err != nil {
			return fmt.Errorf("update today's traffic buckets for client %s: %w", clientID, err)
		}
		if !result.HasBaseline && result.FirstRecord.IsZero() {
			continue
		}
		if err := writeBuckets(ctx, db, clientID, today, cut, result); err != nil {
			return fmt.Errorf("write today's traffic buckets for client %s: %w", clientID, err)
		}
	}
	return nil
}

// maintainBucketLedger is the single hook called from Maintain. It is a no-op
// without a metric store so unit tests of the daily ledger are unaffected.
func maintainBucketLedger(ctx context.Context, db *gorm.DB, clientIDs []string, now time.Time) error {
	if metricstore.GetStore() == nil {
		return nil
	}
	return maintainBucketLedgerWith(ctx, db, clientIDs, now, metricUsageByBucketRange)
}

func maintainBucketLedgerWith(ctx context.Context, db *gorm.DB, clientIDs []string, now time.Time, calculate bucketCalculator) error {
	today := BeijingDay(now)
	if len(clientIDs) > 0 {
		windowStart := today.AddDate(0, 0, -(BucketLedgerRetentionDays - 1))
		if err := ensureBucketRange(ctx, db, clientIDs, windowStart, today, calculate); err != nil {
			return err
		}
		if err := ensureTodayBuckets(ctx, db, clientIDs, now, calculate); err != nil {
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
