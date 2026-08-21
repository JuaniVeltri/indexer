package store

import (
	"context"
	"fmt"
	"time"
)

// analyticsAggregate is one continuous aggregate backing the analytics API,
// paired with the bucket width it was defined with.
type analyticsAggregate struct {
	name string
	// bucket is the PostgreSQL interval literal used by the aggregate's
	// time_bucket call, and the unit a refresh window must be snapped to.
	bucket string
}

// analyticsAggregates lists every aggregate to refresh. Keep it in sync with
// migrations/000014_analytics_aggregates.up.sql.
var analyticsAggregates = []analyticsAggregate{
	{"analytics_tx_hourly", "1 hour"},
	{"analytics_volume_hourly", "1 hour"},
	{"analytics_new_accounts_hourly", "1 hour"},
	{"analytics_active_accounts_hourly", "1 hour"},
	{"analytics_active_accounts_daily", "1 day"},
	{"analytics_active_accounts_weekly", "1 week"},
	{"analytics_asset_supply_hourly", "1 hour"},
	{"analytics_asset_transfers_hourly", "1 hour"},
	{"analytics_contract_activity_hourly", "1 hour"},
}

// AnalyticsAggregateCount reports how many aggregates a full refresh covers, so
// callers can describe partial progress against the total.
func AnalyticsAggregateCount() int {
	return len(analyticsAggregates)
}

// AnalyticsRefreshResult reports the outcome of refreshing one aggregate.
type AnalyticsRefreshResult struct {
	Aggregate string
	// Skipped is set when the requested window contains no complete bucket for
	// this aggregate — asking for a week's aggregate over a two-day window, for
	// instance.
	Skipped  bool
	Duration time.Duration
}

// RefreshAnalyticsAggregates materializes every analytics aggregate over
// [from, to]. A zero start reaches as far back as the data goes; a zero end is
// resolved to the last completed bucket rather than left open, so the bucket
// currently being written is never frozen at a partial value.
//
// TimescaleDB refreshes only buckets that fit entirely inside the window, and
// raises an error rather than doing nothing when none do. Each window is
// therefore snapped to the aggregate's own bucket boundaries first, and
// aggregates left with nothing to do are reported as skipped instead of failing
// the run. Snapping also keeps an in-progress bucket out of the materialization,
// which would otherwise move the watermark past data still being written.
//
// The refresh runs its batches in separate transactions, so it must not be
// wrapped in one here. That also makes it resumable: re-running skips buckets
// that are already materialized.
// https://www.tigerdata.com/docs/api/latest/continuous-aggregates/refresh_continuous_aggregate
func (s *PostgresStore) RefreshAnalyticsAggregates(ctx context.Context, from, to time.Time) ([]AnalyticsRefreshResult, error) {
	results := make([]AnalyticsRefreshResult, 0, len(analyticsAggregates))

	for _, aggregate := range analyticsAggregates {
		start, end, hasFullBucket, err := s.snapToBuckets(ctx, aggregate.bucket, from, to)
		if err != nil {
			return results, fmt.Errorf("align window for %s: %w", aggregate.name, err)
		}
		if !hasFullBucket {
			results = append(results, AnalyticsRefreshResult{Aggregate: aggregate.name, Skipped: true})
			continue
		}

		began := time.Now()

		// The bounds are cast explicitly: the procedure accepts several bound
		// types, so without a cast the server cannot infer which one applies.
		_, err = s.db.ExecContext(ctx,
			"CALL refresh_continuous_aggregate($1::regclass, $2::timestamptz, $3::timestamptz)",
			aggregate.name, start, end)
		if err != nil {
			return results, fmt.Errorf("refresh %s: %w", aggregate.name, err)
		}

		results = append(results, AnalyticsRefreshResult{
			Aggregate: aggregate.name,
			Duration:  time.Since(began),
		})
	}

	return results, nil
}

// snapToBuckets rounds the window bounds down to bucket boundaries and reports
// whether a complete bucket survives between them. A zero start means "as far
// back as the data goes"; a zero end is resolved to the present, never left
// open.
//
// The rounding is delegated to time_bucket rather than computed in Go, because
// its origin is not the UNIX epoch: buckets of a day or more are measured from
// 2000-01-03, which puts weekly boundaries on a Monday. Recomputing that here
// would risk drifting from how the aggregates are actually bucketed.
func (s *PostgresStore) snapToBuckets(
	ctx context.Context,
	bucket string,
	from, to time.Time,
) (start, end any, hasFullBucket bool, err error) {
	// An open upper bound is resolved to the present rather than left NULL.
	// NULL would refresh through the bucket currently being written, freezing
	// it at a partial value: the watermark advances past it and never moves
	// back, so nothing corrects it until a policy happens to reach that far.
	var snappedFrom, snappedTo *time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT time_bucket($1::interval, $2::timestamptz),
		       time_bucket($1::interval, COALESCE($3::timestamptz, now()))`,
		bucket, nullableTime(from), nullableTime(to),
	).Scan(&snappedFrom, &snappedTo)
	if err != nil {
		return nil, nil, false, fmt.Errorf("snap window to %s buckets: %w", bucket, err)
	}

	// A window with both ends inside the same bucket holds no complete bucket.
	if snappedFrom != nil && snappedTo != nil && !snappedFrom.Before(*snappedTo) {
		return nil, nil, false, nil
	}

	return timeOrNil(snappedFrom), timeOrNil(snappedTo), true, nil
}

// nullableTime converts a zero time into a NULL bound.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// timeOrNil unwraps an optional timestamp into a query argument.
func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
