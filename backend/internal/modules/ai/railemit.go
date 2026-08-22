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

// announceRail publishes the terminal attempt of one logical call as a state
// change on the AI-activity projection.
//
// It rides the SAME transaction as the ai_call rows it describes, so the trace
// and the occurrence can never disagree about whether the call happened. The
// ledger row comes first because the bus refuses an entity-less event without a
// trace link: an AI task names no domain record, so the system_log row is what
// keeps the outcome attributable.
func (m *CallMeter) announceRail(ctx context.Context, tx pgx.Tx, terminal Call) error {
	if !RouterReports(terminal.Task) {
		return nil
	}
	key := unitOfWorkKey(terminal)
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
// task, and reads the finish instant off the same clock the rows were written
// with.
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
	corr := c.CorrelationID
	row := tx.QueryRow(ctx, `
		SELECT count(*), now()
		  FROM ai_call
		 WHERE is_terminal
		   AND task = $1
		   AND ($2::uuid IS NULL AND logical_call_id = $3 OR correlation_id = $2::uuid)`,
		string(c.Task), uuidOrNil(corr), c.LogicalCallID)
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

// railDegradeReason is the closed sentinel the route ended on, or none.
//
// A SENTINEL, never a provider's message: degrade_reason reaches an ordinary
// rep, and vendor error text carries provider detail and can echo credential
// material. The underlying cause is already in the router's own log line.
func railDegradeReason(c Call) *string {
	if c.ErrorSentinel == "" {
		return nil
	}
	reason := c.ErrorSentinel
	return &reason
}

// uuidOrNil renders an optional id for a SQL argument that distinguishes absent
// from zero.
func uuidOrNil(id *ids.UUID) any {
	if id == nil || id.IsZero() {
		return nil
	}
	return *id
}
