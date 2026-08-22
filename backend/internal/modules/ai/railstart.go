// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The router's OPENING announcement: the occurrence is live, before the model
// is asked anything.
//
// railemit.go announces the other end — what a call turned out to be, once it
// was over. That was the whole rail for router-owned work, and it is why a rep
// who asked for a summary saw nothing at all and then saw "done": the only
// moment the router spoke was after the moment worth watching had passed.
//
// The unit framed here is ONE LOGICAL CALL, not one unit of work, and the
// difference is the reason this file is small. A logical call has a beginning
// the router can observe (it is about to serve it) and an end it already
// observes (the flush), so the pair needs no scope, no teardown at whatever
// mints a correlation id, and no attempt that must survive a process. For the
// tasks a person triggers and then waits on — summarize, draft_reply,
// offer_draft, all registered oneShot — the logical call IS the unit of work,
// and framing it is the whole feature.
//
// What that costs, honestly: a task whose unit of work spans MANY logical calls
// under one correlation id (the deep read's page-parallel fact lane) reopens
// its occurrence once per call, because a later call's attempt outranks the
// previous settle. The row still ends settled and nothing renders those kinds
// today, so the churn is real and invisible — but it is churn, and a unit-of-
// work frame is what would remove it rather than hide it.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// railStateRunning is the one live state the router can honestly claim. It
// never says queued: by the time this file speaks, the call is being served.
const railStateRunning = "running"

// railStarter is the OPTIONAL half of CallRecorder.
//
// Optional rather than a third method on the interface, because CallRecorder
// has implementations with no database behind them at all — the cert lane's
// in-memory recorder and the DB-less local router seam (ai.WithCallStore).
// Widening the interface would force both to grow a method whose only honest
// body is a no-op, which is a worse lie than not implementing it: a recorder
// that cannot reach Postgres cannot announce, and saying so by NOT satisfying
// this interface is the accurate statement.
type railStarter interface {
	AnnounceRailStart(ctx context.Context, c Call, lease time.Duration)
}

// railLease is how long a live router occurrence stays believable.
//
// DERIVED from the bound it must outlast, never chosen: requestTimeout caps a
// single model call (it is the http.Client timeout every adapter is built
// with), and a logical call may walk every rung of its ladder, spending that
// bound on each. traceWriteTimeout is added because the occurrence is not
// settled by the last rung but by the flush that follows it — a lease that
// expired between the two would render a call stalled in the instant before it
// reported success.
//
// A round number here would be a guess that happens to look like a decision,
// and the failure it buys is silent: too short renders healthy work as a dead
// worker, too long leaves a killed process claiming to work until somebody
// notices. Neither is visible in a test that does not run for minutes.
func railLease(ladder []Tier) time.Duration {
	rungs := len(ladder)
	if rungs < 1 {
		// An empty ladder serves nothing, so no call will run — but the lease
		// is computed before that is known, and a zero lease would mark the
		// occurrence stale the instant it appeared.
		rungs = 1
	}
	return requestTimeout*time.Duration(rungs) + traceWriteTimeout
}

// announceRailStartOnce opens this logical call's occurrence, at most once.
//
// ONCE is the whole reason this hangs off logicalCall rather than off Router.
// CompleteStructured threads one logicalCall through up to three serveAttempt
// calls — the first try, the schema-invalid retry, the tier escalation — and
// they are rungs of one piece of work a reader asked for once. Announcing per
// attempt would reopen the occurrence under a rising attempt twice, so the rail
// would report one request as three starts.
//
// It reads the correlation id off the SAME context value Call.CorrelationID is
// read from at flush, so the opening and closing announcements agree about
// which occurrence they describe, or neither is made.
func (lc *logicalCall) announceRailStartOnce(ctx context.Context, r *Router, task Task, ladder []Tier) {
	if lc.railAnnounced {
		return
	}
	starter, ok := r.calls.(railStarter)
	if !ok {
		return
	}
	// Set before the announcement rather than after it. A failed announce is
	// deliberately not retried on the next rung: the retry would be a SECOND
	// start for the same work, and a rail that is missing one line is a smaller
	// wrong than a rail that invents one.
	lc.railAnnounced = true
	c := Call{Task: task, LogicalCallID: lc.id}
	if cid, ok := principal.CorrelationID(ctx); ok {
		c.CorrelationID = &cid
	}
	starter.AnnounceRailStart(ctx, c, railLease(ladder))
}

// AnnounceRailStart publishes the occurrence as running, and never fails the
// call it is about to describe.
//
// It opens its OWN transaction rather than riding one, because there is no
// transaction to ride: the trace's transaction does not exist until the flush,
// which is the very thing that made the router settled-only. That is the one
// structural cost of speaking early, and it is why every failure below is a log
// line — a model call must not break because the rail could not say it started.
func (m *CallMeter) AnnounceRailStart(ctx context.Context, c Call, lease time.Duration) {
	if !RouterReports(c.Task) {
		return
	}
	// Same refusal as the settling half, for the same reason: storekit.Emit
	// rejects an envelope with no correlation id, so a call outside a
	// correlation scope cannot produce an occurrence however the key is built.
	// Announcing the start of one the flush will never close would leave a row
	// claiming to work until its lease expired.
	if !announceable(c) {
		return
	}
	err := m.db.Tx(ctx, func(tx pgx.Tx) error {
		return m.announceRailStartTx(ctx, tx, c, lease)
	})
	if err != nil {
		m.log.ErrorContext(ctx, "ai: announcing the start of a model call to the AI-activity rail failed — the call runs and is traced, but shows on the rail only once it settles", "task", string(c.Task), "err", err)
	}
}

// announceRailStartTx writes the ledger row and publishes the running state.
//
// No lock, and that is a difference from the settling half worth stating. The
// settle COUNTS terminal calls under a write-identity lock because two
// concurrent settles that computed the same attempt would have one of their
// outcomes silently refused — and losing an outcome is losing a fact. Two
// concurrent STARTS that compute the same attempt both publish `running` for
// one occurrence; the projection's guard refuses the second as an equal
// (attempt, rank) redelivery and the row reads running either way. Nothing is
// lost, so nothing needs serializing, and the hot path does not pay for a lock
// to protect a value it cannot get wrong.
func (m *CallMeter) announceRailStartTx(ctx context.Context, tx pgx.Tx, c Call, lease time.Duration) error {
	key := unitOfWorkKey(c)
	attempt, started, err := railStartAttempt(ctx, tx, c)
	if err != nil {
		return err
	}
	ledgerID, err := storekit.LogSystem(ctx, tx, "ai_task.state_changed", map[string]any{
		"source": SourceRouter, "occurrence_key": key, "state": railStateRunning,
	})
	if err != nil {
		return fmt.Errorf("ai: log rail start: %w", err)
	}
	task := string(c.Task)
	seconds := int(lease.Seconds())
	payload := crmcontracts.InternalEventAiTaskStateChanged{
		Source:        SourceRouter,
		OccurrenceKey: key,
		Kind:          task,
		AiTask:        &task,
		Attempt:       attempt,
		State:         railStateRunning,
		QueuedAt:      started,
		// StartedAt is set and equal to QueuedAt because the router never
		// observes a queue: it announces the instant it begins serving. The
		// column is not decoration here — ai_task_run_queued_has_no_start
		// requires a non-queued state to carry one.
		StartedAt:    &started,
		LeaseSeconds: &seconds,
	}
	if err := storekit.EmitPipelinePayload(ctx, tx, ledgerID, payload); err != nil {
		return fmt.Errorf("ai: publish rail start: %w", err)
	}
	return nil
}

// railStartAttempt is the attempt THIS call will settle under.
//
// One more than the terminal calls already recorded for the occurrence, because
// the settle counts the same rows once its own is written — so the two agree by
// construction for a logical call that is alone under its key, which is every
// oneShot task and therefore every kind the rail draws.
//
// Where they can disagree is a page-PARALLEL fan-out under one correlation id:
// a sibling settling between this start and this settle lifts the settle's
// count above the start's. The occurrence then reopens at the higher attempt
// and settles there, which is the projection behaving correctly — a higher
// attempt outranks everything — and is why disagreement costs churn rather than
// a lost outcome.
//
// clock_timestamp(), not now(): now() is transaction-start, and this is a fresh
// transaction whose start is not the instant the call begins. Still the
// DATABASE's clock, because stale_after is derived from this value and compared
// against the database's now() at read time — a host clock would decide when the
// row reads stalled by the size of its own drift.
func railStartAttempt(ctx context.Context, tx pgx.Tx, c Call) (attempt int, started time.Time, err error) {
	row := tx.QueryRow(ctx, `
		SELECT count(*) + 1, clock_timestamp()
		  FROM ai_call
		 WHERE is_terminal
		   AND task = $1
		   AND correlation_id = $2::uuid`,
		string(c.Task), storekit.UUIDOrNil(*c.CorrelationID))
	if err := row.Scan(&attempt, &started); err != nil {
		return 0, time.Time{}, fmt.Errorf("ai: counting rail start attempts: %w", err)
	}
	return attempt, started, nil
}
