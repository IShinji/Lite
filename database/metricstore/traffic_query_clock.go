package metricstore

// Local patch (not upstream): lets the traffic ledger tests simulate several
// days of hourly maintenance against a real store. Rollup tier selection and
// per-metric retention on the read path depend on "now", so the traffic reads
// take it from this hook instead of calling time.Now directly.

import "time"

var trafficQueryNow = time.Now

// SwapTrafficQueryClockForTest replaces the clock used by the traffic record
// reads. Tests must call the returned restore function.
func SwapTrafficQueryClockForTest(now func() time.Time) func() {
	previous := trafficQueryNow
	trafficQueryNow = now
	return func() { trafficQueryNow = previous }
}
