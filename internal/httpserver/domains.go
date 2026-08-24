package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

// DomainReader is the store subset the domains read API needs.
type DomainReader interface {
	DomainsIndexed(ctx context.Context) (bool, error)
	GetDomainByName(ctx context.Context, name string) (*store.Domain, error)
	GetDomainsByAddress(ctx context.Context, address string) ([]store.Domain, error)
	ListDomains(ctx context.Context, status, cursor string, limit int) ([]store.Domain, error)
	GetDomainEvents(ctx context.Context, name string, limit int) ([]store.DomainEvent, error)
}

func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	indexed, err := s.domainsIndexed(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false})
		return
	}

	address := r.URL.Query().Get("address")
	if address != "" {
		s.handleReverseLookup(w, r, indexed, address)
		return
	}
	s.handleListDomains(w, r, indexed)
}

func (s *Server) handleDomainByName(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := strings.ToLower(r.PathValue("name"))

	indexed, err := s.domainsIndexed(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false})
		return
	}
	if !indexed || s.domains == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false, Domain: nil})
		return
	}

	d, err := s.domains.GetDomainByName(r.Context(), name)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: true})
		return
	}
	env := domainsEnvelope{Indexed: true}
	if d != nil {
		rec := toDomainRecord(*d, time.Now().UTC())
		env.Domain = &rec
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(env)
}

func (s *Server) handleDomainEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := strings.ToLower(r.PathValue("name"))

	indexed, err := s.domainsIndexed(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false})
		return
	}
	if !indexed || s.domains == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false, Events: []domainEventRecord{}})
		return
	}

	events, err := s.domains.GetDomainEvents(r.Context(), name, queryLimit(r))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: true})
		return
	}
	recs := make([]domainEventRecord, 0, len(events))
	for _, e := range events {
		recs = append(recs, toEventRecord(e))
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: true, Events: recs})
}

func (s *Server) handleReverseLookup(w http.ResponseWriter, r *http.Request, indexed bool, address string) {
	if !indexed || s.domains == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false, Domains: []domainRecord{}})
		return
	}
	all, err := s.domains.GetDomainsByAddress(r.Context(), address)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: true})
		return
	}
	now := time.Now().UTC()
	activeOnly := r.URL.Query().Get("status") != "all"
	recs := make([]domainRecord, 0, len(all))
	for _, d := range all {
		rec := toDomainRecord(d, now)
		if activeOnly && rec.Status != store.DomainStatusActive {
			continue
		}
		recs = append(recs, rec)
	}
	env := domainsEnvelope{Indexed: true, Domains: recs}
	if len(recs) > 0 {
		env.Domain = &recs[0]
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(env)
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request, indexed bool) {
	if !indexed || s.domains == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: false, Domains: []domainRecord{}})
		return
	}
	status := r.URL.Query().Get("status")
	cursor := r.URL.Query().Get("cursor")
	list, err := s.domains.ListDomains(r.Context(), status, cursor, queryLimit(r))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(domainsEnvelope{Indexed: true})
		return
	}
	now := time.Now().UTC()
	recs := make([]domainRecord, 0, len(list))
	for _, d := range list {
		recs = append(recs, toDomainRecord(d, now))
	}
	env := domainsEnvelope{Indexed: true, Domains: recs}
	if len(recs) > 0 {
		env.Cursor = recs[len(recs)-1].Name
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(env)
}

func (s *Server) domainsIndexed(r *http.Request) (bool, error) {
	if s.domains == nil {
		return false, nil
	}
	return s.domains.DomainsIndexed(r.Context())
}

func queryLimit(r *http.Request) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 50
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 50
	}
	return n
}

type domainsEnvelope struct {
	Indexed bool                `json:"indexed"`
	Domain  *domainRecord       `json:"domain"`
	Domains []domainRecord      `json:"domains,omitempty"`
	Events  []domainEventRecord `json:"events,omitempty"`
	Cursor  string              `json:"cursor,omitempty"`
}

type domainRecord struct {
	Name            string `json:"name"`
	Owner           string `json:"owner"`
	Address         string `json:"address"`
	TargetType      string `json:"target_type"`
	RegisteredAt    string `json:"registered_at"`
	ExpiresAt       string `json:"expires_at"`
	Status          string `json:"status"`
	LastEventLedger uint32 `json:"last_event_ledger"`
}

type domainEventRecord struct {
	Name            string `json:"name"`
	EventType       string `json:"event_type"`
	Owner           string `json:"owner,omitempty"`
	Address         string `json:"address,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	TransactionHash string `json:"transaction_hash"`
	LedgerSequence  uint32 `json:"ledger_sequence"`
	CreatedAt       string `json:"created_at"`
}

func toDomainRecord(d store.Domain, now time.Time) domainRecord {
	return domainRecord{
		Name:            d.Name,
		Owner:           d.Owner,
		Address:         d.ResolvedAddress,
		TargetType:      d.TargetType,
		RegisteredAt:    formatTime(d.RegisteredAt),
		ExpiresAt:       formatTime(d.ExpiresAt),
		Status:          d.EffectiveStatus(now),
		LastEventLedger: d.LastEventLedger,
	}
}

func toEventRecord(e store.DomainEvent) domainEventRecord {
	rec := domainEventRecord{
		Name:            e.Name,
		EventType:       e.EventType,
		Owner:           e.Owner,
		Address:         e.ResolvedAddress,
		TransactionHash: e.TransactionHash,
		LedgerSequence:  e.LedgerSequence,
		CreatedAt:       formatTime(e.CreatedAt),
	}
	if e.ExpiresAt != nil {
		rec.ExpiresAt = formatTime(*e.ExpiresAt)
	}
	return rec
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
