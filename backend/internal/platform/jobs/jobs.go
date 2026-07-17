// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jobs owns the River client lifecycle — the durable
// background-job substrate, the peer of platform/events for the outbox.
// It owns no domain: the queue set, workers, and periodic jobs are
// supplied by the composition layer. The boundary is deliberate: an event
// announces that something happened (outbox); a job asks for work to be
// done (here).
package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Config is the runner's wiring, populated by the composition layer. Both
// Queues and Workers are required for a client to work jobs.
type Config struct {
	Queues       map[string]river.QueueConfig
	Workers      *river.Workers
	PeriodicJobs []*river.PeriodicJob
}

// Runner wraps a River client bound to the shared pool. The zero value is
// not usable — construct with New.
type Runner struct {
	client *river.Client[pgx.Tx]
}

// New builds a River client over the given pool. The pool must outlive the
// runner (River holds it for the client's lifetime).
func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Runner, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       cfg.Queues,
		Workers:      cfg.Workers,
		PeriodicJobs: cfg.PeriodicJobs,
		Logger:       log,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: new client: %w", err)
	}
	return &Runner{client: client}, nil
}

// NewInserter builds an INSERT-ONLY River client over the pool — no queues,
// no workers, never Started. It is for a process role (the api) that enqueues
// jobs another role (the worker) executes: Insert writes the river_job row and
// the worker's leader-elected client picks it up by Kind. The pool must
// outlive the returned Runner.
func NewInserter(pool *pgxpool.Pool, log *slog.Logger) (*Runner, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("jobs: new inserter: %w", err)
	}
	return &Runner{client: client}, nil
}

// Insert enqueues one job. Safe from an insert-only Runner (NewInserter): the
// row is durable the moment Insert returns, independent of any worker running.
func (r *Runner) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error {
	if _, err := r.client.Insert(ctx, args, opts); err != nil {
		return fmt.Errorf("jobs: insert %s: %w", args.Kind(), err)
	}
	return nil
}

// Start begins working the configured queues and returns once startup
// completes; the client keeps running until Stop. Leadership is elected
// cluster-wide, so periodic jobs fire exactly once across all replicas.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.client.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start: %w", err)
	}
	return nil
}

// Stop drains in-flight jobs and shuts the client down gracefully; a job
// caught mid-flight by shutdown finishes rather than being abandoned.
func (r *Runner) Stop(ctx context.Context) error {
	if err := r.client.Stop(ctx); err != nil {
		return fmt.Errorf("jobs: stop: %w", err)
	}
	return nil
}

// SubscribeCompleted delivers job-completion events so callers can await a
// specific job without polling or sleeping. Subscribe before Start so no
// completion is missed; call the returned cancel when done.
func (r *Runner) SubscribeCompleted() (<-chan *river.Event, func()) {
	return r.client.Subscribe(river.EventKindJobCompleted)
}
