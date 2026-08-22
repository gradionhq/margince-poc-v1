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
// DERIVED from the bound it must outlast, never chosen. Three factors, and the
// third is the one an earlier version of this file got wrong:
//
//   - requestTimeout caps a SINGLE model call — the http.Client timeout every
//     adapter is built with.
//   - a walk of the ladder may spend that bound on EVERY rung.
//   - and CompleteStructured walks the ladder up to maxLadderWalks times for one
//     logical call: the first try, the schema-invalid retry, the escalation.
//
// The third matters because the start is announced ONCE, by construction, and
// the projection's guard refuses an equal (attempt, rank) re-announcement — so
// there is no such thing as extending this lease later. It has to cover the
// whole logical call on the day it is written. Sized for one walk, a structured
// call that legitimately retried would render stalled while it was still
// working, which is precisely the failure this derivation exists to prevent.
//
// traceWriteTimeout is added because the occurrence is settled not by the last
// rung but by the flush that follows it.
//
// A round number would be a guess that happens to look like a decision, and the
// failure it buys is silent in both directions: too short renders healthy work
// as a dead worker, too long leaves a killed process claiming to work. Neither
// is visible in a test that does not run for minutes.
func railLease(ladder []Tier) time.Duration {
	rungs := len(ladder)
	if rungs < 1 {
		// An empty ladder serves nothing, so no call will run — but the lease
		// is computed before that is known, and a zero lease would mark the
		// occurrence stale the instant it appeared.
		rungs = 1
	}
	return requestTimeout*time.Duration(rungs*maxLadderWalks) + traceWriteTimeout
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
//
// WHERE IT IS CALLED FROM is as load-bearing as what it does. serveAttempt
// calls it after the workspace check, the budget, the cache key and the profile
// — every return above those is untraced, so announcing higher would open an
// occurrence with no terminal trace behind it at all. From that point the
// deferred finalizeAttempt is armed, so the attempt APPENDS a terminal trace
// whatever it does next, and the ladder in hand is the adjusted one, so the
// lease is sized to rungs that can really run.
//
// Appending is not writing, and that gap is this placement's honest limit: the
// flush is best-effort by design (it must never fail a working model call), so
// a flush that times out — or a process that dies between the two — leaves a
// start nothing settles. The lease bounds how long such a row claims to be
// working, and aiactivity's sweep of abandoned router occurrences is what
// finally closes it. The settle is LIKELY, not guaranteed; a comment claiming
// otherwise would be the kind nobody re-checks.
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
// No lock, unlike the settling half — and what that costs is worth stating
// exactly, because it is not nothing.
//
// The settle COUNTS terminal calls under a write-identity lock because two
// concurrent settles that computed one attempt would have an OUTCOME silently
// refused, and losing an outcome is losing a fact that nothing can recover.
//
// Two concurrent starts under one key lose something smaller and recoverable. B
// counts the same terminals as A, both publish `running` at that attempt, and
// the projection refuses the second as an equal (attempt, rank) event. The row
// reads running either way, so nothing is wrong on screen — but if A then
// SETTLES while B is still working, the row reads settled while B runs, until
// B's own settle reopens it at the next attempt. A live interval is suppressed;
// no fact is lost.
//
// That is accepted rather than overlooked, because it needs two logical calls
// of ONE task under ONE correlation id — which is the multi-call shape the file
// header already names as churn-prone, which no displayed kind has, and which a
// unit-of-work frame is what actually fixes. Paying a lock on every start to
// tighten a live interval nobody currently renders is the wrong trade.
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
	// At least one second, because truncation here has a meaning nobody wants:
	// the projection reads a lease of 0 as NO lease, and a running occurrence
	// without one is exactly the row that claims to be working forever. A
	// sub-second lease is not reachable from railLease today, which is the
	// reason to clamp rather than reject — a future caller that passes one has
	// asked for the shortest believable lease, not for an immortal row.
	seconds := max(int(lease.Seconds()), 1)
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
// construction for a logical call that is alone under its key. Every kind the
// rail DRAWS today is such a call (summarize, draft_reply and offer_draft are
// registered oneShot), which is not the same as every router-owned task: the
// registry also hands the router page-parallel work like site_fact_extract, and
// those are the calls the paragraph below is about.
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
