package analytics

import (
	"net/http"
	"slices"
	"strings"
)

// preflightMaxAge is how long a browser may cache a preflight result, in
// seconds. The policy is static, so a long cache costs nothing.
const preflightMaxAge = "86400"

// AllowAllOrigins is the wildcard accepted in an allow-list. It is a reasonable
// default for this API: the data is public, the endpoints are read-only, and no
// credentials or cookies are involved.
const AllowAllOrigins = "*"

// withCORS answers cross-origin requests from the browser. The explorer reaches
// these endpoints through NEXT_PUBLIC_INDEXER_URL, which means the fetch runs in
// the page and is cross-origin by construction: without these headers every
// dashboard fails while curl succeeds, so the gap does not show up in terminal
// testing.
func (h *Handler) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.setCORSHeaders(w, r.Header.Get("Origin"))
		next.ServeHTTP(w, r)
	})
}

// handlePreflight answers the OPTIONS request a browser sends before a
// non-simple cross-origin fetch. Go's method-based route patterns do not match
// OPTIONS, so without an explicit route these would be rejected as 405.
func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	h.setCORSHeaders(w, r.Header.Get("Origin"))
	w.WriteHeader(http.StatusNoContent)
}

// setCORSHeaders grants access to origin when the allow-list permits it. An
// origin that is not allowed simply gets no header, which the browser turns
// into a CORS error — the request itself is still answered normally, since
// these endpoints expose nothing that needs protecting.
func (h *Handler) setCORSHeaders(w http.ResponseWriter, origin string) {
	allowed, ok := h.resolveOrigin(origin)

	// Vary is set whether or not access is granted, and before the early
	// return: with a per-origin allow-list the response differs by origin even
	// when the difference is the absence of the header, and a shared cache that
	// did not key on it could hand an allowed origin a denied response.
	if !slices.Contains(h.allowedOrigins, AllowAllOrigins) {
		w.Header().Add("Vary", "Origin")
	}
	if !ok {
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", allowed)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
	w.Header().Set("Access-Control-Max-Age", preflightMaxAge)
}

// resolveOrigin reports the value to echo back for the requesting origin.
func (h *Handler) resolveOrigin(origin string) (string, bool) {
	if len(h.allowedOrigins) == 0 {
		return "", false
	}
	if slices.Contains(h.allowedOrigins, AllowAllOrigins) {
		return AllowAllOrigins, true
	}
	if origin == "" {
		return "", false
	}
	if slices.ContainsFunc(h.allowedOrigins, func(a string) bool {
		return strings.EqualFold(a, origin)
	}) {
		return origin, true
	}
	return "", false
}
