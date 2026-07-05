// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

// The chassis's observability surface: correlation-aware logging, the
// access log, the readiness probe, and the metrics endpoint. Everything
// here is transport plumbing — what to check and what to count is
// injected by the composition layer.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LogHandler builds the slog backend from the operator's --log-level and
// --log-format values. It lives here so every process role shares one
// level/format vocabulary and one "a typo is a boot error" rule.
func LogHandler(w io.Writer, level, format string) (slog.Handler, error) {
	var lv slog.LevelVar
	switch level {
	case "debug":
		lv.Set(slog.LevelDebug)
	case "info":
		lv.Set(slog.LevelInfo)
	case "warn":
		lv.Set(slog.LevelWarn)
	case "error":
		lv.Set(slog.LevelError)
	default:
		return nil, fmt.Errorf("--log-level %q: want debug, info, warn, or error", level)
	}
	opts := &slog.HandlerOptions{Level: &lv}
	switch format {
	case "text":
		return slog.NewTextHandler(w, opts), nil
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("--log-format %q: want text or json", format)
	}
}

// WithCorrelation wraps a slog.Handler so every record logged through a
// *Context method carries the request's correlation_id — the same id the
// Correlate middleware minted and every emitted event's trace links, so
// one grep joins log lines, audit rows, and bus events.
func WithCorrelation(h slog.Handler) slog.Handler {
	return &correlationHandler{inner: h}
}

type correlationHandler struct{ inner slog.Handler }

func (h *correlationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *correlationHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id, ok := principal.CorrelationID(ctx); ok {
		rec.AddAttrs(slog.String("correlation_id", id.String()))
	}
	return h.inner.Handle(ctx, rec)
}

func (h *correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{inner: h.inner.WithGroup(name)}
}

// AccessLog logs one line per request (method, path, status, duration);
// the correlation_id rides in via the ctx-aware handler, so it must be
// mounted inside Correlate. The path is the raw request path, not the
// route pattern — the access log answers "what did clients ask", the
// metrics answer "how did routes behave".
func AccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// ReadyCheck is one named dependency probe for /readyz.
type ReadyCheck struct {
	Name  string
	Check func(context.Context) error
}

// Readyz answers the readiness probe: every injected dependency check
// must pass within a short deadline. Distinct from /healthz, which stays
// a dumb liveness answer — a wedged database must fail readiness (stop
// routing traffic here) without failing liveness (don't restart-loop the
// process the database outage didn't break).
func Readyz(checks ...ReadyCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for _, c := range checks {
			if err := c.Check(ctx); err != nil {
				// The dependency name is enough for the orchestrator; the
				// error text is for the server log, not the probe body.
				slog.ErrorContext(r.Context(), "readiness check failed", "dependency", c.Name, "err", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, "unready: %s\n", c.Name)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}
}

// Metrics serves the Prometheus text exposition format, hand-rolled: at
// PoC stage the handful of gauges below does not justify the
// client_golang dependency tree, and the text format is a stable,
// trivially-emitted contract. backlog and published are injected by the
// composition layer (platform/events owns the outbox SQL).
func Metrics(pool *pgxpool.Pool, backlog func(context.Context) (int64, error), published func() uint64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		if n, err := backlog(ctx); err == nil {
			_, _ = fmt.Fprintf(w, "# HELP margince_outbox_unpublished Committed outbox rows the relay has not shipped yet.\n")
			_, _ = fmt.Fprintf(w, "# TYPE margince_outbox_unpublished gauge\n")
			_, _ = fmt.Fprintf(w, "margince_outbox_unpublished %d\n", n)
		} else {
			slog.ErrorContext(r.Context(), "metrics: outbox backlog query failed", "err", err)
		}

		_, _ = fmt.Fprintf(w, "# HELP margince_relay_published_total Outbox rows shipped to the bus since process start.\n")
		_, _ = fmt.Fprintf(w, "# TYPE margince_relay_published_total counter\n")
		_, _ = fmt.Fprintf(w, "margince_relay_published_total %d\n", published())

		stat := pool.Stat()
		_, _ = fmt.Fprintf(w, "# HELP margince_pgxpool_conns Connection pool state by class.\n")
		_, _ = fmt.Fprintf(w, "# TYPE margince_pgxpool_conns gauge\n")
		_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"acquired\"} %d\n", stat.AcquiredConns())
		_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"idle\"} %d\n", stat.IdleConns())
		_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"total\"} %d\n", stat.TotalConns())
		_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"max\"} %d\n", stat.MaxConns())
	}
}
