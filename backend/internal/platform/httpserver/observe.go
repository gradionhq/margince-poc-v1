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
	"runtime"
	"sort"
	"strings"
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
// mounted inside Correlate. The path is the request path, not the route
// pattern — the access log answers "what did clients ask", the metrics
// answer "how did routes behave".
//
// capabilityPrefixes name the route prefixes whose NEXT path segment is a
// bearer credential rather than an identifier: the no-login preference
// centre carries its capability token in the URL, and that token resolves
// straight to a (workspace, person) consent state. Everyone who can read
// the log — an ops dashboard, a shipped aggregator, a third-party log SaaS,
// a support engineer — would otherwise be holding a working credential, so
// the segment is replaced before the line is written. The composition layer
// names them for the same reason it names every other thing this chassis
// observes: the chassis owns the mechanism, never the route vocabulary.
func AccessLog(log *slog.Logger, next http.Handler, capabilityPrefixes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", RedactCapabilitySegment(r.URL.Path, capabilityPrefixes...),
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// redactedSegment stands in for a capability the log must not carry.
const redactedSegment = "[redacted]"

// RedactCapabilitySegment replaces the ONE path segment that follows a
// capability prefix and keeps everything around it, so the line still says
// which route was asked for and what trailed the credential ("…/unsubscribe")
// without saying which credential was presented. A path that reaches no
// prefix, or that stops at the prefix with no segment after it, is returned
// unchanged — there is nothing there to leak.
func RedactCapabilitySegment(path string, capabilityPrefixes ...string) string {
	for _, prefix := range capabilityPrefixes {
		if prefix == "" || !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(path, prefix)
		if rest == "" {
			continue
		}
		if _, trailing, found := strings.Cut(rest, "/"); found {
			return prefix + redactedSegment + "/" + trailing
		}
		return prefix + redactedSegment
	}
	return path
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

// Unwrap exposes the wrapped writer so http.NewResponseController can reach
// the real connection: SSE streams and long-running tool calls need
// SetWriteDeadline and Flush, and an embedded-only wrapper silently swallows
// both.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

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
//
// aiState rides the 200 body as a visibility line — "configured",
// "unconfigured", or "fake" — never a gate: an AI-unconfigured
// deployment is a legitimate, ready deployment (ai-operational-spec
// §2), so it is reported alongside "ready", not checked as a
// ReadyCheck that could turn it into a 503.
//
// embedState is the same shape of visibility line for the search
// module's embed store binding (Task 17): "active", "needs_reindex", or
// "reembedding". It is called once, after every check has passed, with
// the same deadline-bound ctx the checks ran under; a nil embedState (a
// role that wires no embed lane) and one whose own marker-read failed
// and already turned that into "unknown" both render identically —
// Readyz never inspects why, only what it's handed. Like the AI line,
// this NEVER gates: the embed store still serves N+1 reads correctly
// under a stale binding, so a drifted or unreadable marker is
// informational, not a reason to stop routing traffic here.
func Readyz(aiState string, embedState func(context.Context) string, checks ...ReadyCheck) http.HandlerFunc {
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
		embed := "unknown"
		if embedState != nil {
			embed = embedState(ctx)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ready\nai: %s\nembed: %s\n", aiState, embed)
	}
}

// OverlayMetrics is the overlay sync-health section /metrics adds when
// this role has an incumbent connection surface wired (design.md §4.7):
// per-object-class source lag (the fleet-wide worst-case staleness),
// plus the inbound sync-rate and conflict-rate counters. Nil means this
// role never wired an overlay keyvault (WithKeyvault absent) — the same
// "declared or absent" posture backlog/published already establish for
// the outbox relay, never a silent zero-valued section.
type OverlayMetrics struct {
	// SourceLag answers, per canonical object class, now minus the
	// oldest last_synced_at seen anywhere in the fleet for that class.
	SourceLag func(context.Context) (map[string]time.Duration, error)
	// SyncedTotal answers the process's inbound mirror-sync counter.
	SyncedTotal func() uint64
	// ConflictTotal answers the process's mirror.conflict counter.
	ConflictTotal func() uint64
	// DeletedTotal answers the process's mirror.deleted counter (records
	// purged from the mirror by the continuous-sync deletion feed).
	DeletedTotal func() uint64
}

// Metrics serves the Prometheus text exposition format, hand-rolled: at
// PoC stage the handful of gauges below does not justify the
// client_golang dependency tree, and the text format is a stable,
// trivially-emitted contract. backlog and published are injected by the
// composition layer (platform/events owns the outbox SQL). extra renders
// any additional counter families a process role wires in (e.g. the AI
// router's call metrics) directly after the pool gauges; nil means the
// role wired none. overlay is injected the same way (nil for a role with
// no overlay surface wired).
//
// jobStats renders the job-runtime section. Unlike extra it takes a
// context, because it queries at scrape time, and it is handed THIS
// handler's deadline-bound ctx rather than the request's — an unbounded
// job read is the one thing the 2s budget below exists to stop.
//
// A section that could not MEASURE writes nothing and returns nil, so the
// rest of the exposition still serves; the error return is reserved for a
// refused WRITE, which in practice means the scraper's connection is
// already gone. The handler logs it and stops rather than writing further
// sections into a socket that is not there.
//
// The parameter list has reached the point where a further section should
// become a struct rather than a seventh argument.
func Metrics(pool *pgxpool.Pool, backlog func(context.Context) (int64, error), published func() uint64, extra func(io.Writer), jobStats func(context.Context, io.Writer) error, overlay *OverlayMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// Always first, and never injected: this section measures the
		// PROCESS answering the scrape, so it is the one part of the
		// exposition that means something different on every target and
		// cannot be assembled anywhere but here.
		writeRuntimeMetrics(w)

		// The backlog is a fleet-wide reading of a shared table, so a role
		// that would only duplicate another target's copy of it passes nil
		// rather than querying — the same "declared or absent" posture the
		// sections below take, and the reason a FAILED read writes nothing:
		// a gauge reporting rows it did not count reads as a drained outbox.
		if backlog != nil {
			if n, err := backlog(ctx); err == nil {
				_, _ = fmt.Fprintf(w, "# HELP margince_outbox_unpublished Committed outbox rows the relay has not shipped yet.\n")
				_, _ = fmt.Fprintf(w, "# TYPE margince_outbox_unpublished gauge\n")
				_, _ = fmt.Fprintf(w, "margince_outbox_unpublished %d\n", n)
			} else {
				slog.ErrorContext(r.Context(), "metrics: outbox backlog query failed", "err", err)
			}
		}

		_, _ = fmt.Fprintf(w, "# HELP margince_relay_published_total Outbox rows shipped to the bus since process start.\n")
		_, _ = fmt.Fprintf(w, "# TYPE margince_relay_published_total counter\n")
		_, _ = fmt.Fprintf(w, "margince_relay_published_total %d\n", published())

		// Omitted rather than zeroed when no pool was injected — the same
		// "declared or absent" posture every other section here takes, and
		// the reason a failed backlog read above writes nothing: a gauge
		// reporting connections it did not measure reads as an idle pool.
		if pool != nil {
			stat := pool.Stat()
			_, _ = fmt.Fprintf(w, "# HELP margince_pgxpool_conns Connection pool state by class.\n")
			_, _ = fmt.Fprintf(w, "# TYPE margince_pgxpool_conns gauge\n")
			_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"acquired\"} %d\n", stat.AcquiredConns())
			_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"idle\"} %d\n", stat.IdleConns())
			_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"total\"} %d\n", stat.TotalConns())
			_, _ = fmt.Fprintf(w, "margince_pgxpool_conns{state=\"max\"} %d\n", stat.MaxConns())
		}

		if extra != nil {
			extra(w)
		}
		if jobStats != nil {
			if err := jobStats(ctx, w); err != nil {
				slog.ErrorContext(r.Context(), "metrics: writing the job section failed", "err", err)
				return
			}
		}
		if overlay != nil {
			writeOverlayMetrics(r.Context(), w, overlay)
		}
	}
}

// writeRuntimeMetrics renders what the SCRAPED PROCESS is doing, as opposed
// to what the installation is doing. Every other section here is a reading of
// shared state — the outbox table, the job table, the mirror — which any role
// with a pool answers identically, so a wedged replica is arithmetically
// invisible in them: the fleet-wide numbers are the same whichever process
// served the scrape.
//
// These four are not. They are read out of THIS runtime, so they differ per
// target and an operator can tell which process stopped working rather than
// only that work stopped. That is the whole reason the worker role serves a
// listener at all: it does the work and, until it did, published nothing about
// itself.
//
// Named margince_process_* rather than Prometheus' conventional go_* because
// this exposition is hand-rolled: if client_golang is ever adopted it brings
// its own go_* collector, and two definitions of one series name is a worse
// outcome than a prefix that says who wrote it.
func writeRuntimeMetrics(w io.Writer) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	_, _ = fmt.Fprintf(w, "# HELP margince_process_goroutines Goroutines running in the scraped process.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_process_goroutines gauge\n")
	_, _ = fmt.Fprintf(w, "margince_process_goroutines %d\n", runtime.NumGoroutine())

	_, _ = fmt.Fprintf(w, "# HELP margince_process_heap_bytes Heap bytes allocated and in use by the scraped process.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_process_heap_bytes gauge\n")
	_, _ = fmt.Fprintf(w, "margince_process_heap_bytes %d\n", mem.HeapAlloc)

	_, _ = fmt.Fprintf(w, "# HELP margince_process_heap_sys_bytes Heap bytes obtained from the OS by the scraped process -- with heap_bytes, whether memory is in use or merely held.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_process_heap_sys_bytes gauge\n")
	_, _ = fmt.Fprintf(w, "margince_process_heap_sys_bytes %d\n", mem.HeapSys)

	_, _ = fmt.Fprintf(w, "# HELP margince_process_gc_cycles_total Completed GC cycles since the scraped process started.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_process_gc_cycles_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_process_gc_cycles_total %d\n", mem.NumGC)
}

// writeOverlayMetrics renders the overlay sync-health section — split
// out of Metrics so that function's own top-to-bottom read stays one
// section per line, not buried behind a nested nil-check.
func writeOverlayMetrics(ctx context.Context, w http.ResponseWriter, overlay *OverlayMetrics) {
	if lag, err := overlay.SourceLag(ctx); err == nil {
		_, _ = fmt.Fprintf(w, "# HELP margince_overlay_source_lag_seconds Seconds since the mirror's oldest last sync per object class (worst case across the fleet).\n")
		_, _ = fmt.Fprintf(w, "# TYPE margince_overlay_source_lag_seconds gauge\n")
		for _, objectClass := range sortedKeys(lag) {
			_, _ = fmt.Fprintf(w, "margince_overlay_source_lag_seconds{object_class=%q} %.0f\n", objectClass, lag[objectClass].Seconds())
		}
	} else {
		slog.Error("metrics: overlay source-lag query failed", "err", err)
	}

	_, _ = fmt.Fprintf(w, "# HELP margince_overlay_mirror_synced_total Mirror rows ingested (push+pull) since process start.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_overlay_mirror_synced_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_overlay_mirror_synced_total %d\n", overlay.SyncedTotal())

	_, _ = fmt.Fprintf(w, "# HELP margince_overlay_mirror_conflict_total mirror.conflict events emitted (incumbent-wins divergence) since process start.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_overlay_mirror_conflict_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_overlay_mirror_conflict_total %d\n", overlay.ConflictTotal())

	_, _ = fmt.Fprintf(w, "# HELP margince_overlay_mirror_deleted_total mirror.deleted events emitted (incumbent-deleted records purged from the mirror) since process start.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_overlay_mirror_deleted_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_overlay_mirror_deleted_total %d\n", overlay.DeletedTotal())
}

// sortedKeys answers lag's object-class keys in a stable order — a
// Prometheus scrape target's series order should not flap between
// scrapes for no reason, and map iteration order is not stable.
func sortedKeys(lag map[string]time.Duration) []string {
	keys := make([]string, 0, len(lag))
	for k := range lag {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
