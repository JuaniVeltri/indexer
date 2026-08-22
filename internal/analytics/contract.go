// Package analytics implements the network-wide analytics read API: time series
// for network metrics (transaction volume, fees, account activity, asset supply)
// and Top-N rankings of the most active entities.
//
// The request and response shapes in this file are a frozen contract. The
// explorer builds its dashboards against them, so field names, metric
// identifiers, and the empty-result behaviour must not change without
// coordinating with StellarViewOrg/stellarview-explorer. See docs/analytics-api.md.
package analytics

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidParam reports a query parameter that failed validation. Handlers
// translate it into a 400 response, distinguishing bad input from a genuine
// server-side failure.
var ErrInvalidParam = errors.New("invalid parameter")

// Metric identifies a network metric available as a time series.
type Metric string

const (
	// MetricTxCount counts transactions per bucket.
	MetricTxCount Metric = "tx_count"
	// MetricTxVolume totals native (XLM) transferred per bucket.
	MetricTxVolume Metric = "tx_volume"
	// MetricFeeClassic totals fees charged on non-Soroban transactions, in stroops.
	MetricFeeClassic Metric = "fee_classic"
	// MetricFeeSoroban totals the fee charged on Soroban transactions, in
	// stroops. This is the whole fee, not the Soroban resource fee: the
	// indexer does not record the resource component, so it cannot be
	// separated from the inclusion fee. Charting this as a resource fee
	// overstates it by the inclusion fee on every transaction.
	MetricFeeSoroban Metric = "fee_soroban"
	// MetricActiveAccounts counts distinct transaction source accounts per bucket.
	MetricActiveAccounts Metric = "active_accounts"
	// MetricNewAccounts counts create_account operations per bucket, including
	// those in transactions that failed — the operations are recorded either
	// way and an aggregate cannot join them to the transaction's status. Do
	// not present it as accounts successfully created.
	MetricNewAccounts Metric = "new_accounts"
	// MetricAssetSupply totals net supply change (mints minus burns and clawbacks).
	MetricAssetSupply Metric = "asset_supply"
)

// AllMetrics lists every supported time-series metric, in the order they are
// documented.
var AllMetrics = []Metric{
	MetricTxCount,
	MetricTxVolume,
	MetricFeeClassic,
	MetricFeeSoroban,
	MetricActiveAccounts,
	MetricNewAccounts,
	MetricAssetSupply,
}

// TopMetric identifies a Top-N ranking.
type TopMetric string

const (
	// TopContractActivity ranks contracts by events emitted, which is the
	// only per-contract activity signal the indexer records.
	TopContractActivity TopMetric = "contract_activity"
	// TopAssetTransfers ranks assets by transferred volume.
	TopAssetTransfers TopMetric = "asset_transfers"
	// TopHighestFees ranks individual transactions by fee charged.
	TopHighestFees TopMetric = "highest_fees"
)

// AllTopMetrics lists every supported Top-N metric.
var AllTopMetrics = []TopMetric{
	TopContractActivity,
	TopAssetTransfers,
	TopHighestFees,
}

// Resolution is the bucket width of a time series.
type Resolution string

const (
	ResolutionHourly Resolution = "hourly"
	ResolutionDaily  Resolution = "daily"
	ResolutionWeekly Resolution = "weekly"
)

// AllResolutions lists every supported resolution, coarsening left to right.
var AllResolutions = []Resolution{ResolutionHourly, ResolutionDaily, ResolutionWeekly}

// bucketIntervals maps each resolution to the PostgreSQL interval literal passed
// to time_bucket. Boundaries follow time_bucket's own origin — 2000-01-03 for
// buckets of a day or more, which puts weekly boundaries on a Monday — so a
// series derived from hourly rows lines up with one computed directly from the
// raw table.
var bucketIntervals = map[Resolution]string{
	ResolutionHourly: "1 hour",
	ResolutionDaily:  "1 day",
	ResolutionWeekly: "1 week",
}

// BucketInterval returns the PostgreSQL interval literal for this resolution.
func (r Resolution) BucketInterval() string {
	return bucketIntervals[r]
}

// Window is the rolling look-back period of a Top-N query.
type Window string

const (
	Window24h Window = "24h"
	Window7d  Window = "7d"
	Window30d Window = "30d"
)

// AllWindows lists every supported Top-N window.
var AllWindows = []Window{Window24h, Window7d, Window30d}

var windowDurations = map[Window]time.Duration{
	Window24h: 24 * time.Hour,
	Window7d:  7 * 24 * time.Hour,
	Window30d: 30 * 24 * time.Hour,
}

// Duration returns how far back this window reaches from the query time.
func (w Window) Duration() time.Duration {
	return windowDurations[w]
}

// TimeSeriesPoint is one bucket of a time series.
type TimeSeriesPoint struct {
	// Timestamp marks the start of the bucket, in UTC.
	Timestamp time.Time `json:"timestamp"`
	// Value is the aggregated value for the bucket.
	Value float64 `json:"value"`
}

// TimeSeriesResponse is the envelope returned by the time-series endpoint. Data
// is never null: a metric with nothing aggregated yet returns an empty slice,
// which the explorer renders as a "not available yet" state.
type TimeSeriesResponse struct {
	Metric     Metric            `json:"metric"`
	Resolution Resolution        `json:"resolution"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Data       []TimeSeriesPoint `json:"data"`
}

// TopEntry is one row of a Top-N ranking.
type TopEntry struct {
	// ID identifies the entity: a contract ID, a "CODE-ISSUER" asset key, or a
	// transaction hash.
	ID string `json:"id"`
	// Label is the human-readable form of ID.
	Label string `json:"label"`
	// Value is the ranking value: events emitted, transferred volume, or fee.
	Value float64 `json:"value"`
	// Metadata carries per-metric context and is omitted when empty.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TopResponse is the envelope returned by the Top-N endpoint. As with
// TimeSeriesResponse, Data is never null.
type TopResponse struct {
	Metric TopMetric  `json:"metric"`
	Window Window     `json:"window"`
	Data   []TopEntry `json:"data"`
}

// ParseMetric validates a raw metric parameter against AllMetrics.
func ParseMetric(raw string) (Metric, error) {
	for _, m := range AllMetrics {
		if Metric(raw) == m {
			return m, nil
		}
	}
	return "", fmt.Errorf("%w: metric %q, want one of %v", ErrInvalidParam, raw, AllMetrics)
}

// ParseTopMetric validates a raw metric parameter against AllTopMetrics.
func ParseTopMetric(raw string) (TopMetric, error) {
	for _, m := range AllTopMetrics {
		if TopMetric(raw) == m {
			return m, nil
		}
	}
	return "", fmt.Errorf("%w: metric %q, want one of %v", ErrInvalidParam, raw, AllTopMetrics)
}

// ParseResolution validates a raw resolution parameter.
func ParseResolution(raw string) (Resolution, error) {
	for _, r := range AllResolutions {
		if Resolution(raw) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: resolution %q, want one of %v", ErrInvalidParam, raw, AllResolutions)
}

// ParseWindow validates a raw window parameter.
func ParseWindow(raw string) (Window, error) {
	for _, w := range AllWindows {
		if Window(raw) == w {
			return w, nil
		}
	}
	return "", fmt.Errorf("%w: window %q, want one of %v", ErrInvalidParam, raw, AllWindows)
}
