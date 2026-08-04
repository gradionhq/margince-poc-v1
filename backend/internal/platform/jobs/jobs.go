// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jobs owns the River client lifecycle — the durable
// background-job substrate, the peer of platform/events for the outbox —
// and the DECLARATION every job kind is built from: the Spec table
// compiled from backend/api/jobs.yaml, which holds each kind's role,
// queue, timeout, attempt cap, fan-out edge and registration posture.
//
// Declaration and wiring are split on purpose, and the split is the whole
// design: this package says what a kind IS, and the composition layer
// still assembles the runner — it supplies the queue set, constructs the
// workers, and schedules the periodic jobs. So platform never learns what
// a worker does.
//
// The declaration is what the runtime OBEYS for the timeout of every job
// registered THROUGH Govern: it wraps a worker in a type River reaches
// only through Work, so that worker cannot answer for its own wall clock.
//
// Registration is bound too, and the binding lives one layer up: the
// composition layer registers only through a generated function whose type
// parameter is the closed set of DECLARED args types, so a kind
// api/jobs.yaml has never heard of does not compile, and forbidigo refuses
// every direct River registration — AddWorker, AddWorkerArgs and
// AddWorkerSafely alike — that would go around it. Neither gate reaches a
// fixture registering into a throwaway *river.Workers, and neither would
// notice a hand-edited generated union, which is what MustBeTotal is for: it
// names any registered kind the file does not declare, and the runner refuses
// to boot rather than working it at River's silent one-minute default.
//
// It owns no domain either way. The boundary with the bus is deliberate:
// an event announces that something happened (outbox); a job asks for
// work to be done (here).
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

// NewInserter builds an insert-only Runner for a role that enqueues jobs
// but works none (the api): no queues, workers, or periodic jobs are
// configured, so Start must NOT be called on it — Enqueue is its whole
// surface. The worker role's fully configured Runner picks the rows up.
func NewInserter(pool *pgxpool.Pool, log *slog.Logger) (*Runner, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("jobs: new insert-only client: %w", err)
	}
	return &Runner{client: client}, nil
}

// Enqueue inserts one job for the worker role to pick up. Uniqueness
// policy rides opts (e.g. UniqueOpts.ByArgs deduplicates a re-submitted
// job): the caller owns the policy, this method only carries it to River.
func (r *Runner) Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error {
	if _, err := r.client.Insert(ctx, args, opts); err != nil {
		return fmt.Errorf("jobs: enqueue %s: %w", args.Kind(), err)
	}
	return nil
}

// EnqueueTx inserts a job through the caller's transaction. It is used when a
// durable work request and the operational row the worker will claim must
// either both commit or both disappear.
func (r *Runner) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	if _, err := r.client.InsertTx(ctx, tx, args, opts); err != nil {
		return fmt.Errorf("jobs: enqueue %s transactionally: %w", args.Kind(), err)
	}
	return nil
}

// EnqueueTxUnique inserts a job through the caller's transaction like
// EnqueueTx, but also surfaces River's dedupe outcome: inserted is false
// when the job's UniqueOpts matched an existing job and River skipped the
// insert rather than erroring. Callers that must tell "enqueued" apart
// from "already running" (e.g. a confirm endpoint choosing 202 vs 409)
// need this signal; EnqueueTx alone discards it.
func (r *Runner) EnqueueTxUnique(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (bool, error) {
	res, err := r.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return false, fmt.Errorf("jobs: enqueue %s transactionally: %w", args.Kind(), err)
	}
	return !res.UniqueSkippedAsDuplicate, nil
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

// SubscribeFailed delivers job-FAILURE events — a job that errored and was
// either set to retry or discarded. It fires per ATTEMPT, so unlike completion
// and cancellation it does not announce a settled job: a caller that needs the
// failure to be final must make the fault permanent and let the attempt budget
// run out, or read the row's state itself. What it gives that the other two
// cannot is any signal at all for a failing job — one that has neither
// completed nor cancelled may equally be one still running. Subscribe before
// Start so no failure is missed; call the returned cancel when done.
func (r *Runner) SubscribeFailed() (<-chan *river.Event, func()) {
	return r.client.Subscribe(river.EventKindJobFailed)
}

// SubscribeCancelled delivers job-cancellation events (river.JobCancel) —
// the counterpart to SubscribeCompleted for a job that deliberately stops
// rather than finishing normally (e.g. the embed-reindex worker's
// identity-drift guard, which must be observed as a cancellation, not
// burn its retry budget). Subscribe before Start so no cancellation is
// missed; call the returned cancel when done.
func (r *Runner) SubscribeCancelled() (<-chan *river.Event, func()) {
	return r.client.Subscribe(river.EventKindJobCancelled)
}
