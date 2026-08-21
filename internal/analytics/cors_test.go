package analytics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// serveWithOrigins issues a request through the real route table so the tests
// exercise the same middleware wiring the server uses.
func serveWithOrigins(t *testing.T, allowed []string, method, target, origin string) *httptest.ResponseRecorder {
	t.Helper()

	h := NewHandler(&fakeReader{}, allowed)
	h.now = func() time.Time { return frozenNow }

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(method, target, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const seriesTarget = "/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly" +
	"&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z"

// The explorer reaches these endpoints from the browser through
// NEXT_PUBLIC_INDEXER_URL, so a response without this header is unreadable to
// it even though curl sees a perfectly good 200.
func TestResponsesCarryCORSHeadersForBrowsers(t *testing.T) {
	rec := serveWithOrigins(t, []string{AllowAllOrigins}, http.MethodGet, seriesTarget, "https://explorer.example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != AllowAllOrigins {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, AllowAllOrigins)
	}
}

// Go's method-based route patterns do not match OPTIONS, so without an explicit
// preflight route a browser gets 405 and never sends the real request.
func TestPreflightIsAnswered(t *testing.T) {
	rec := serveWithOrigins(t, []string{AllowAllOrigins}, http.MethodOptions,
		"/api/v1/analytics/timeseries", "https://explorer.example.com")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("preflight must advertise the allowed methods")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != AllowAllOrigins {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, AllowAllOrigins)
	}
}

func TestAllowListEchoesOnlyPermittedOrigins(t *testing.T) {
	allowed := []string{"https://explorer.example.com", "https://staging.example.com"}

	rec := serveWithOrigins(t, allowed, http.MethodGet, seriesTarget, "https://staging.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://staging.example.com" {
		t.Errorf("permitted origin: header = %q, want it echoed back", got)
	}
	// A response that differs per origin must not be cached as if it did not.
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}

	rec = serveWithOrigins(t, allowed, http.MethodGet, seriesTarget, "https://evil.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin was granted access: %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d — the request is still answered, the browser enforces the policy", rec.Code)
	}
}

func TestEmptyAllowListDisablesCrossOriginAccess(t *testing.T) {
	rec := serveWithOrigins(t, nil, http.MethodGet, seriesTarget, "https://explorer.example.com")

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want no header", got)
	}
}
