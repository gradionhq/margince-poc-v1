// Package httpserver is the HTTP chassis (ADR-0054 §5): the middleware
// every process role's HTTP surface rides — correlation scope, security
// headers, panic recovery, the health probe. Platform owns no domain:
// route assembly and module wiring live in the composition layer.
package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"

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
// and referrer leakage. The CSP pins scripts to the embedded SPA; the
// fonts.g* entries exist only because index.html loads the design
// language's typefaces from Google Fonts.
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
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
