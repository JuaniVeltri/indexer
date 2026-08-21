package store

import (
	"context"
	"testing"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// The Top-N window is closed at both ends, which keeps these assertions
// isolated from any real data a developer has ingested outside the fixture's
// 2013 window.
var (
	fixtureSince = fixtureBase.Add(-time.Hour)
	fixtureUntil = fixtureBase.Add(48 * time.Hour)
)

func TestTopNContractActivityRanksByEventCount(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	entries, err := store.TopN(context.Background(), analytics.TopContractActivity, fixtureSince, fixtureUntil, 10)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	if entries[0].Value != fixtureExpectations.Contract1Events {
		t.Errorf("top contract value = %v, want %v", entries[0].Value, fixtureExpectations.Contract1Events)
	}
	// Neither contract has a row in the contracts table, so the label falls back
	// to the identifier rather than coming back empty.
	if entries[0].Label == "" {
		t.Error("entry label must never be empty")
	}
}

func TestTopNAssetTransfersScalesAmountsByDecimals(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	entries, err := store.TopN(context.Background(), analytics.TopAssetTransfers, fixtureSince, fixtureUntil, 10)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (only the native asset is transferred): %+v", len(entries), entries)
	}

	// 30,000,000 + 5,000,000 stroops reported in whole XLM.
	const wantXLM = 3.5
	if entries[0].Value != wantXLM {
		t.Errorf("native transfer volume = %v, want %v", entries[0].Value, wantXLM)
	}
	if entries[0].ID != "native" {
		t.Errorf("native asset id = %q, want %q", entries[0].ID, "native")
	}
}

func TestTopNHighestFeesRanksIndividualTransactions(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	entries, err := store.TopN(context.Background(), analytics.TopHighestFees, fixtureSince, fixtureUntil, 2)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	want := fixtureExpectations
	if entries[0].ID != want.HighestFeeHash || entries[0].Value != want.HighestFee {
		t.Errorf("first = (%s, %v), want (%s, %v)",
			entries[0].ID, entries[0].Value, want.HighestFeeHash, want.HighestFee)
	}
	if entries[1].ID != want.SecondHighestFeeHash || entries[1].Value != want.SecondHighestFee {
		t.Errorf("second = (%s, %v), want (%s, %v)",
			entries[1].ID, entries[1].Value, want.SecondHighestFeeHash, want.SecondHighestFee)
	}
	// The label is a shortened hash, not the full 64 characters.
	if len(entries[0].Label) >= len(entries[0].ID) {
		t.Errorf("label %q should be a truncated form of %q", entries[0].Label, entries[0].ID)
	}
}

// TestTopNTieOrderingIsStable is acceptance criterion #3. Both fixture
// contracts emit exactly four events, so only the deterministic secondary sort
// keeps repeated queries in agreement.
func TestTopNTieOrderingIsStable(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	first, err := store.TopN(context.Background(), analytics.TopContractActivity, fixtureSince, fixtureUntil, 10)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(first) < 2 || first[0].Value != first[1].Value {
		t.Fatalf("fixture should produce a tie, got %+v", first)
	}

	for range 5 {
		again, err := store.TopN(context.Background(), analytics.TopContractActivity, fixtureSince, fixtureUntil, 10)
		if err != nil {
			t.Fatalf("TopN: %v", err)
		}
		for i := range first {
			if again[i].ID != first[i].ID {
				t.Fatalf("tied ranking reordered between calls: position %d was %s, now %s",
					i, first[i].ID, again[i].ID)
			}
		}
	}

	// The tie must break on the identifier, ascending.
	if first[0].ID > first[1].ID {
		t.Errorf("tie broken in descending id order: %s before %s", first[0].ID, first[1].ID)
	}
}

func TestTopNReturnsEmptyForAQuietWindow(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	quiet := time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)

	for _, metric := range analytics.AllTopMetrics {
		entries, err := store.TopN(context.Background(), metric, quiet, quiet.Add(24*time.Hour), 10)
		if err != nil {
			t.Errorf("TopN(%s): %v", metric, err)
			continue
		}
		if len(entries) != 0 {
			t.Errorf("TopN(%s) returned %d entries for a quiet window", metric, len(entries))
		}
	}
}

func TestTopNRespectsTheLimit(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	entries, err := store.TopN(context.Background(), analytics.TopHighestFees, fixtureSince, fixtureUntil, 3)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

// TestTopNAssetTransfersToleratesIncompleteAssetIdentity guards a scan failure.
// The schema allows a null asset_code beside a non-null issuer, and a null
// anywhere in a SQL concatenation makes the whole expression null — which would
// fail to scan into the entry identifier and turn the endpoint into a 500.
func TestTopNAssetTransfersToleratesIncompleteAssetIdentity(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	mustExec(t, store, `
		INSERT INTO token_events (event_type, event_type_name, asset_type, asset_code,
			asset_issuer, amount, transaction_hash, ledger_sequence, created_at)
		VALUES (0, 'transfer', 1, NULL, $1, '5000000', $2, 900500, $3)`,
		fixtureIssuer, fixtureHash("te-nullcode"), fixtureBase.Add(30*time.Minute))

	if _, err := store.RefreshAnalyticsAggregates(context.Background(), fixtureSince, fixtureUntil); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	entries, err := store.TopN(context.Background(), analytics.TopAssetTransfers, fixtureSince, fixtureUntil, 10)
	if err != nil {
		t.Fatalf("TopN must survive an asset with no code: %v", err)
	}

	for _, e := range entries {
		if e.ID == "" {
			t.Errorf("entry identifier must never be empty: %+v", e)
		}
	}
}
