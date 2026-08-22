package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Analytics fixtures live in 2013, before the Stellar network existed, so they
// cannot collide with real ingested data in a developer's local database.
//
// The past rather than the future matters: refreshing a continuous aggregate
// advances its watermark to the end of the refreshed region, and the watermark
// never moves back. Future-dated fixtures would leave a developer's aggregates
// watermarked years ahead, silently disabling real-time aggregation for all of
// their real data.
var fixtureBase = time.Date(2013, 3, 14, 0, 0, 0, 0, time.UTC)

// The window every fixture test owns. It is wide enough to contain a complete
// weekly bucket, so a test that spreads data across weeks is cleaned up by the
// same teardown as everything else: deleting rows over a narrower range than
// the refresh that materialized them would leave a phantom bucket that no
// later refresh can correct, because the watermark has already moved past it.
var (
	fixtureWindowStart = fixtureBase.Add(-time.Hour)
	fixtureWindowEnd   = fixtureBase.Add(30 * 24 * time.Hour)
)

// Accounts and contracts used by the fixture. The two contracts deliberately
// end up with equal event counts in one window so tie ordering can be asserted.
const (
	fixtureAccountA  = "GFIXTUREAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fixtureAccountB  = "GFIXTUREBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	fixtureAccountC  = "GFIXTURECCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	fixtureContract1 = "CFIXTURE1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fixtureContract2 = "CFIXTURE2BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	fixtureAssetCode = "FIXT"
	fixtureIssuer    = "GFIXTUREISSUERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	// A catalogued Soroban token with a non-default precision, so the decimals
	// join and the scaling it feeds are actually exercised rather than always
	// falling back to the classic 7.
	fixtureTokenContract = "CFIXTURETOKENAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fixtureTokenSymbol   = "FIXT2"
	fixtureTokenDecimals = 2

	// Stellar Asset Contracts wrapping the native and classic assets. Both are
	// catalogued with a precision that is deliberately wrong, to prove classic
	// amounts ignore it: the protocol fixes those at 7 decimals.
	fixtureNativeSAC        = "CFIXTURENATIVESACAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fixtureClassicSAC       = "CFIXTURECLASSICSACAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fixtureWrongSACDecimals = 0
)

// fixtureHash pads a label into the 64-character hash the schema requires,
// keeping fixture rows recognisable when inspecting the database by hand.
func fixtureHash(label string) string {
	const width = 64
	h := "fixture" + label
	for len(h) < width {
		h += "0"
	}
	return h[:width]
}

// fixtureExpectations records what the fixture data should aggregate to, so the
// tests compare against hand-computed numbers rather than a second query that
// could repeat the same mistake.
//
//	hour 0: 3 transactions from accounts A, A, B — fees 100 + 200 classic, 5000 soroban
//	hour 1: 2 transactions from accounts B, C     — fees 400 + 150 classic, no soroban
//
// Distinct accounts are therefore 2 in each hour but only 3 across the day,
// which is the case a summed rollup would get wrong.
var fixtureExpectations = struct {
	TxCountHour0, TxCountHour1           float64
	FeeClassicHour0, FeeClassicHour1     float64
	FeeSorobanHour0, FeeSorobanHour1     float64
	ActiveHour0, ActiveHour1, ActiveDay  float64
	NewAccountsHour0, NewAccountsHour1   float64
	VolumeXLMHour0, VolumeXLMHour1       float64
	SupplyHour0                          float64
	SorobanTokenUnits                    float64
	Contract1Events, Contract2Events     float64
	HighestFee, SecondHighestFee         float64
	HighestFeeHash, SecondHighestFeeHash string
}{
	TxCountHour0: 3, TxCountHour1: 2,
	FeeClassicHour0: 300, FeeClassicHour1: 550,
	FeeSorobanHour0: 5000, FeeSorobanHour1: 0,
	ActiveHour0: 2, ActiveHour1: 2, ActiveDay: 3,
	NewAccountsHour0: 2, NewAccountsHour1: 1,
	// 30,000,000 and 5,000,000 stroops, reported in XLM.
	VolumeXLMHour0: 3, VolumeXLMHour1: 0.5,
	// Minted 10 units, burned 3 units, at the classic 7-decimal scale.
	SupplyHour0: 7,
	// 12,345 base units of a 2-decimal token.
	SorobanTokenUnits: 123.45,
	// Both contracts emit 4 events across the fixture window: an exact tie.
	Contract1Events: 4, Contract2Events: 4,
	HighestFee: 5000, SecondHighestFee: 400,
	HighestFeeHash: fixtureHash("tx-h0-soroban"), SecondHighestFeeHash: fixtureHash("tx-h1-b"),
}

// insertAnalyticsFixture writes a deterministic two-hour slice of network
// activity and refreshes every analytics aggregate over it. The returned
// cleanup removes the rows and re-refreshes, so the aggregates end empty again
// and repeated runs stay independent.
func insertAnalyticsFixture(t *testing.T, s *PostgresStore) (from, to time.Time, cleanup func()) {
	t.Helper()

	from, to, cleanup = insertAnalyticsFixtureRows(t, s)

	if _, err := s.RefreshAnalyticsAggregates(context.Background(), from, to); err != nil {
		cleanup()
		t.Fatalf("refresh aggregates: %v", err)
	}
	return from, to, cleanup
}

// insertAnalyticsFixtureRows writes the same rows but leaves the aggregates
// unmaterialized, so a test can drive the refresh itself. Cleanup still deletes
// and re-refreshes, which is what leaves the window empty for the next run.
func insertAnalyticsFixtureRows(t *testing.T, s *PostgresStore) (from, to time.Time, cleanup func()) {
	t.Helper()

	ctx := context.Background()
	hour0 := fixtureBase
	hour1 := fixtureBase.Add(time.Hour)
	from, to = fixtureWindowStart, fixtureWindowEnd

	cleanup = func() {
		deleteAnalyticsFixture(t, s)
		if _, err := s.RefreshAnalyticsAggregates(ctx, from, to); err != nil {
			t.Errorf("cleanup refresh: %v", err)
		}
	}

	// Start from a clean slate in case a previous run was interrupted.
	deleteAnalyticsFixture(t, s)

	insertFixtureContracts(t, s)
	insertFixtureTransactions(t, s, hour0, hour1)
	insertFixtureOperations(t, s, hour0, hour1)
	insertFixtureTokenEvents(t, s, hour0, hour1)
	insertFixtureContractEvents(t, s, hour0, hour1)

	return from, to, cleanup
}

// insertFixtureContracts catalogues the Soroban token so the decimals join in
// the ranking and supply queries resolves to something other than the fallback.
func insertFixtureContracts(t *testing.T, s *PostgresStore) {
	t.Helper()

	catalogue := []struct {
		id       string
		symbol   string
		decimals int
	}{
		{fixtureTokenContract, fixtureTokenSymbol, fixtureTokenDecimals},
		{fixtureNativeSAC, "XLM", fixtureWrongSACDecimals},
		{fixtureClassicSAC, fixtureAssetCode, fixtureWrongSACDecimals},
	}

	for _, c := range catalogue {
		mustExec(t, s, `
			INSERT INTO contracts (contract_id, created_ledger, created_at, last_modified_ledger,
				contract_type, is_sep41_token, token_symbol, token_decimals)
			VALUES ($1, 900000, $2, 900000, 0, TRUE, $3, $4)
			ON CONFLICT (contract_id) DO UPDATE SET token_decimals = EXCLUDED.token_decimals`,
			c.id, fixtureBase, c.symbol, c.decimals)
	}
}

func insertFixtureTransactions(t *testing.T, s *PostgresStore, hour0, hour1 time.Time) {
	t.Helper()

	rows := []struct {
		hash      string
		account   string
		fee       int64
		isSoroban bool
		at        time.Time
	}{
		{fixtureHash("tx-h0-a1"), fixtureAccountA, 100, false, hour0.Add(1 * time.Minute)},
		{fixtureHash("tx-h0-a2"), fixtureAccountA, 200, false, hour0.Add(2 * time.Minute)},
		{fixtureHash("tx-h0-soroban"), fixtureAccountB, 5000, true, hour0.Add(3 * time.Minute)},
		{fixtureHash("tx-h1-b"), fixtureAccountB, 400, false, hour1.Add(1 * time.Minute)},
		{fixtureHash("tx-h1-c"), fixtureAccountC, 150, false, hour1.Add(2 * time.Minute)},
	}

	for i, r := range rows {
		mustExec(t, s, `
			INSERT INTO transactions (hash, ledger_sequence, application_order, account,
				account_sequence, fee_charged, max_fee, operation_count, memo_type, status,
				is_soroban, envelope_xdr, result_xdr, created_at)
			VALUES ($1, $2, $3, $4, 1, $5, $5, 1, 0, 1, $6, 'fixture', 'fixture', $7)`,
			r.hash, 900000+i, i+1, r.account, r.fee, r.isSoroban, r.at)
	}
}

func insertFixtureOperations(t *testing.T, s *PostgresStore, hour0, hour1 time.Time) {
	t.Helper()

	// Two account creations in the first hour, one in the second, plus a payment
	// that must not be counted as a new account. The numeric type matters: the
	// aggregate filters on it, not on the name.
	const (
		createAccountType = 0
		paymentType       = 1
	)
	ops := []struct {
		opType   int16
		typeName string
		at       time.Time
	}{
		{createAccountType, "create_account", hour0.Add(1 * time.Minute)},
		{createAccountType, "create_account", hour0.Add(2 * time.Minute)},
		{paymentType, "payment", hour0.Add(3 * time.Minute)},
		{createAccountType, "create_account", hour1.Add(1 * time.Minute)},
	}

	for i, op := range ops {
		mustExec(t, s, `
			INSERT INTO operations (transaction_id, transaction_hash, application_order,
				type, type_name, details, created_at)
			VALUES ($1, $2, 1, $3, $4, '{}'::jsonb, $5)`,
			900000+i, fixtureHash(fmt.Sprintf("op%d", i)), op.opType, op.typeName, op.at)
	}
}

func insertFixtureTokenEvents(t *testing.T, s *PostgresStore, hour0, hour1 time.Time) {
	t.Helper()

	events := []struct {
		eventType int16
		name      string
		assetType int16
		amount    string
		at        time.Time
	}{
		// Native transfers: 3 XLM in hour 0, 0.5 XLM in hour 1.
		{0, "transfer", 0, "10000000", hour0.Add(1 * time.Minute)},
		{0, "transfer", 0, "20000000", hour0.Add(2 * time.Minute)},
		{0, "transfer", 0, "5000000", hour1.Add(1 * time.Minute)},
		// Fee events must never reach the volume metric.
		{4, "fee", 0, "99000000", hour0.Add(3 * time.Minute)},
		// Supply: mint 10 units, burn 3 units of a classic asset.
		{1, "mint", 1, "100000000", hour0.Add(4 * time.Minute)},
		{2, "burn", 1, "30000000", hour0.Add(5 * time.Minute)},
		// A catalogued 2-decimal Soroban token: 12,345 base units = 123.45.
		{0, "transfer", 2, "12345", hour0.Add(6 * time.Minute)},
	}

	for i, e := range events {
		// Every real token event carries the asset's contract id, including
		// native and classic assets through their Stellar Asset Contract. The
		// fixture mirrors that so the decimals join is exercised on the same
		// shape production produces.
		var code, issuer, contract any
		switch e.assetType {
		case 0:
			code, issuer, contract = "XLM", nil, fixtureNativeSAC
		case 2:
			code, issuer, contract = nil, nil, fixtureTokenContract
		default:
			code, issuer, contract = fixtureAssetCode, fixtureIssuer, fixtureClassicSAC
		}
		mustExec(t, s, `
			INSERT INTO token_events (event_type, event_type_name, asset_type, asset_code,
				asset_issuer, asset_contract_id, amount, transaction_hash, ledger_sequence, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			e.eventType, e.name, e.assetType, code, issuer, contract, e.amount,
			fixtureHash(fmt.Sprintf("te%d", i)), 900000+i, e.at)
	}
}

func insertFixtureContractEvents(t *testing.T, s *PostgresStore, hour0, hour1 time.Time) {
	t.Helper()

	// Four events each, so the two contracts tie and the ranking must fall back
	// to its deterministic secondary key.
	events := []struct {
		contract string
		at       time.Time
	}{
		{fixtureContract1, hour0.Add(1 * time.Minute)},
		{fixtureContract1, hour0.Add(2 * time.Minute)},
		{fixtureContract1, hour0.Add(3 * time.Minute)},
		{fixtureContract2, hour0.Add(4 * time.Minute)},
		{fixtureContract1, hour1.Add(1 * time.Minute)},
		{fixtureContract2, hour1.Add(2 * time.Minute)},
		{fixtureContract2, hour1.Add(3 * time.Minute)},
		{fixtureContract2, hour1.Add(4 * time.Minute)},
	}

	for i, e := range events {
		mustExec(t, s, `
			INSERT INTO contract_events (contract_id, transaction_hash, ledger_sequence,
				type, topics_xdr, value_xdr, created_at)
			VALUES ($1, $2, $3, 0, 'fixture', 'fixture', $4)`,
			e.contract, fixtureHash(fmt.Sprintf("ce%d", i)), 900000+i, e.at)
	}
}

func deleteAnalyticsFixture(t *testing.T, s *PostgresStore) {
	t.Helper()

	for _, table := range []string{"transactions", "operations", "token_events", "contract_events"} {
		mustExec(t, s, fmt.Sprintf(
			"DELETE FROM %s WHERE created_at >= $1 AND created_at < $2", table),
			fixtureWindowStart, fixtureWindowEnd)
	}
	mustExec(t, s, "DELETE FROM contracts WHERE contract_id LIKE 'CFIXTURE%'")
}

func mustExec(t *testing.T, s *PostgresStore, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %.60s...: %v", query, err)
	}
}
