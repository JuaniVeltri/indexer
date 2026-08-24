package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ApplyDomainEvents upserts current domain state and appends history.
// History inserts are idempotent on (transaction_hash, node, event_type).
func (s *PostgresStore) ApplyDomainEvents(ctx context.Context, events []DomainEvent) error {
	if len(events) == 0 {
		return nil
	}

	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer dbTx.Rollback()

	for _, ev := range events {
		if err := applyOneDomainEvent(ctx, dbTx, ev); err != nil {
			return err
		}
	}
	return dbTx.Commit()
}

func applyOneDomainEvent(ctx context.Context, dbTx *sql.Tx, ev DomainEvent) error {
	cur, err := getDomainByNodeTx(ctx, dbTx, ev.Node)
	if err != nil {
		return err
	}
	next := ApplyDomainTransition(cur, ev)

	if _, err := dbTx.ExecContext(ctx, `
		INSERT INTO domains (
			node, name, tld, label, owner, resolved_address, target_type,
			registered_at, expires_at, status, last_event_ledger, last_event_tx,
			created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (node) DO UPDATE SET
			name = EXCLUDED.name,
			tld = EXCLUDED.tld,
			label = EXCLUDED.label,
			owner = EXCLUDED.owner,
			resolved_address = EXCLUDED.resolved_address,
			target_type = EXCLUDED.target_type,
			registered_at = EXCLUDED.registered_at,
			expires_at = EXCLUDED.expires_at,
			status = EXCLUDED.status,
			last_event_ledger = EXCLUDED.last_event_ledger,
			last_event_tx = EXCLUDED.last_event_tx,
			updated_at = EXCLUDED.updated_at`,
		next.Node, next.Name, next.TLD, next.Label, next.Owner, next.ResolvedAddress, next.TargetType,
		nullTime(next.RegisteredAt), nullTime(next.ExpiresAt), next.Status, next.LastEventLedger, nullTx(next.LastEventTx),
		next.CreatedAt, next.UpdatedAt,
	); err != nil {
		return fmt.Errorf("upsert domain %s: %w", ev.Node, err)
	}

	details := ev.Details
	if details == "" {
		details = "{}"
	}
	_, err = dbTx.ExecContext(ctx, `
		INSERT INTO domain_events (
			node, name, event_type, owner, resolved_address, expires_at,
			transaction_hash, ledger_sequence, details, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (transaction_hash, node, event_type) DO NOTHING`,
		ev.Node, ev.Name, ev.EventType, nullStr(ev.Owner), nullStr(ev.ResolvedAddress),
		ev.ExpiresAt, ev.TransactionHash, ev.LedgerSequence, details, ev.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert domain event: %w", err)
	}
	return nil
}

const domainSelectCols = `
		node, name, tld, label, owner, resolved_address, target_type,
		registered_at, expires_at,
		status, last_event_ledger, COALESCE(last_event_tx, ''),
		created_at, updated_at`

func getDomainByNodeTx(ctx context.Context, dbTx *sql.Tx, node string) (*Domain, error) {
	row := dbTx.QueryRowContext(ctx,
		`SELECT `+domainSelectCols+` FROM domains WHERE node = $1 FOR UPDATE`, node)
	d, err := scanDomain(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

func scanDomain(row interface {
	Scan(dest ...interface{}) error
}) (*Domain, error) {
	var d Domain
	var registered, expires sql.NullTime
	err := row.Scan(
		&d.Node, &d.Name, &d.TLD, &d.Label, &d.Owner, &d.ResolvedAddress, &d.TargetType,
		&registered, &expires, &d.Status, &d.LastEventLedger, &d.LastEventTx,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if registered.Valid {
		d.RegisteredAt = registered.Time.UTC()
	}
	if expires.Valid {
		d.ExpiresAt = expires.Time.UTC()
	}
	return &d, nil
}

// DomainsIndexed reports whether any ledger has been ingested, which is the
// signal the read API uses for "not indexed yet" vs empty-but-ready.
func (s *PostgresStore) DomainsIndexed(ctx context.Context) (bool, error) {
	seq, err := s.GetLastIngestedLedger(ctx)
	if err != nil {
		return false, err
	}
	return seq > 0, nil
}

// GetDomainByName returns the current domain row for a fully-qualified name.
func (s *PostgresStore) GetDomainByName(ctx context.Context, name string) (*Domain, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+domainSelectCols+` FROM domains WHERE name = $1`, strings.ToLower(name))
	d, err := scanDomain(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// GetDomainsByAddress returns domains whose current resolved address matches.
func (s *PostgresStore) GetDomainsByAddress(ctx context.Context, address string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+domainSelectCols+`
		FROM domains
		WHERE resolved_address = $1 AND name <> ''
		ORDER BY name`, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDomains(rows)
}

// ListDomains pages named domain rows. status filters the computed status
// (active / expired / revoked). cursor is the last name from the previous page.
func (s *PostgresStore) ListDomains(ctx context.Context, status, cursor string, limit int) ([]Domain, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	q := `
		SELECT ` + domainSelectCols + `
		FROM domains
		WHERE name <> ''`
	args := []interface{}{}
	arg := 1
	if cursor != "" {
		q += fmt.Sprintf(" AND name > $%d", arg)
		args = append(args, cursor)
		arg++
	}
	switch status {
	case DomainStatusRevoked:
		q += fmt.Sprintf(" AND status = $%d", arg)
		args = append(args, DomainStatusRevoked)
		arg++
	case DomainStatusExpired:
		q += fmt.Sprintf(" AND status <> $%d AND expires_at IS NOT NULL AND expires_at <= NOW()", arg)
		args = append(args, DomainStatusRevoked)
		arg++
	case DomainStatusActive:
		q += fmt.Sprintf(" AND status <> $%d AND (expires_at IS NULL OR expires_at > NOW())", arg)
		args = append(args, DomainStatusRevoked)
		arg++
	}
	q += fmt.Sprintf(" ORDER BY name LIMIT $%d", arg)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDomains(rows)
}

// GetDomainEvents returns history for a domain, identified by name.
func (s *PostgresStore) GetDomainEvents(ctx context.Context, name string, limit int) ([]DomainEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	d, err := s.GetDomainByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT node, name, event_type, COALESCE(owner, ''), COALESCE(resolved_address, ''),
			expires_at, transaction_hash, ledger_sequence, COALESCE(details::text, '{}'), created_at
		FROM domain_events
		WHERE node = $1
		ORDER BY ledger_sequence ASC, created_at ASC
		LIMIT $2`, d.Node, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DomainEvent
	for rows.Next() {
		var e DomainEvent
		var expires sql.NullTime
		if err := rows.Scan(&e.Node, &e.Name, &e.EventType, &e.Owner, &e.ResolvedAddress,
			&expires, &e.TransactionHash, &e.LedgerSequence, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		if expires.Valid {
			t := expires.Time.UTC()
			e.ExpiresAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanDomains(rows *sql.Rows) ([]Domain, error) {
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullTx(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
