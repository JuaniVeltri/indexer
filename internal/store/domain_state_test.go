package store

import (
	"testing"
	"time"
)

func TestApplyDomainTransition_RegisterTransferRenewExpireRevoke(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp1 := time.Unix(1800000000, 0).UTC()
	exp2 := time.Unix(1900000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	regAt := t0
	register := DomainEvent{
		Node:            "aa",
		Name:            "stellar.xlm",
		TLD:             "xlm",
		Label:           "stellar",
		EventType:       DomainEventRegister,
		Owner:           addr1,
		ResolvedAddress: addr1,
		ExpiresAt:       &exp1,
		RegisteredAt:    &regAt,
		TransactionHash: "tx1",
		LedgerSequence:  10,
		CreatedAt:       t0,
	}
	d := ApplyDomainTransition(nil, register)
	if d.Name != "stellar.xlm" || d.Owner != addr1 || d.ResolvedAddress != addr1 {
		t.Fatalf("register: got %+v", d)
	}
	if d.EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("register status = %s, want active", d.EffectiveStatus(t0))
	}

	transfer := DomainEvent{
		Node:            "aa",
		EventType:       DomainEventTransfer,
		ResolvedAddress: addr2,
		TransactionHash: "tx2",
		LedgerSequence:  11,
		CreatedAt:       t0.Add(time.Second),
	}
	d = ApplyDomainTransition(&d, transfer)
	if d.ResolvedAddress != addr2 {
		t.Errorf("after transfer resolved = %s, want %s", d.ResolvedAddress, addr2)
	}
	if d.Owner != addr1 {
		t.Errorf("transfer must not change owner, got %s", d.Owner)
	}

	renew := DomainEvent{
		Node:            "aa",
		EventType:       DomainEventRenew,
		ExpiresAt:       &exp2,
		TransactionHash: "tx3",
		LedgerSequence:  12,
		CreatedAt:       t0.Add(2 * time.Second),
	}
	d = ApplyDomainTransition(&d, renew)
	if !d.ExpiresAt.Equal(exp2) {
		t.Errorf("after renew expires = %v, want %v", d.ExpiresAt, exp2)
	}
	if d.ResolvedAddress != addr2 {
		t.Errorf("renew must keep resolved address %s, got %s", addr2, d.ResolvedAddress)
	}

	if d.EffectiveStatus(exp2.Add(time.Second)) != DomainStatusExpired {
		t.Errorf("past expiry: status = %s, want expired", d.EffectiveStatus(exp2.Add(time.Second)))
	}

	revoke := DomainEvent{
		Node:            "aa",
		EventType:       DomainEventRevoke,
		TransactionHash: "tx4",
		LedgerSequence:  13,
		CreatedAt:       t0.Add(3 * time.Second),
	}
	d = ApplyDomainTransition(&d, revoke)
	if d.EffectiveStatus(t0) != DomainStatusRevoked {
		t.Errorf("after revoke status = %s, want revoked", d.EffectiveStatus(t0))
	}
}

func TestApplyDomainTransition_StaleEventDoesNotRollBack(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp := time.Unix(1800000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	regAt := t0

	d := ApplyDomainTransition(nil, DomainEvent{
		Node: "bb", Name: "stellar.xlm", TLD: "xlm", Label: "stellar",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp, RegisteredAt: &regAt, LedgerSequence: 20, CreatedAt: t0,
	})
	d = ApplyDomainTransition(&d, DomainEvent{
		Node: "bb", EventType: DomainEventTransfer, ResolvedAddress: addr2,
		LedgerSequence: 30, CreatedAt: t0,
	})

	// Out-of-order replay of the original register must not restore addr1.
	d = ApplyDomainTransition(&d, DomainEvent{
		Node: "bb", Name: "stellar.xlm", TLD: "xlm", Label: "stellar",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp, RegisteredAt: &regAt, LedgerSequence: 20, CreatedAt: t0,
	})
	if d.ResolvedAddress != addr2 {
		t.Errorf("stale register rolled back address: got %s, want %s", d.ResolvedAddress, addr2)
	}
	if d.Owner != addr1 {
		t.Errorf("stale register must not clear owner, got %q", d.Owner)
	}
	if !d.ExpiresAt.Equal(exp) {
		t.Errorf("stale register must not clear expiry, got %v", d.ExpiresAt)
	}
	if d.Name != "stellar.xlm" {
		t.Errorf("name should remain stellar.xlm, got %q", d.Name)
	}
}

func TestApplyDomainTransition_OutOfOrderTransferThenRegister(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp := time.Unix(1800000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	regAt := t0

	d := ApplyDomainTransition(nil, DomainEvent{
		Node: "cc", EventType: DomainEventTransfer, ResolvedAddress: addr2,
		LedgerSequence: 30, CreatedAt: t0,
	})
	if d.ResolvedAddress != addr2 {
		t.Fatalf("transfer-first resolved = %s", d.ResolvedAddress)
	}

	d = ApplyDomainTransition(&d, DomainEvent{
		Node: "cc", Name: "alice.xlm", TLD: "xlm", Label: "alice",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp, RegisteredAt: &regAt, LedgerSequence: 10, CreatedAt: t0,
	})
	if d.Name != "alice.xlm" {
		t.Errorf("expected name filled in, got %q", d.Name)
	}
	if d.Owner != addr1 {
		t.Errorf("stale register must fill missing owner, got %q", d.Owner)
	}
	if d.RegisteredAt.IsZero() || !d.RegisteredAt.Equal(regAt) {
		t.Errorf("stale register must fill missing registered_at, got %v", d.RegisteredAt)
	}
	if d.ExpiresAt.IsZero() || !d.ExpiresAt.Equal(exp) {
		t.Errorf("stale register must fill missing expires_at, got %v", d.ExpiresAt)
	}
	if d.ResolvedAddress != addr2 {
		t.Errorf("older register must not overwrite newer transfer, got %s", d.ResolvedAddress)
	}
	if d.EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("out-of-order row status = %s, want active", d.EffectiveStatus(t0))
	}
}

func TestApplyDomainTransition_OutOfOrderRenewThenRegister(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp1 := time.Unix(1800000000, 0).UTC()
	exp2 := time.Unix(1900000000, 0).UTC()
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	regAt := t0

	d := ApplyDomainTransition(nil, DomainEvent{
		Node: "ff", EventType: DomainEventRenew, ExpiresAt: &exp2,
		LedgerSequence: 30, CreatedAt: t0,
	})
	d = ApplyDomainTransition(&d, DomainEvent{
		Node: "ff", Name: "bob.xlm", TLD: "xlm", Label: "bob",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp1, RegisteredAt: &regAt, LedgerSequence: 10, CreatedAt: t0,
	})
	if d.Owner != addr1 {
		t.Errorf("stale register must fill missing owner, got %q", d.Owner)
	}
	if !d.ExpiresAt.Equal(exp2) {
		t.Errorf("older register must not overwrite newer renew expiry: got %v, want %v", d.ExpiresAt, exp2)
	}
	if d.ResolvedAddress != addr1 {
		t.Errorf("stale register should fill empty address, got %s", d.ResolvedAddress)
	}
	if d.EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("status = %s, want active", d.EffectiveStatus(t0))
	}
}

func TestApplyDomainTransition_ClaimRestoresActive(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp1 := t0.Add(-time.Hour)
	exp2 := t0.Add(365 * 24 * time.Hour)
	addr1 := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	addr2 := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	regAt := t0.Add(-2 * time.Hour)

	d := ApplyDomainTransition(nil, DomainEvent{
		Node: "dd", Name: "old.xlm", TLD: "xlm", Label: "old",
		EventType: DomainEventRegister, Owner: addr1, ResolvedAddress: addr1,
		ExpiresAt: &exp1, RegisteredAt: &regAt, LedgerSequence: 1, CreatedAt: t0,
	})
	if d.EffectiveStatus(t0) != DomainStatusExpired {
		t.Fatalf("want expired before claim, got %s", d.EffectiveStatus(t0))
	}

	d = ApplyDomainTransition(&d, DomainEvent{
		Node: "dd", EventType: DomainEventClaim, Owner: addr2, ResolvedAddress: addr2,
		ExpiresAt: &exp2, LedgerSequence: 2, CreatedAt: t0,
	})
	if d.Owner != addr2 || d.ResolvedAddress != addr2 {
		t.Errorf("claim owner/address = %s/%s", d.Owner, d.ResolvedAddress)
	}
	if d.EffectiveStatus(t0) != DomainStatusActive {
		t.Errorf("after claim status = %s, want active", d.EffectiveStatus(t0))
	}
}

func TestApplyDomainTransition_IdempotentSameEvent(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	exp := time.Unix(1800000000, 0).UTC()
	addr := "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	regAt := t0
	ev := DomainEvent{
		Node: "ee", Name: "idem.xlm", TLD: "xlm", Label: "idem",
		EventType: DomainEventRegister, Owner: addr, ResolvedAddress: addr,
		ExpiresAt: &exp, RegisteredAt: &regAt, LedgerSequence: 5, CreatedAt: t0,
		TransactionHash: "same",
	}
	d := ApplyDomainTransition(nil, ev)
	again := ApplyDomainTransition(&d, ev)
	if again.ResolvedAddress != d.ResolvedAddress || again.LastEventLedger != d.LastEventLedger {
		t.Errorf("re-applying the same event changed state")
	}
}
