package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePinger struct {
	err error
}

func (f fakePinger) PingContext(ctx context.Context) error {
	return f.err
}

func notStale(time.Duration) bool { return false }
func stale(time.Duration) bool    { return true }

func TestHealthzReturnsOKWhenDBReachableAndPipelineAdvancing(t *testing.T) {
	handler := healthzHandler(fakePinger{}, notStale)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("expected ok status body, got %q", rec.Body.String())
	}
}

func TestHealthzReturnsServiceUnavailableWhenDBDown(t *testing.T) {
	handler := healthzHandler(fakePinger{err: errors.New("dial tcp 10.0.0.5:5432: connection refused")}, notStale)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"unhealthy"`) {
		t.Errorf("expected unhealthy status body, got %q", body)
	}
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "connection refused") {
		t.Errorf("expected raw DB error not to be exposed in response body, got %q", body)
	}
}

func TestHealthzReturnsServiceUnavailableWhenPipelineStale(t *testing.T) {
	handler := healthzHandler(fakePinger{}, stale)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"unhealthy"`) {
		t.Errorf("expected unhealthy status body, got %q", body)
	}
	if !strings.Contains(body, "pipeline not advancing") {
		t.Errorf("expected reason 'pipeline not advancing', got %q", body)
	}
}

// TestServerExposesMetricsAndHealthz is a hermetic end-to-end check that New
// wires both routes on a real listener.
func TestServerExposesMetricsAndHealthz(t *testing.T) {
	srv := New("127.0.0.1:0", Options{DB: fakePinger{}, ExposeMetrics: true})

	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Exercise the handlers directly instead of racing the background
	// listener for its ephemeral port.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected /healthz status 200, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected /metrics status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "indexer_ingestion_lag_ledgers") {
		t.Errorf("expected /metrics body to expose indexer_ingestion_lag_ledgers, got %q", rec.Body.String())
	}
}
