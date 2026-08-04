// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The worker role's operator surface: /healthz, /readyz and /metrics on a
// listener of their own (OPS-MET-8's "every service").
//
// The worker is where the dispatchers actually run, and until this file it
// published nothing at all. Every job-runtime gauge is served by cmd/api,
// derived from river_job at request time, and that stays true — a job-table
// projection is fleet-wide by construction and having two roles answer it
// would mean two sources for one number. But it also means a single wedged
// replica is indistinguishable from a healthy fleet: an operator could see
// that work was not being done and not which process was failing to do it.
//
// So this listener carries only what is PROCESS-LOCAL and therefore differs
// per target — the Go runtime, this process's own pool, this process's relay
// counter. It re-serves no job-table gauge, and passes a nil outbox backlog
// for the same reason: that read is the api's, and a second copy of a
// fleet-wide number is a worse operator surface than one copy. It carries no
// workspace id and no tenant data at all, which is what makes it a NARROWER
// surface than the api's /metrics rather than a second copy of it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// The listener's own time bounds. Every request it serves is a probe or a
// scrape — short by construction, and the metrics handler carries a 2s budget
// of its own — so these are generous for the traffic and tight enough that an
// unauthenticated client cannot hold a connection, and the goroutine behind
// it, open indefinitely. observeShutdown is the drain window: barely above the
// scrape budget, so this surface never delays the job drain behind it.
const (
	observeReadTimeout  = 10 * time.Second
	observeWriteTimeout = 10 * time.Second
	observeIdleTimeout  = 30 * time.Second
	observeShutdown     = 5 * time.Second
)

// bootGate reports whether this process has finished starting. /readyz is what
// an orchestrator uses to decide a replica may be left in service, and the
// listener comes up FIRST on purpose — answering a probe DURING boot is half
// the reason to have one. Without this gate that ordering would make the
// worker answer "ready" while the event lanes and the job runner were still
// starting, so a rollout could retire the last working replica in favour of
// one that had not yet picked up a job.
//
// Atomic because the boot goroutine writes it while a scrape reads it.
type bootGate struct{ done atomic.Bool }

// complete marks the boot finished. Called once, after the last phase that has
// to be running before this replica can do any work.
func (g *bootGate) complete() { g.done.Store(true) }

// check is the ReadyCheck form. It reports what THIS process knows — whether
// the phase that starts the runner returned — which is the honest substitute
// for a River liveness accessor that does not exist.
func (g *bootGate) check(context.Context) error {
	if g.done.Load() {
		return nil
	}
	return errors.New("the worker is still starting its event lanes and job runner")
}

// observeListener is a started operator surface: how to stop it, and the
// address it actually bound.
//
// Addr is the empty string when the surface is OFF and the resolved address
// otherwise — a caller that asked for :0 gets the port the kernel chose. It is
// returned rather than only logged so that "nothing was bound" is a value a
// caller can check rather than an absence it has to infer.
type observeListener struct {
	Stop func()
	Addr string
}

// startObserveListener serves the worker's probes and metrics on cfg.observeAddr.
//
// An empty address is OFF, and off is the DEFAULT: this is an operator surface
// with no authentication of its own, so a deployment opts into exposing it and
// chooses the interface it binds. Returning a no-op stop for that case keeps
// the caller's defer
// unconditional; a boot that silently bound something the operator did not ask
// for would be the worse failure.
//
// The listen is done HERE rather than inside the goroutine so a busy port or a
// malformed address fails the boot with a message naming it, instead of being
// logged into a process that carries on looking healthy.
func startObserveListener(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, boot *bootGate, log *slog.Logger) (observeListener, error) {
	if cfg.observeAddr == "" {
		return observeListener{Stop: func() {}}, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.HandleFunc("/readyz", httpserver.Readyz("", nil, workerReadyChecks(pool, rdb, boot)...))
	// nil backlog: the outbox backlog is a fleet-wide read the api already
	// serves. nil jobStats and nil overlay for the same reason — both are
	// projections of shared tables, not of this process.
	mux.HandleFunc("/metrics", httpserver.Metrics(pool, nil, events.PublishedTotal, nil, nil, nil))

	srv := &http.Server{
		Addr: cfg.observeAddr,
		// The same headers the api's chassis sets. This port serves no HTML
		// and is not meant for a browser, which is exactly why nosniff and
		// the rest are cheap here and worth having: an operator surface
		// reachable from a browser tab should not depend on nobody opening
		// one. RecoverPanics stays outermost so a panicking handler answers
		// rather than killing the connection.
		Handler:           httpserver.RecoverPanics(log, httpserver.SecureHeaders(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       observeReadTimeout,
		WriteTimeout:      observeWriteTimeout,
		IdleTimeout:       observeIdleTimeout,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.observeAddr)
	if err != nil {
		return observeListener{}, fmt.Errorf("worker: --observe-addr %s: %w", cfg.observeAddr, err)
	}
	bound := listener.Addr().String()
	log.Info("worker observability listener", "addr", bound)

	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("worker observability listener stopped", "err", err)
		}
	}()

	return observeListener{Addr: bound, Stop: func() {
		// Its own window, detached from the run context: at shutdown that
		// context is already cancelled, and passing it would turn every
		// graceful drain into an immediate close.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), observeShutdown)
		defer cancel()
		if err := srv.Shutdown(stopCtx); err != nil {
			log.Warn("stopping the worker observability listener", "err", err)
		}
	}}, nil
}

// workerReadyChecks are what this replica needs before it can do any work: its
// own boot having finished, the database every job is read and written
// through, and the bus its subscribers consume from. All three are probed,
// because a worker missing any one of them is not doing the work while
// answering a check of the others perfectly.
//
// Deliberately NOT a check on the River client itself: it exposes no liveness
// accessor, so any answer here would be this file's guess about a dependency's
// internals rather than a reading of it. The boot gate is what stands in its
// place, and it reports only what this process actually knows.
func workerReadyChecks(pool *pgxpool.Pool, rdb *redis.Client, boot *bootGate) []httpserver.ReadyCheck {
	return []httpserver.ReadyCheck{
		{Name: "boot", Check: boot.check},
		{Name: "postgres", Check: pool.Ping},
		{Name: "redis", Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
	}
}
