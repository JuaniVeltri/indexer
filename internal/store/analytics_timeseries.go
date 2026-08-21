package store

import (
	"context"
	"fmt"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// stroopsPerXLM scales the native asset's base units into whole XLM.
const stroopsPerXLM = 1e7

// timeSeriesSource describes how one metric is read: the relation holding its
// hourly buckets, and the expression that turns those rows into a bucket value.
//
// Every metric is served through the same query template, which re-buckets the
// hourly aggregate to the requested resolution. For active_accounts the
// relation already matches the requested resolution, so the outer grouping sees
// exactly one row per bucket and the sum is an identity — the distinct count is
// never rolled up, which would overstate it.
type timeSeriesSource struct {
	relation string
	value    string
}

// hourlySources maps the metrics whose relation does not depend on resolution.
var hourlySources = map[analytics.Metric]timeSeriesSource{
	analytics.MetricTxCount: {
		relation: "analytics_tx_hourly",
		value:    "SUM(tx_count)",
	},
	analytics.MetricFeeClassic: {
		relation: "analytics_tx_hourly",
		// A bucket with only Soroban traffic leaves the filtered sum NULL.
		value: "SUM(COALESCE(fee_classic, 0))",
	},
	analytics.MetricFeeSoroban: {
		relation: "analytics_tx_hourly",
		value:    "SUM(COALESCE(fee_soroban, 0))",
	},
	analytics.MetricNewAccounts: {
		relation: "analytics_new_accounts_hourly",
		value:    "SUM(new_accounts)",
	},
	analytics.MetricTxVolume: {
		relation: "analytics_volume_hourly",
		value:    fmt.Sprintf("SUM(stroops_transferred) / %.0f", stroopsPerXLM),
	},
	// Supply is stored per asset in base units, so each row is scaled by that
	// asset's decimals before the assets are summed together.
	analytics.MetricAssetSupply: {
		relation: `(
			SELECT s.bucket,
			       s.net_supply_delta / (10::numeric ^ ` + safeDecimals + `) AS units
			FROM analytics_asset_supply_hourly s
			LEFT JOIN contracts c ON c.contract_id = s.asset_contract_id
		) AS asset_supply`,
		value: "SUM(units)",
	},
}

// safeDecimals is the scaling exponent for an asset's base units.
//
// contracts.token_decimals is read from an arbitrary contract's decimals()
// entry point and stored unvalidated, so it can be absurd or negative. Used
// raw as an exponent it takes down the whole query rather than one row:
// a large value overflows the numeric format, and a sufficiently negative one
// underflows the divisor to zero. Clamping to the representable range keeps a
// hostile or buggy token from turning a public endpoint into a 500.
const safeDecimals = `GREATEST(0, LEAST(38, COALESCE(c.token_decimals, 7)))`

// activeAccountViews maps each resolution to its dedicated distinct-count
// aggregate.
var activeAccountViews = map[analytics.Resolution]string{
	analytics.ResolutionHourly: "analytics_active_accounts_hourly",
	analytics.ResolutionDaily:  "analytics_active_accounts_daily",
	analytics.ResolutionWeekly: "analytics_active_accounts_weekly",
}

// sourceFor resolves the relation and value expression for a metric at a
// resolution.
func sourceFor(metric analytics.Metric, resolution analytics.Resolution) (timeSeriesSource, error) {
	if metric == analytics.MetricActiveAccounts {
		view, ok := activeAccountViews[resolution]
		if !ok {
			return timeSeriesSource{}, fmt.Errorf("no active_accounts aggregate for resolution %q", resolution)
		}
		return timeSeriesSource{relation: view, value: "SUM(active_accounts)"}, nil
	}

	source, ok := hourlySources[metric]
	if !ok {
		return timeSeriesSource{}, fmt.Errorf("unsupported metric %q", metric)
	}
	return source, nil
}

// TimeSeries returns one point per bucket for metric between from (inclusive)
// and to (exclusive), bucketed at the requested resolution. Buckets with no
// activity are absent rather than zero, and a range with no data at all yields
// an empty series rather than an error.
func (s *PostgresStore) TimeSeries(
	ctx context.Context,
	metric analytics.Metric,
	resolution analytics.Resolution,
	from, to time.Time,
) ([]analytics.TimeSeriesPoint, error) {
	source, err := sourceFor(metric, resolution)
	if err != nil {
		return nil, err
	}

	// The lower bound is snapped down to a bucket boundary before filtering.
	// Filtering on the caller's raw timestamp would slice the leading bucket:
	// an hourly-backed metric would drop the hours before it and report the
	// remainder as a whole day, while active_accounts — already bucketed at the
	// requested resolution — would drop that bucket entirely. Snapping keeps
	// every returned bucket complete and makes both families agree.
	//
	// The relation and value expression come from the tables above, never from
	// request input; the caller-supplied values are all bound parameters.
	query := fmt.Sprintf(`
		SELECT time_bucket($1::interval, bucket) AS ts,
		       (%s)::double precision AS value
		FROM %s
		WHERE bucket >= time_bucket($1::interval, $2::timestamptz)
		  AND bucket < $3
		GROUP BY ts
		ORDER BY ts`, source.value, source.relation)

	rows, err := s.db.QueryContext(ctx, query, resolution.BucketInterval(), from, to)
	if err != nil {
		return nil, fmt.Errorf("query %s time series: %w", metric, err)
	}
	defer rows.Close()

	points := make([]analytics.TimeSeriesPoint, 0)
	for rows.Next() {
		var p analytics.TimeSeriesPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err != nil {
			return nil, fmt.Errorf("scan %s bucket: %w", metric, err)
		}
		p.Timestamp = p.Timestamp.UTC()
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s time series: %w", metric, err)
	}

	return points, nil
}
