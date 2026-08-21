package store

import (
	"context"
	"testing"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// pointsByBucket indexes a series by bucket start so assertions can name the
// hour they are checking instead of relying on slice positions.
func pointsByBucket(points []analytics.TimeSeriesPoint) map[time.Time]float64 {
	byBucket := make(map[time.Time]float64, len(points))
	for _, p := range points {
		byBucket[p.Timestamp.UTC()] = p.Value
	}
	return byBucket
}

// TestTimeSeriesMatchesManualRecomputation checks every metric against
// hand-computed values for a known range, which is acceptance criterion #1.
func TestTimeSeriesMatchesManualRecomputation(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	hour0 := fixtureBase
	hour1 := fixtureBase.Add(time.Hour)
	want := fixtureExpectations

	tests := []struct {
		metric analytics.Metric
		hour0  float64
		hour1  float64
		// hour1Absent marks metrics with no activity in the second hour, which
		// must produce no bucket at all rather than a zero.
		hour1Absent bool
	}{
		{metric: analytics.MetricTxCount, hour0: want.TxCountHour0, hour1: want.TxCountHour1},
		{metric: analytics.MetricFeeClassic, hour0: want.FeeClassicHour0, hour1: want.FeeClassicHour1},
		{metric: analytics.MetricFeeSoroban, hour0: want.FeeSorobanHour0, hour1: want.FeeSorobanHour1},
		{metric: analytics.MetricActiveAccounts, hour0: want.ActiveHour0, hour1: want.ActiveHour1},
		{metric: analytics.MetricNewAccounts, hour0: want.NewAccountsHour0, hour1: want.NewAccountsHour1},
		{metric: analytics.MetricTxVolume, hour0: want.VolumeXLMHour0, hour1: want.VolumeXLMHour1},
		{metric: analytics.MetricAssetSupply, hour0: want.SupplyHour0, hour1Absent: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.metric), func(t *testing.T) {
			points, err := store.TimeSeries(context.Background(), tt.metric, analytics.ResolutionHourly, from, to)
			if err != nil {
				t.Fatalf("TimeSeries: %v", err)
			}

			byBucket := pointsByBucket(points)
			if got := byBucket[hour0]; got != tt.hour0 {
				t.Errorf("hour 0 = %v, want %v (series: %+v)", got, tt.hour0, points)
			}

			if tt.hour1Absent {
				if _, ok := byBucket[hour1]; ok {
					t.Errorf("hour 1 should have no bucket, got %v", byBucket[hour1])
				}
				return
			}
			if got := byBucket[hour1]; got != tt.hour1 {
				t.Errorf("hour 1 = %v, want %v (series: %+v)", got, tt.hour1, points)
			}
		})
	}
}

// TestDailyRollupSumsTheHourlyBuckets covers the derived resolutions: an
// additive metric re-bucketed to a day must equal the sum of its hours.
func TestDailyRollupSumsTheHourlyBuckets(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	daily, err := store.TimeSeries(context.Background(), analytics.MetricTxCount, analytics.ResolutionDaily, from, to)
	if err != nil {
		t.Fatalf("TimeSeries daily: %v", err)
	}

	want := fixtureExpectations.TxCountHour0 + fixtureExpectations.TxCountHour1
	got := pointsByBucket(daily)[fixtureBase]
	if got != want {
		t.Errorf("daily tx_count = %v, want %v (series: %+v)", got, want, daily)
	}
}

// TestActiveAccountsDailyIsNotASumOfHours is the reason active_accounts has a
// dedicated aggregate per resolution. Accounts A, A, B transact in the first
// hour and B, C in the second: two distinct accounts per hour, but three across
// the day. A summed rollup would report four.
func TestActiveAccountsDailyIsNotASumOfHours(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	daily, err := store.TimeSeries(context.Background(), analytics.MetricActiveAccounts, analytics.ResolutionDaily, from, to)
	if err != nil {
		t.Fatalf("TimeSeries daily: %v", err)
	}

	got := pointsByBucket(daily)[fixtureBase]
	sumOfHours := fixtureExpectations.ActiveHour0 + fixtureExpectations.ActiveHour1

	if got != fixtureExpectations.ActiveDay {
		t.Errorf("daily active_accounts = %v, want %v", got, fixtureExpectations.ActiveDay)
	}
	if got == sumOfHours {
		t.Errorf("daily active_accounts (%v) equals the sum of hourly buckets — "+
			"the distinct count is being rolled up instead of recomputed", got)
	}
}

// TestWeeklyResolutionIsServedForEveryMetric covers acceptance criterion #2:
// every metric answers at every resolution in a single request.
func TestWeeklyResolutionIsServedForEveryMetric(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	for _, metric := range analytics.AllMetrics {
		for _, resolution := range analytics.AllResolutions {
			if _, err := store.TimeSeries(context.Background(), metric, resolution, from, to); err != nil {
				t.Errorf("TimeSeries(%s, %s): %v", metric, resolution, err)
			}
		}
	}
}

// TestTimeSeriesReturnsEmptySeriesForAQuietRange guards the explorer's
// "not available yet" path: no data is an empty series, never an error.
func TestTimeSeriesReturnsEmptySeriesForAQuietRange(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	quietFrom := time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)
	quietTo := quietFrom.Add(24 * time.Hour)

	for _, metric := range analytics.AllMetrics {
		points, err := store.TimeSeries(context.Background(), metric, analytics.ResolutionHourly, quietFrom, quietTo)
		if err != nil {
			t.Errorf("TimeSeries(%s): %v", metric, err)
			continue
		}
		if len(points) != 0 {
			t.Errorf("TimeSeries(%s) returned %d points for a quiet range", metric, len(points))
		}
	}
}

// TestRefreshSkipsAggregatesWiderThanTheWindow covers the backfill path for
// short ranges. A window narrower than a week contains no complete weekly
// bucket, which TimescaleDB reports as an error rather than a no-op, so the
// refresh must recognise the case and skip that aggregate instead of failing.
func TestRefreshSkipsAggregatesWiderThanTheWindow(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from := fixtureBase.Add(-time.Hour)
	to := fixtureBase.Add(48 * time.Hour)

	results, err := store.RefreshAnalyticsAggregates(context.Background(), from, to)
	if err != nil {
		t.Fatalf("RefreshAnalyticsAggregates: %v", err)
	}
	if len(results) != len(analyticsAggregates) {
		t.Fatalf("got %d results, want one per aggregate (%d)", len(results), len(analyticsAggregates))
	}

	skipped := make(map[string]bool)
	for _, r := range results {
		skipped[r.Aggregate] = r.Skipped
	}

	if !skipped["analytics_active_accounts_weekly"] {
		t.Error("a two-day window holds no complete weekly bucket, so the weekly aggregate should be skipped")
	}
	if skipped["analytics_tx_hourly"] {
		t.Error("hourly aggregates fit comfortably in a two-day window and must not be skipped")
	}
}

// TestRefreshWithUnboundedWindowSucceeds is the default backfill invocation:
// no bounds at all, letting TimescaleDB refresh the full extent of the data.
func TestRefreshWithUnboundedWindowSucceeds(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	if _, err := store.RefreshAnalyticsAggregates(context.Background(), time.Time{}, time.Time{}); err != nil {
		t.Fatalf("unbounded refresh: %v", err)
	}
}
