// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package httpserver is the HTTP chassis (ADR-0054 §5): the middleware
// every process role's HTTP surface rides — correlation scope, security
// headers, panic recovery, the health probe. Platform owns no domain:
// route assembly and module wiring live in the composition layer.
package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Healthz answers the unauthenticated liveness probe.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// SecureHeaders sets the browser-facing response headers on everything —
// UI and API alike. SameSite=Strict on the session cookie covers CSRF;
// these close what it does not: framing (clickjacking), MIME sniffing,
// and referrer leakage. The CSP is same-origin only: this backend serves
// no first-party HTML at all — every response from here is JSON — so
// these directives bind API responses rather than a page. style-src
// keeps 'unsafe-inline' regardless: loosening it is a CSP posture
// decision for whatever renders HTML behind this origin, not a
// side effect of where any one screen happens to live.
// LimitBodies caps every request body at httperr.MaxBodyBytes so no
// handler — including ones decoding r.Body directly — can be fed an
// unbounded payload. Reads past the cap fail with http.MaxBytesError.
func LimitBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, httperr.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// HSTS: the app is only ever reached through a TLS-terminating
		// front end (cookies are Secure), so pin the browser to HTTPS for
		// two years and forbid a downgrade on the next visit. A browser
		// ignores it on the plain-HTTP hop, so it is safe to set always.
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		next.ServeHTTP(w, r)
	})
}

// RequestOrigin reconstructs the externally visible scheme+host — the
// origin a client outside the fronting proxy actually sees. TLS
// terminates ahead of the chassis in production, so the forwarded proto
// wins when present. Consumers: the OAuth discovery documents (RFC 8414 /
// RFC 9728) and the 401 challenge's resource_metadata pointer, both of
// which must name an origin the client can dereference, not the internal
// one the process is bound to.
func RequestOrigin(r *http.Request) string {
	const (
		secure   = "https"
		insecure = "http"
	)
	// Only the two legitimate values are honored; anything else in the
	// forwarded header is attacker noise. Host itself must be sanitized
	// by the fronting proxy — the metadata documents say so.
	scheme := secure
	switch forwardedProto(r) {
	case secure:
	case insecure:
		scheme = insecure
	default:
		if r.TLS == nil {
			scheme = insecure
		}
	}
	return scheme + "://" + r.Host
}

// forwardedProto reads the scheme the OUTERMOST proxy saw out of
// X-Forwarded-Proto. Each hop APPENDS to the header, so a chain arrives as
// "https, http": the client-facing scheme is the FIRST element, and every
// later one describes an internal hop.
//
// Taking the whole value would match neither case arm above and fall through
// to r.TLS, which is nil behind a terminating proxy — so a two-hop deployment
// would advertise an http:// origin in the OAuth discovery documents and in
// the protected-resource URL, which is the one thing they exist to state
// correctly. The value is also trimmed and lowercased because the header is a
// token, not a literal.
func forwardedProto(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.ToLower(strings.TrimSpace(proto))
}

// BearerToken reads the credential out of an Authorization header value, and
// it is the ONE reading of it in this process — /v1, the MCP transport and the
// provider push webhooks all come here. Two spellings meant the same
// credential authenticated on one transport and 401'd on the other, which a
// client experiences as an infinite re-authorization loop against a token that
// is perfectly valid.
//
// The scheme name is matched case-INSENSITIVELY (RFC 7235 §2.1 makes it a
// case-insensitive token; `bearer` is a shape real clients send), and the
// prefix must actually be present: a TrimPrefix-style read would accept a
// header that never carried it, turning an unrelated credential — or a Basic
// header — into a token lookup. An empty credential after the scheme reads as
// no credential at all, so a caller never looks up "".
func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// Correlate opens the per-request trace scope: one freshly minted
// correlation_id groups every event the request's writes emit (events.md
// §2). Minted server-side, never taken from a request header — a client
// that could set it could stitch itself into another tenant's story.
func Correlate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := principal.WithCorrelationID(r.Context(), ids.NewV7())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RecoverPanics is the outermost guard: a panicking handler answers an
// opaque 500 instead of killing the connection (and taking pre-Go-1.21
// servers down with it). The panic value and stack are logged — the one
// place observability matters most must never be a silent 500.
func RecoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(r.Context(), "handler panic",
					"panic", rec, "method", r.Method, "path", r.URL.Path,
					"stack", string(debug.Stack()))
				httperr.Write(w, r, &httperr.DetailedError{
					Status: http.StatusInternalServerError, Code: "internal",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ClientIP is the ONE client-IP throttle key in this process — the login and
// password-reset limits in identity, the anonymous booking and preference
// paths, and every connector edge. It is here rather than in either caller
// because two copies meant a deployment could harden one edge and leave the
// other keyed differently, and nothing would say so.
//
// RemoteAddr is the DIRECT peer. A raw X-Forwarded-For is attacker-chosen and
// deliberately never read: a deployment fronted by a proxy terminates rate
// limiting there, or extends this to a *trusted* Forwarded header — never
// trusted blindly.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
