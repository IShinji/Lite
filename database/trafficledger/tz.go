package trafficledger

// Local patch (not upstream): viewer time zone helpers for the dashboard.

import (
	"math/big"
	"strings"
	"time"
)

// DefaultTZName is the time zone used when the request has no (valid) tz.
const DefaultTZName = "Asia/Shanghai"

const maxTZNameLen = 64

// ParseTZ resolves an IANA zone name. Empty, too long, non-IANA-looking or
// unknown names fall back to Asia/Shanghai. The returned bool reports whether
// the input was accepted as given (false means fallback was used).
func ParseTZ(raw string) (*time.Location, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxTZNameLen || strings.EqualFold(raw, "local") || strings.Contains(raw, "..") {
		return BeijingLocation, DefaultTZName, false
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '_' || r == '-' || r == '+':
		default:
			return BeijingLocation, DefaultTZName, false
		}
	}
	if raw == DefaultTZName {
		return BeijingLocation, DefaultTZName, true
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return BeijingLocation, DefaultTZName, false
	}
	return loc, raw, true
}

// TZWindow returns the starts of the last n calendar days of loc ending with
// the day containing now, plus the start of the following day (len n+1), and
// the civil date keys (len n).
func TZWindow(loc *time.Location, now time.Time, n int) ([]time.Time, []string) {
	y, m, d := now.In(loc).Date()
	starts := make([]time.Time, 0, n+1)
	keys := make([]string, 0, n)
	for i := 0; i <= n; i++ {
		dd := d - (n - 1) + i
		starts = append(starts, time.Date(y, m, dd, 0, 0, 0, 0, loc))
		if i < n {
			keys = append(keys, time.Date(y, m, dd, 12, 0, 0, 0, time.UTC).Format(time.DateOnly))
		}
	}
	return starts, keys
}

// DayAlignedWithBeijing reports whether [start, next) is exactly one Beijing
// calendar day, in which case the daily ledger already holds its exact value.
func DayAlignedWithBeijing(start, next time.Time) bool {
	return start.Equal(BeijingDay(start)) && next.Sub(start) == 24*time.Hour
}

// SplitAdjustment distributes a calibration adjustment stored for one Beijing
// day over the viewer-zone days in starts (len n+1). Calibration is only known
// per Beijing day, so the split is proportional to the time overlap (exactly
// 1:1 when the zone shares Beijing's day boundary). Overlap outside the window
// is clamped into the first/last day. The pieces always sum to the input.
func SplitAdjustment(beijingDay time.Time, adj SignedUsage, starts []time.Time) map[int]SignedUsage {
	n := len(starts) - 1
	if n < 1 || (adj.Up == 0 && adj.Down == 0) {
		return nil
	}
	b0 := beijingDay.Unix()
	b1 := beijingDay.AddDate(0, 0, 1).Unix()
	type piece struct {
		idx     int
		overlap int64
	}
	var pieces []piece
	for i := 0; i < n; i++ {
		lo, hi := starts[i].Unix(), starts[i+1].Unix()
		if i == 0 {
			lo = b0 - 1
			if lo > starts[0].Unix() {
				lo = starts[0].Unix()
			}
		}
		if i == n-1 {
			hi = b1 + 1
			if hi < starts[n].Unix() {
				hi = starts[n].Unix()
			}
		}
		if lo < b0 {
			lo = b0
		}
		if hi > b1 {
			hi = b1
		}
		if hi > lo {
			pieces = append(pieces, piece{i, hi - lo})
		}
	}
	total := b1 - b0
	result := make(map[int]SignedUsage, len(pieces))
	var usedUp, usedDown int64
	for k, p := range pieces {
		part := SignedUsage{}
		if k == len(pieces)-1 {
			part = SignedUsage{Up: adj.Up - usedUp, Down: adj.Down - usedDown}
		} else {
			part = SignedUsage{Up: proportion(adj.Up, p.overlap, total), Down: proportion(adj.Down, p.overlap, total)}
			usedUp += part.Up
			usedDown += part.Down
		}
		result[p.idx] = part
	}
	return result
}

func proportion(value, num, den int64) int64 {
	r := new(big.Int).Mul(big.NewInt(value), big.NewInt(num))
	return r.Quo(r, big.NewInt(den)).Int64()
}
