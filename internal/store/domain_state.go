package store

import "strings"

// ApplyDomainTransition folds a registry event into the current domain row.
// Newer ledgers win for mutable state. Older events (parallel backfill) only
// fill fields that are still empty so a transfer/renew that lands before
// register still reconstructs owner, expiry, and name without rolling back
// the later address.
func ApplyDomainTransition(cur *Domain, ev DomainEvent) Domain {
	var d Domain
	if cur != nil {
		d = *cur
	} else {
		d.Status = DomainStatusActive
	}
	d.Node = firstNonEmpty(d.Node, ev.Node)
	fillMissingDomainFields(&d, ev)

	stale := cur != nil && ev.LedgerSequence < cur.LastEventLedger
	if stale {
		return d
	}

	switch ev.EventType {
	case DomainEventRegister:
		if ev.Owner != "" {
			d.Owner = ev.Owner
		}
		if ev.ResolvedAddress != "" {
			d.ResolvedAddress = ev.ResolvedAddress
			d.TargetType = targetTypeOf(ev.ResolvedAddress)
		}
		if ev.RegisteredAt != nil && (d.RegisteredAt.IsZero() || ev.RegisteredAt.Before(d.RegisteredAt)) {
			d.RegisteredAt = *ev.RegisteredAt
		}
		if ev.ExpiresAt != nil {
			d.ExpiresAt = *ev.ExpiresAt
		}
		d.Status = DomainStatusActive
	case DomainEventTransfer:
		if ev.ResolvedAddress != "" {
			d.ResolvedAddress = ev.ResolvedAddress
			d.TargetType = targetTypeOf(ev.ResolvedAddress)
		}
	case DomainEventRenew:
		if ev.ExpiresAt != nil {
			d.ExpiresAt = *ev.ExpiresAt
		}
		if d.Status != DomainStatusRevoked {
			d.Status = DomainStatusActive
		}
	case DomainEventClaim:
		if ev.Owner != "" {
			d.Owner = ev.Owner
		}
		if ev.ResolvedAddress != "" {
			d.ResolvedAddress = ev.ResolvedAddress
			d.TargetType = targetTypeOf(ev.ResolvedAddress)
		}
		if ev.ExpiresAt != nil {
			d.ExpiresAt = *ev.ExpiresAt
		}
		d.Status = DomainStatusActive
	case DomainEventRevoke:
		d.Status = DomainStatusRevoked
	}

	if ev.LedgerSequence >= d.LastEventLedger {
		d.LastEventLedger = ev.LedgerSequence
		d.LastEventTx = ev.TransactionHash
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = ev.CreatedAt
	}
	d.UpdatedAt = ev.CreatedAt
	return d
}

func fillMissingDomainFields(d *Domain, ev DomainEvent) {
	if d.Name == "" && ev.Name != "" {
		d.Name = ev.Name
	}
	if d.TLD == "" && ev.TLD != "" {
		d.TLD = ev.TLD
	}
	if d.Label == "" && ev.Label != "" {
		d.Label = ev.Label
	}
	if d.Owner == "" && ev.Owner != "" {
		d.Owner = ev.Owner
	}
	if d.RegisteredAt.IsZero() && ev.RegisteredAt != nil {
		d.RegisteredAt = *ev.RegisteredAt
	}
	if d.ExpiresAt.IsZero() && ev.ExpiresAt != nil {
		d.ExpiresAt = *ev.ExpiresAt
	}
	if d.ResolvedAddress == "" && ev.ResolvedAddress != "" {
		d.ResolvedAddress = ev.ResolvedAddress
		d.TargetType = targetTypeOf(ev.ResolvedAddress)
	}
}

func targetTypeOf(addr string) string {
	if strings.HasPrefix(addr, "C") {
		return DomainTargetContract
	}
	return DomainTargetAccount
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
