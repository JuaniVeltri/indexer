package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestApplyDomainEvents_ReverseLookupAndIdempotency(t *testing.T) {
	s := getTestDB(t)
	defer s.Close()

	ctx := context.Background()
	node := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	_, _ = s.db.ExecContext(ctx, "DELETE FROM domain_events WHERE node = $1", node)
	_, _ = s.db.ExecContext(ctx, "DELETE FROM domains WHERE node = $1", node)
	defer func() {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM domain_events WHERE node = $1", node)
		_, _ = s.db.ExecContext(ctx, "DELETE FROM domains WHERE node = $1", node)
	}()

	t0 := time.Unix(1700000000, 0).UTC()
	exp1 := time.Unix(1800000000, 0).UTC()
	exp2 := time.Unix(1900000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	regAt := t0

	events := []DomainEvent{
		{
			Node: node, Name: "idxdemo.xlm", TLD: "xlm", Label: "idxdemo",
			EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
			ExpiresAt: &exp1, RegisteredAt: &regAt, TransactionHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			LedgerSequence: 10, CreatedAt: t0,
		},
		{
			Node: node, EventType: DomainEventTransfer, ResolvedAddress: addr2,
			TransactionHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			LedgerSequence:  11, CreatedAt: t0.Add(time.Second),
		},
		{
			Node: node, EventType: DomainEventRenew, ExpiresAt: &exp2,
			TransactionHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			LedgerSequence:  12, CreatedAt: t0.Add(2 * time.Second),
		},
	}
	if err := s.ApplyDomainEvents(ctx, events); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			t.Skip("domains table missing; apply migrations")
		}
		t.Fatalf("ApplyDomainEvents: %v", err)
	}
	// Idempotent replay.
	if err := s.ApplyDomainEvents(ctx, events); err != nil {
		t.Fatalf("idempotent ApplyDomainEvents: %v", err)
	}

	d, err := s.GetDomainByName(ctx, "idxdemo.xlm")
	if err != nil || d == nil {
		t.Fatalf("GetDomainByName: %v %#v", err, d)
	}
	if d.ResolvedAddress != addr2 {
		t.Errorf("resolved = %s, want %s", d.ResolvedAddress, addr2)
	}
	if !d.ExpiresAt.Equal(exp2) {
		t.Errorf("expires = %v, want %v", d.ExpiresAt, exp2)
	}

	old, err := s.GetDomainsByAddress(ctx, addr1)
	if err != nil {
		t.Fatalf("GetDomainsByAddress old: %v", err)
	}
	if len(old) != 0 {
		t.Errorf("old address should not reverse-lookup, got %+v", old)
	}
	cur, err := s.GetDomainsByAddress(ctx, addr2)
	if err != nil {
		t.Fatalf("GetDomainsByAddress new: %v", err)
	}
	if len(cur) != 1 || cur[0].Name != "idxdemo.xlm" {
		t.Errorf("new address reverse-lookup = %+v", cur)
	}

	hist, err := s.GetDomainEvents(ctx, "idxdemo.xlm", 50)
	if err != nil {
		t.Fatalf("GetDomainEvents: %v", err)
	}
	if len(hist) != 3 {
		t.Errorf("history len = %d, want 3 (idempotent insert)", len(hist))
	}

	list, err := s.ListDomains(ctx, DomainStatusActive, "", 50)
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	found := false
	for _, row := range list {
		if row.Name == "idxdemo.xlm" {
			found = true
		}
	}
	if !found {
		t.Error("active list missing idxdemo.xlm")
	}

	// Expire by setting expires_at in the past via a renew, then exclude from active.
	past := t0.Add(-time.Hour)
	if err := s.ApplyDomainEvents(ctx, []DomainEvent{{
		Node: node, EventType: DomainEventRenew, ExpiresAt: &past,
		TransactionHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		LedgerSequence:  13, CreatedAt: t0.Add(3 * time.Second),
	}}); err != nil {
		t.Fatalf("expire renew: %v", err)
	}
	active, err := s.ListDomains(ctx, DomainStatusActive, "", 50)
	if err != nil {
		t.Fatalf("ListDomains active after expire: %v", err)
	}
	for _, row := range active {
		if row.Name == "idxdemo.xlm" {
			t.Error("expired domain included in active list")
		}
	}
	expired, err := s.ListDomains(ctx, DomainStatusExpired, "", 50)
	if err != nil {
		t.Fatalf("ListDomains expired: %v", err)
	}
	found = false
	for _, row := range expired {
		if row.Name == "idxdemo.xlm" {
			found = true
		}
	}
	if !found {
		t.Error("expired list missing idxdemo.xlm")
	}

	got, err := s.GetDomainByName(ctx, "idxdemo.xlm")
	if err != nil || got == nil {
		t.Fatalf("history still visible: %v %#v", err, got)
	}
	if got.EffectiveStatus(t0) != DomainStatusExpired {
		t.Errorf("effective status = %s, want expired", got.EffectiveStatus(t0))
	}
}

func TestApplyDomainEvents_OutOfOrderTransferThenRegister(t *testing.T) {
	s := getTestDB(t)
	defer s.Close()

	ctx := context.Background()
	node := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	_, _ = s.db.ExecContext(ctx, "DELETE FROM domain_events WHERE node = $1", node)
	_, _ = s.db.ExecContext(ctx, "DELETE FROM domains WHERE node = $1", node)
	defer func() {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM domain_events WHERE node = $1", node)
		_, _ = s.db.ExecContext(ctx, "DELETE FROM domains WHERE node = $1", node)
	}()

	t0 := time.Unix(1700000000, 0).UTC()
	exp := time.Unix(1800000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	regAt := t0

	// Transfer lands first (parallel backfill).
	if err := s.ApplyDomainEvents(ctx, []DomainEvent{{
		Node: node, EventType: DomainEventTransfer, ResolvedAddress: addr2,
		TransactionHash: "1111111111111111111111111111111111111111111111111111111111111111",
		LedgerSequence:  30, CreatedAt: t0,
	}}); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			t.Skip("domains table missing; apply migrations")
		}
		t.Fatalf("transfer: %v", err)
	}

	var expires, registered sql.NullTime
	if err := s.QueryRow(ctx, "SELECT expires_at, registered_at FROM domains WHERE node = $1", node).Scan(&expires, &registered); err != nil {
		t.Fatalf("scan times after transfer: %v", err)
	}
	if expires.Valid {
		t.Errorf("transfer-only row must keep expires_at NULL, got %v", expires.Time)
	}
	if registered.Valid {
		t.Errorf("transfer-only row must keep registered_at NULL, got %v", registered.Time)
	}

	// Older register fills owner/expiry/name without moving the address.
	if err := s.ApplyDomainEvents(ctx, []DomainEvent{{
		Node: node, Name: "ooorder.xlm", TLD: "xlm", Label: "ooorder",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp, RegisteredAt: &regAt,
		TransactionHash: "2222222222222222222222222222222222222222222222222222222222222222",
		LedgerSequence:  10, CreatedAt: t0,
	}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	d, err := s.GetDomainByName(ctx, "ooorder.xlm")
	if err != nil || d == nil {
		t.Fatalf("GetDomainByName: %v %#v", err, d)
	}
	if d.Owner != addr1 {
		t.Errorf("owner = %q, want %s", d.Owner, addr1)
	}
	if d.ResolvedAddress != addr2 {
		t.Errorf("address = %s, want transfer target %s", d.ResolvedAddress, addr2)
	}
	if d.RegisteredAt.IsZero() || !d.RegisteredAt.Equal(regAt) {
		t.Errorf("registered_at = %v, want %v", d.RegisteredAt, regAt)
	}
	if d.ExpiresAt.IsZero() || !d.ExpiresAt.Equal(exp) {
		t.Errorf("expires_at = %v, want %v", d.ExpiresAt, exp)
	}
	if d.EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("status = %s, want active", d.EffectiveStatus(t0))
	}

	found, err := s.GetDomainsByAddress(ctx, addr2)
	if err != nil {
		t.Fatalf("GetDomainsByAddress: %v", err)
	}
	if len(found) != 1 || found[0].Name != "ooorder.xlm" || found[0].Owner != addr1 {
		t.Errorf("reverse lookup = %+v", found)
	}
	if found[0].EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("reverse-lookup status = %s, want active", found[0].EffectiveStatus(t0))
	}

	active, err := s.ListDomains(ctx, DomainStatusActive, "", 50)
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	ok := false
	for _, row := range active {
		if row.Name == "ooorder.xlm" {
			ok = true
		}
	}
	if !ok {
		t.Error("active list missing out-of-order domain")
	}
}
