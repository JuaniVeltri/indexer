package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// hashLabelPrefix is how many leading and trailing characters of a transaction
// hash survive truncation into a display label.
const hashLabelPrefix = 8

// TopN returns the highest-ranked entities for metric over [since, until),
// most valuable first. Every ranking breaks ties on a deterministic secondary
// key so repeating a query returns the same order.
//
// Both bounds are snapped to whole hours, because two of the three rankings
// read hourly aggregates and cannot resolve anything finer. Comparing raw
// instants against hour-aligned buckets shrank a 24-hour window to as little as
// 23h01m at the start while admitting the whole in-progress hour at the end —
// including rows dated after the window. highest_fees reads raw rows and is
// snapped identically, so all three cover exactly the same range.
func (s *PostgresStore) TopN(
	ctx context.Context,
	metric analytics.TopMetric,
	since, until time.Time,
	limit int,
) ([]analytics.TopEntry, error) {
	switch metric {
	case analytics.TopContractActivity:
		return s.topContractActivity(ctx, since, until, limit)
	case analytics.TopAssetTransfers:
		return s.topAssetTransfers(ctx, since, until, limit)
	case analytics.TopHighestFees:
		return s.topHighestFees(ctx, since, until, limit)
	default:
		return nil, fmt.Errorf("unsupported top-N metric %q", metric)
	}
}

// topContractActivity ranks contracts by events emitted. Contract metadata is
// joined in for a readable label, falling back to the identifier for contracts
// the indexer has not catalogued.
func (s *PostgresStore) topContractActivity(ctx context.Context, since, until time.Time, limit int) ([]analytics.TopEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.contract_id,
		       COALESCE(NULLIF(c.label, ''), NULLIF(c.token_name, ''), a.contract_id) AS label,
		       SUM(a.event_count)::double precision AS value,
		       c.contract_type,
		       c.token_symbol
		FROM analytics_contract_activity_hourly a
		LEFT JOIN contracts c ON c.contract_id = a.contract_id
		WHERE a.bucket >= time_bucket(INTERVAL '1 hour', $1::timestamptz)
		  AND a.bucket < time_bucket(INTERVAL '1 hour', $2::timestamptz)
		GROUP BY a.contract_id, c.label, c.token_name, c.contract_type, c.token_symbol
		ORDER BY value DESC, a.contract_id
		LIMIT $3`, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("query contract activity: %w", err)
	}
	defer rows.Close()

	entries := make([]analytics.TopEntry, 0, limit)
	for rows.Next() {
		var (
			entry        analytics.TopEntry
			contractType sql.NullInt64
			tokenSymbol  sql.NullString
		)
		if err := rows.Scan(&entry.ID, &entry.Label, &entry.Value, &contractType, &tokenSymbol); err != nil {
			return nil, fmt.Errorf("scan contract activity: %w", err)
		}

		entry.Metadata = metadata(map[string]any{
			"contract_type": nullableInt(contractType),
			"token_symbol":  nullableString(tokenSymbol),
		})
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read contract activity: %w", err)
	}

	return entries, nil
}

// topAssetTransfers ranks assets by transferred volume. Stored amounts are in
// each asset's base units, so they are scaled by the asset's decimals — falling
// back to the classic Stellar precision of 7 — before assets are compared.
func (s *PostgresStore) topAssetTransfers(ctx context.Context, since, until time.Time, limit int) ([]analytics.TopEntry, error) {
	query := fmt.Sprintf(`
		WITH scaled AS (
			SELECT
				-- The CASE is wrapped rather than each branch guarded: the schema
				-- allows a null asset_code beside a non-null issuer, and a null
				-- anywhere in a concatenation makes the whole identifier null,
				-- which would fail to scan and take the endpoint down with it.
				COALESCE(
					CASE
						WHEN t.asset_type = 0            THEN 'native'
						WHEN t.asset_issuer IS NOT NULL  THEN t.asset_code || '-' || t.asset_issuer
						ELSE t.asset_contract_id
					END,
					'unknown'
				) AS id,
				COALESCE(NULLIF(t.asset_code, ''), c.token_symbol, t.asset_contract_id, 'unknown') AS label,
				t.asset_issuer AS issuer,
				%[1]s AS decimals,
				t.amount_transferred / (10::numeric ^ %[1]s) AS units,
				t.transfer_count
			FROM analytics_asset_transfers_hourly t
			LEFT JOIN contracts c ON c.contract_id = t.asset_contract_id
			WHERE t.bucket >= time_bucket(INTERVAL '1 hour', $1::timestamptz)
				  AND t.bucket < time_bucket(INTERVAL '1 hour', $2::timestamptz)
		)
		SELECT id,
		       MIN(label) AS label,
		       SUM(units)::double precision AS value,
		       SUM(transfer_count) AS transfers,
		       MIN(issuer) AS issuer,
		       MIN(decimals) AS decimals
		FROM scaled
		GROUP BY id
		ORDER BY value DESC, id
		LIMIT $3`, decimalsExpr("t"))

	rows, err := s.db.QueryContext(ctx, query, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("query asset transfers: %w", err)
	}
	defer rows.Close()

	entries := make([]analytics.TopEntry, 0, limit)
	for rows.Next() {
		var (
			entry     analytics.TopEntry
			transfers int64
			issuer    sql.NullString
			decimals  int
		)
		if err := rows.Scan(&entry.ID, &entry.Label, &entry.Value, &transfers, &issuer, &decimals); err != nil {
			return nil, fmt.Errorf("scan asset transfers: %w", err)
		}

		entry.Metadata = metadata(map[string]any{
			"issuer":    nullableString(issuer),
			"transfers": transfers,
			"decimals":  decimals,
		})
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read asset transfers: %w", err)
	}

	return entries, nil
}

// topHighestFees ranks individual transactions, which no aggregate can
// summarise. It reads the raw hypertable through idx_tx_fee_charged, so each
// chunk in the window is scanned in fee order and merge-appended.
//
// fee_charged is selected unconverted and widened in Go on purpose. Casting it
// in the select list makes ORDER BY bind to the converted output column, which
// the index cannot satisfy: the plan degrades from a merge append over index
// scans to a sequential scan and a top-N sort.
func (s *PostgresStore) topHighestFees(ctx context.Context, since, until time.Time, limit int) ([]analytics.TopEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT hash, fee_charged, account, is_soroban
		FROM transactions
		WHERE created_at >= time_bucket(INTERVAL '1 hour', $1::timestamptz)
		  AND created_at < time_bucket(INTERVAL '1 hour', $2::timestamptz)
		ORDER BY fee_charged DESC, hash
		LIMIT $3`, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("query highest fees: %w", err)
	}
	defer rows.Close()

	entries := make([]analytics.TopEntry, 0, limit)
	for rows.Next() {
		var (
			entry      analytics.TopEntry
			feeCharged int64
			account    string
			isSoroban  bool
		)
		if err := rows.Scan(&entry.ID, &feeCharged, &account, &isSoroban); err != nil {
			return nil, fmt.Errorf("scan highest fees: %w", err)
		}

		entry.Value = float64(feeCharged)
		entry.Label = truncateHash(entry.ID)
		entry.Metadata = metadata(map[string]any{
			"account":    account,
			"is_soroban": isSoroban,
		})
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read highest fees: %w", err)
	}

	return entries, nil
}

// truncateHash shortens a transaction hash for display, keeping enough of both
// ends to stay recognisable.
func truncateHash(hash string) string {
	if len(hash) <= 2*hashLabelPrefix+1 {
		return hash
	}
	return hash[:hashLabelPrefix] + "…" + hash[len(hash)-hashLabelPrefix:]
}

// metadata drops absent entries so a ranking never carries null-valued keys,
// and returns nil when nothing is left for the field to be omitted entirely.
func metadata(fields map[string]any) map[string]any {
	for key, value := range fields {
		if value == nil {
			delete(fields, key)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func nullableString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
