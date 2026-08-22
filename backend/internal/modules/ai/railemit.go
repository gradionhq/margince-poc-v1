// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The router's report to the AI-activity projection.
//
// Every model call in this build passes through the trace flush, which is what
// makes this the one place a task cannot be missed: a task declared next year
// reports its work before its author has thought about the rail. That is the
// whole reason the default reporter is here rather than at each caller — the
// callers are the set that kept growing without anybody noticing seventeen of
// them were silent.
//
// What the router can honestly say is narrow, and the narrowness is the point:
// it learns of a call once the call is OVER, so its occurrence is settled the
// moment it appears. It never claims to be running, and it therefore never
// needs a lease. Work that deserves a live line has a durable carrier, and the
// registry in railowner.go hands the task to that carrier instead.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The states a router occurrence can hold. It is settled by construction, so
// the live half of the projection's vocabulary is unreachable from here.
const (
	railStateDone     = "done"
	railStateDegraded = "degraded"
	railStateFailed   = "failed"
)

// announceRailBestEffort announces the call and, if it cannot, says so in the
// log and leaves the trace alone.
//
// The announcement runs inside a SAVEPOINT for one reason: the trace is what
// the budget guardrail, the cost ledger and the certification record are all
// read from, and none of them may be lost because a rail row could not be
// written. That is the same posture the router's flush already takes toward
// tracing itself — observability must not become a new way for a working model
// call to fail — applied one layer further in.
//
// It is a swallowed error only in the sense that the caller does not see it.
// The log line names the task and the reason, which is what an operator needs
// to tell a rail that is missing rows from a rail that has none to show.
func (m *CallMeter) announceRailBestEffort(ctx context.Context, tx pgx.Tx, terminal Call) {
	if !RouterReports(terminal.Task) {
		return
	}
	// A call outside a correlation scope cannot be announced AT ALL, and saying
	// so here is the difference between one skipped occurrence and a guaranteed
	// error per call. storekit.Emit refuses an envelope with no correlation id,
	// and Call.CorrelationID is read from that same context value — so when it
	// is absent the emit below is not unlikely to fail, it cannot succeed. An
	// earlier version of this file documented a fallback to the logical call id
	// and had a test for the key it would have used; the key was reachable and
	// the occurrence never was.
	if !announceable(terminal) {
		m.log.WarnContext(ctx, "ai: a model call ran outside any correlation scope, so it is traced but cannot be announced to the AI-activity rail", "task", string(terminal.Task))
		return
	}
	nested, err := tx.Begin(ctx)
	if err != nil {
		m.log.ErrorContext(ctx, "ai: opening the rail announcement savepoint failed — the call is traced but absent from the AI-activity rail", "task", string(terminal.Task), "err", err)
		return
	}
	if err := m.announceRail(ctx, nested, terminal); err != nil {
		m.log.ErrorContext(ctx, "ai: announcing the call to the AI-activity rail failed — the call is traced but absent from the rail", "task", string(terminal.Task), "err", err)
		if rbErr := nested.Rollback(ctx); rbErr != nil {
			m.log.ErrorContext(ctx, "ai: rolling back the rail announcement failed", "task", string(terminal.Task), "err", rbErr)
		}
		return
	}
	if err := nested.Commit(ctx); err != nil {
		m.log.ErrorContext(ctx, "ai: committing the rail announcement failed — the call is traced but absent from the rail", "task", string(terminal.Task), "err", err)
	}
}

// announceable reports whether this call can reach the bus at all.
//
// A correlation id is not a nicety here: storekit.Emit REFUSES an envelope
// without one, so a call outside a correlation scope cannot produce an
// occurrence however the key is built.
func announceable(c Call) bool {
	return c.CorrelationID != nil && !c.CorrelationID.IsZero()
}

// railLockTimeout bounds the WAIT for the occurrence lock.
//
// The whole flush runs under traceWriteTimeout, and the lock serializes the
// page-parallel fan-out this exists for — so an unbounded wait would spend the
// TRACE's budget queueing behind siblings, and the deadline would then land on
// whatever statement happened to be running. A bounded wait fails as a clean
// SQL error inside the savepoint instead, which costs one occurrence rather
// than every ai_call row of the logical call.
const railLockTimeout = "1500ms"

// lockOccurrence takes the occurrence lock under a bounded wait, and puts the
// timeout back the moment it holds it.
//
// SET LOCAL outlives the statement — it is scoped to the transaction, and a
// subtransaction that COMMITS keeps it — so leaving it set would quietly impose
// a 1.5s ceiling on the ledger and outbox writes that follow, which want to WAIT
// for an unrelated lock rather than abandon an occurrence over one. The timeout
// is here to bound acquisition, and acquisition only.
//
// The value is a compile-time literal in both directions: SET LOCAL takes no
// placeholder, so the only safe spelling of a GUC value is one that cannot come
// from a request.
func lockOccurrence(ctx context.Context, tx pgx.Tx, key string) error {
	// The PRIOR value, restored afterwards — not `= DEFAULT`, which means the
	// server's configured default and would discard a caller's own setting. A
	// pool can carry lock_timeout on its DSN, which makes it the session value,
	// and this transaction has no business deciding that a bound somebody else
	// chose no longer applies to the writes that follow.
	var previous string
	if err := tx.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&previous); err != nil {
		return fmt.Errorf("ai: reading the lock timeout: %w", err)
	}
	// set_config's third argument is is_local, so this is SET LOCAL by another
	// name — and unlike SET LOCAL it takes the value as a parameter, which is
	// what lets the restore below carry a value read at runtime.
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, railLockTimeout); err != nil {
		return fmt.Errorf("ai: bounding the rail occurrence lock: %w", err)
	}
	lockErr := storekit.LockWriteIdentity(ctx, tx, "ai_task_run", key)
	if lockErr != nil {
		// Not restored on this path, and it does not need to be: the caller
		// rolls the savepoint back, which reverts the setting with it.
		return fmt.Errorf("ai: locking the rail occurrence: %w", lockErr)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, previous); err != nil {
		return fmt.Errorf("ai: restoring the lock timeout: %w", err)
	}
	return nil
}

// announceRail publishes the terminal attempt of one logical call as a state
// change on the AI-activity projection.
//
// It rides the same transaction as the ai_call rows it describes, so the trace
// and the occurrence agree about whether the call happened. The ledger row
// comes first because the bus refuses an entity-less event without a trace
// link: an AI task names no domain record, so the system_log row is what keeps
// the outcome attributable.
func (m *CallMeter) announceRail(ctx context.Context, tx pgx.Tx, terminal Call) error {
	key := unitOfWorkKey(terminal)
	// Serialize this occurrence's writers before anything reads its attempt.
	//
	// railAttempt COUNTS, and at READ COMMITTED two concurrent transactions for
	// one (correlation, task) cannot see each other's uncommitted ai_call row —
	// so both count the same value, both announce the same attempt, and the
	// projection's guard refuses the second as a redelivery of the first. One of
	// the two outcomes is then lost with nothing logged, and it is the LATER one
	// that loses, so a failure can outlive the retry that fixed it. Measured
	// without it: 3 of 16 concurrent calls got a distinct attempt.
	//
	// Not hypothetical: site_fact_extract is the deep read's page-PARALLEL fact
	// lane by design, so one read fires many of these at once under one
	// correlation id.
	//
	// It is taken INSIDE the savepoint, with the timeout, so that failing to get
	// it aborts only this subtransaction. Taken on the outer transaction a lock
	// error would poison the trace transaction itself, and every ai_call row of
	// the logical call would be lost at COMMIT — the opposite of what the
	// savepoint is here to guarantee.
	if err := lockOccurrence(ctx, tx, key); err != nil {
		return err
	}
	attempt, finished, err := railAttempt(ctx, tx, terminal)
	if err != nil {
		return err
	}
	ledgerID, err := storekit.LogSystem(ctx, tx, "ai_task.state_changed", map[string]any{
		"source": SourceRouter, "occurrence_key": key, "state": railState(terminal),
	})
	if err != nil {
		return fmt.Errorf("ai: log rail state change: %w", err)
	}
	// The call ran for LatencyMS before it finished, so its start is derivable
	// from the database's own clock rather than this process's — a host clock
	// here would disagree with every other timestamp on the row.
	//
	// This pair describes the LATEST call of the unit of work, not the span of
	// all of them, and that is the grain rather than a rounding error: the
	// contract defines started_at as "when the current attempt became current",
	// and a row that claimed to span forty page reads would be answering a
	// question ("how long did the site read take") that no emitter here can
	// actually answer — the unit of work has no end of its own until its last
	// call lands.
	started := finished.Add(-time.Duration(terminal.LatencyMS) * time.Millisecond)
	task := string(terminal.Task)
	payload := crmcontracts.InternalEventAiTaskStateChanged{
		Source:        SourceRouter,
		OccurrenceKey: key,
		// The task's own name is the display kind. A router occurrence is not
		// one of a catalog of named activities — it IS the task, and giving it
		// any other name would invent a second vocabulary for the same thing.
		Kind:       task,
		AiTask:     &task,
		Attempt:    attempt,
		State:      railState(terminal),
		QueuedAt:   started,
		StartedAt:  &started,
		FinishedAt: &finished,
		// No lease: the occurrence is settled when it is written, so there is
		// no live attempt whose believability could expire.
		DegradeReason: railDegradeReason(terminal),
	}
	if err := storekit.EmitPipelinePayload(ctx, tx, ledgerID, payload); err != nil {
		return fmt.Errorf("ai: publish rail state change: %w", err)
	}
	return nil
}

// unitOfWorkKey identifies the occurrence this call belongs to: one piece of
// work, one task.
//
// The correlation id is the request or the job pass, so a site read that makes
// forty calls for one task is ONE line on the rail rather than forty — the
// volume rule falls out of the key instead of asking every caller to remember
// it. A call with no correlation id is its own unit, which is honest rather
// than tidy: nothing groups it, and reporting it alone is better than not
// reporting it at all.
func unitOfWorkKey(c Call) string {
	if c.CorrelationID != nil && !c.CorrelationID.IsZero() {
		return c.CorrelationID.String() + ":" + string(c.Task)
	}
	return c.LogicalCallID.String() + ":" + string(c.Task)
}

// railAttempt counts the terminal calls this unit of work has now made for this
// task, and reads the finish instant off the database's clock.
//
// clock_timestamp(), NOT now(): now() is TRANSACTION-START, and this transaction
// may have spent time queueing for the occurrence lock. A waiter would then
// stamp an earlier finish than the attempt that went before it, and the settled
// feed — ordered by finished_at — would move the occurrence backwards in a list
// that is supposed to read newest-first. clock_timestamp() is when the call
// actually finished being recorded, which is the fact the column names.
//
// Still the DATABASE's clock either way: every other timestamp on the row is
// stamped there, and a host clock would disagree with them by the drift between
// the two machines.
//
// The count is the occurrence's attempt, and it has to be counted rather than
// assumed: the projection's guard is lexicographic on (attempt, rank) and
// settled is terminal within an attempt, so a second call under one key that
// reused attempt 1 would be refused as a duplicate and the rail would keep
// reporting the first outcome forever — including a failure a retry had already
// corrected.
//
// It is bounded by the calls of a single request or job pass, which is what
// makes counting affordable here and would not be if the key were coarser.
func railAttempt(ctx context.Context, tx pgx.Tx, c Call) (attempt int, finished time.Time, err error) {
	// A correlation id that is absent and one that is the zero value mean the
	// same thing here — nothing groups this call — and storekit's own spelling
	// is what collapses them, rather than a second helper saying it again.
	var corr *ids.UUID
	if c.CorrelationID != nil {
		corr = storekit.UUIDOrNil(*c.CorrelationID)
	}
	row := tx.QueryRow(ctx, `
		SELECT count(*), clock_timestamp()
		  FROM ai_call
		 WHERE is_terminal
		   AND task = $1
		   AND ($2::uuid IS NULL AND logical_call_id = $3 OR correlation_id = $2::uuid)`,
		string(c.Task), corr, c.LogicalCallID)
	if err := row.Scan(&attempt, &finished); err != nil {
		return 0, time.Time{}, fmt.Errorf("ai: counting rail attempts: %w", err)
	}
	if attempt < 1 {
		// The terminal row was inserted in this transaction, so the count can
		// only be short if the caller announced a call it never recorded.
		return 0, time.Time{}, fmt.Errorf("ai: rail attempt counted %d terminal calls for task %q, but this one was just written", attempt, c.Task)
	}
	return attempt, finished, nil
}

// railState reads the occurrence's settled state off the terminal attempt.
//
// An errored call is failed even when it also degraded: degraded means partial
// state was kept and MUST NOT read as done, but a call that ended on a sentinel
// kept nothing at all.
func railState(c Call) string {
	switch {
	case c.ErrorSentinel != "":
		return railStateFailed
	case c.Degraded:
		return railStateDegraded
	default:
		return railStateDone
	}
}

// railDegradeReason says why the occurrence did not finish cleanly, in a closed
// vocabulary, or none when it did.
//
// A SENTINEL or an attempt reason, never a provider's message: degrade_reason
// reaches an ordinary rep, and vendor error text carries provider detail and can
// echo credential material. The underlying cause is already in the router's own
// log line.
//
// The degraded-WITHOUT-a-sentinel case is the common one and was missed at
// first: applyBudget demotes the ladder and the call then SUCCEEDS, so there is
// no error to name and the rail said "degraded" with no way to say why. The
// router already knows — it wrote the reason onto the trace as the attempt's
// own — so the answer is to carry it rather than to leave the field null and
// call it optional.
func railDegradeReason(c Call) *string {
	if c.ErrorSentinel != "" {
		reason := c.ErrorSentinel
		return &reason
	}
	if c.Degraded && c.AttemptReason != "" {
		reason := c.AttemptReason
		return &reason
	}
	return nil
}
