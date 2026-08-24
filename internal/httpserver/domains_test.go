package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

type fakeDomains struct {
	indexed bool
	byName  map[string]*store.Domain
	byAddr  map[string][]store.Domain
	list    []store.Domain
	events  map[string][]store.DomainEvent
}

func (f fakeDomains) DomainsIndexed(ctx context.Context) (bool, error) {
	return f.indexed, nil
}

func (f fakeDomains) GetDomainByName(ctx context.Context, name string) (*store.Domain, error) {
	return f.byName[name], nil
}

func (f fakeDomains) GetDomainsByAddress(ctx context.Context, address string) ([]store.Domain, error) {
	return f.byAddr[address], nil
}

func (f fakeDomains) ListDomains(ctx context.Context, status, cursor string, limit int) ([]store.Domain, error) {
	return f.list, nil
}

func (f fakeDomains) GetDomainEvents(ctx context.Context, name string, limit int) ([]store.DomainEvent, error) {
	return f.events[name], nil
}

func TestDomainsAPINotIndexedYet(t *testing.T) {
	srv := New("127.0.0.1:0", Options{DB: fakePinger{}})

	paths := []string{
		"/v1/domains",
		"/v1/domains?address=GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		"/v1/domains/stellar.xlm",
		"/v1/domains/stellar.xlm/events",
	}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		var env domainsEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		if env.Indexed {
			t.Errorf("%s: indexed = true, want false", path)
		}
	}
}

func TestDomainsAPIResolveAndReverseLookup(t *testing.T) {
	now := time.Now().UTC().Add(24 * time.Hour)
	d := store.Domain{
		Name:            "stellar.xlm",
		Owner:           "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO",
		ResolvedAddress: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		TargetType:      store.DomainTargetAccount,
		RegisteredAt:    time.Unix(1700000000, 0).UTC(),
		ExpiresAt:       now,
		Status:          store.DomainStatusActive,
		LastEventLedger: 42,
	}
	srv := New("127.0.0.1:0", Options{DB: fakePinger{}})
	srv.SetDomainReader(fakeDomains{
		indexed: true,
		byName:  map[string]*store.Domain{"stellar.xlm": &d},
		byAddr:  map[string][]store.Domain{d.ResolvedAddress: {d}},
		list:    []store.Domain{d},
		events: map[string][]store.DomainEvent{
			"stellar.xlm": {{
				Name: "stellar.xlm", EventType: store.DomainEventRegister,
				TransactionHash: "aa", LedgerSequence: 42, CreatedAt: d.RegisteredAt,
			}},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/domains/stellar.xlm", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d", rec.Code)
	}
	var env domainsEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Indexed || env.Domain == nil || env.Domain.Name != "stellar.xlm" {
		t.Fatalf("resolve envelope = %+v", env)
	}
	if env.Domain.Status != store.DomainStatusActive {
		t.Errorf("status = %s, want active", env.Domain.Status)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/domains?address="+d.ResolvedAddress, nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Domain == nil || env.Domain.Name != "stellar.xlm" || len(env.Domains) != 1 {
		t.Fatalf("reverse lookup envelope = %+v", env)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/domains/stellar.xlm/events", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Events) != 1 || env.Events[0].EventType != store.DomainEventRegister {
		t.Fatalf("events envelope = %+v", env)
	}
}

func TestDomainsAPIMissingNameStillIndexed(t *testing.T) {
	srv := New("127.0.0.1:0", Options{DB: fakePinger{}})
	srv.SetDomainReader(fakeDomains{indexed: true, byName: map[string]*store.Domain{}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/domains/missing.xlm", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var env domainsEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Indexed {
		t.Error("indexed should be true when ingestion has run")
	}
	if env.Domain != nil {
		t.Errorf("domain = %+v, want null", env.Domain)
	}
}
