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
// fleet-wide number is a worse operator surface than one copy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// observeShutdown bounds the listener's own drain. A probe or a scrape is a
// short request by construction — the metrics handler carries a 2s budget of
// its own — so a window barely above that is enough to let one in flight
// finish, and short enough that the observability surface never delays the
// job drain behind it.
const observeShutdown = 5 * time.Second

// startObserveListener serves the worker's probes and metrics on cfg.observeAddr.
//
// An empty address is OFF, and off is the DEFAULT: this is an operator surface
// with no authentication of its own — it carries workspace ids, exactly as the
// api's does — so a deployment opts into exposing it and chooses the interface
// it binds. Returning a no-op stop for that case keeps the caller's defer
// unconditional; a boot that silently bound something the operator did not ask
// for would be the worse failure.
//
// The listen is done HERE rather than inside the goroutine so a busy port or a
// malformed address fails the boot with a message naming it, instead of being
// logged into a process that carries on looking healthy.
func startObserveListener(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, log *slog.Logger) (func(), error) {
	if cfg.observeAddr == "" {
		return func() {}, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.HandleFunc("/readyz", httpserver.Readyz("", nil, workerReadyChecks(pool, rdb)...))
	// nil backlog: the outbox backlog is a fleet-wide read the api already
	// serves. nil jobStats and nil overlay for the same reason — both are
	// projections of shared tables, not of this process.
	mux.HandleFunc("/metrics", httpserver.Metrics(pool, nil, events.PublishedTotal, nil, nil, nil))

	srv := &http.Server{
		Addr:              cfg.observeAddr,
		Handler:           httpserver.RecoverPanics(log, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.observeAddr)
	if err != nil {
		return nil, fmt.Errorf("worker: --observe-addr %s: %w", cfg.observeAddr, err)
	}
	log.Info("worker observability listener", "addr", listener.Addr().String())

	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("worker observability listener stopped", "err", err)
		}
	}()

	return func() {
		// Its own window, detached from the run context: at shutdown that
		// context is already cancelled, and passing it would turn every
		// graceful drain into an immediate close.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), observeShutdown)
		defer cancel()
		if err := srv.Shutdown(stopCtx); err != nil {
			log.Warn("stopping the worker observability listener", "err", err)
		}
	}, nil
}

// workerReadyChecks are the dependencies this role cannot work without: the
// database it reads and writes every job through, and the bus its subscribers
// consume from. Both are probed, because a worker that has one and not the
// other is half a worker and reads as ready on a check of either alone.
//
// Deliberately NOT a check on the job runner: River's client has no exported
// liveness accessor, so any answer here would be this file's guess about a
// dependency's internals rather than a reading of it.
func workerReadyChecks(pool *pgxpool.Pool, rdb *redis.Client) []httpserver.ReadyCheck {
	return []httpserver.ReadyCheck{
		{Name: "postgres", Check: pool.Ping},
		{Name: "redis", Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
	}
}
