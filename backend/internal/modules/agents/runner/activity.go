// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

// The runner's half of the AI-activity projection: every trigger occurrence
// reports its own state onto the bus, and one consumer projects those into the
// table the rail reads.
//
// Nothing here reads the projection back. The runner does not know that table
// exists; it announces what its own rows say, and what a reader is allowed to
// see is decided at the other end.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// ActivitySource names this source to the projection. Identity, not display:
	// two sources must never collide on one occurrence key.
	ActivitySource = "agent_runner"

	// ActivityAITask is the api/ai-tasks.yaml task a scheduled run performs.
	// Exported so a root-package fitness test can hold it to the generated task
	// set — a module may not import the ai module to assert that about itself.
	ActivityAITask = "agent_loop"

	// RunStaleAfter is how long a live occurrence stays believable.
	//
	// It must not be SHORTER than the sweep's own stuck-run grace, or the rail
	// would call a run stale while the sweep still considers it live — two
	// answers to one question, and the reader gets the wrong one. Held to that
	// by TestTheRailNeverCallsARunStaleBeforeTheSweepWould in compose.
	RunStaleAfter = 30 * time.Minute
)

// runProjectionState maps agent_run.status onto the projection's vocabulary.
//
// TOTAL over the column's CHECK, and held there by a fitness test rather than
// by care: a status this map does not carry would emit an empty state, which
// the projection's own CHECK then refuses — a wedged consumer group rather than
// a missing line.
//
// awaiting_approval reports as RUNNING, and that is a decision rather than a
// gap. The occurrence is open and the agent is still working on the reader's
// behalf; what differs is who it is waiting for, and the approvals inbox is the
// surface that answers that. Neither v1 spec can stage a confirmation, so the
// distinction has no producer today either.
// ProjectionStateFor is the exported reader of that map, so a root-package
// fitness test can hold it TOTAL over the column's CHECK without this module
// exporting the map itself for anyone to edit.
func ProjectionStateFor(status string) (string, bool) {
	state, ok := runProjectionState[status]
	return state, ok
}

var runProjectionState = map[string]string{
	"running":           "running",
	"awaiting_approval": "running",
	"completed":         "done",
	"degraded":          "degraded",
	"failed":            "failed",
}

// occurrence is one trigger occurrence, as the projection needs to hear about
// it. Every field is read from the row that just changed, so no call site can
// disagree with another about what it wrote.
type occurrence struct {
	spec       string
	triggerRef string
	state      string
	passportID *ids.PassportID
	startedAt  *time.Time
	finishedAt *time.Time
	// degradeReason is one of the runner's OWN closed reasons. It is never a
	// provider's or a parser's message: those carry vendor text and can echo
	// credential material, and this column reaches an ordinary rep.
	degradeReason *string
	summary       *string
}

// key is the occurrence identity the projection dedupes on.
//
// The spec name is part of it because runner_job is unique on
// (agent_spec, trigger_ref) while agent_run is unique on trigger_ref alone —
// keyed on the ref by itself, two specs triggered by the same occurrence would
// collapse into one row.
//
// This is also what makes the queued job and its run ONE line rather than two.
// The read used to strip that duplicate in Go, comparing trigger refs after the
// fact; here the table's own UNIQUE (source, occurrence_key) does it, and there
// is no window in which both can be returned.
func (o occurrence) key() string { return o.spec + ":" + o.triggerRef }

// announceActivity publishes one occurrence's current state, inside the
// transaction that produced it.
//
// The ledger row comes first because the bus refuses an entity-less event
// without a trace link: an AI task names no domain record, so the system_log
// row is what keeps the outcome attributable.
func announceActivity(ctx context.Context, tx pgx.Tx, o occurrence) error {
	ctx, err := attributedTo(ctx, tx, o.passportID)
	if err != nil {
		return err
	}
	ledgerID, err := storekit.LogSystem(ctx, tx, "ai_task.state_changed", map[string]any{
		"source": ActivitySource, "occurrence_key": o.key(), "state": o.state,
	})
	if err != nil {
		return fmt.Errorf("runner: log activity state change: %w", err)
	}
	task := ActivityAITask
	lease := int(RunStaleAfter.Seconds())
	payload := crmcontracts.InternalEventAiTaskStateChanged{
		Source:        ActivitySource,
		OccurrenceKey: o.key(),
		// The spec's catalog name is the display kind: the rail's copy is keyed
		// on the same string the operator reads in the catalog, so a spec with
		// no copy renders no line rather than a wrong one.
		Kind:   o.spec,
		AiTask: &task,
		// Always 1. The attempt column orders sources whose lifecycle goes
		// BACKWARDS; this one's does not — a trigger occurrence goes queued to
		// running to settled and a duplicate trigger is absorbed by
		// idempotency, never reopened.
		Attempt:       1,
		State:         o.state,
		QueuedAt:      queuedAt(o),
		StartedAt:     o.startedAt,
		FinishedAt:    o.finishedAt,
		LeaseSeconds:  &lease,
		DegradeReason: o.degradeReason,
		Summary:       o.summary,
	}
	if err := storekit.EmitPipelinePayload(ctx, tx, ledgerID, payload); err != nil {
		return fmt.Errorf("runner: publish activity state change: %w", err)
	}
	return nil
}

// queuedAt is when this occurrence became current, which the projection ages a
// live row from. A claimed occurrence dates from its claim; a queued one has
// only now, because nothing has happened to it yet.
func queuedAt(o occurrence) time.Time {
	if o.startedAt != nil {
		return *o.startedAt
	}
	return time.Now().UTC()
}

// attributedTo puts the human the occurrence belongs to behind the emitting
// principal, resolved from the passport IN THE SAME TRANSACTION.
//
// It is resolved here rather than taken from whatever principal the caller
// happens to hold, because the two are different on the paths that matter: the
// scheduler enqueues under a system principal with nobody behind it, and an
// occurrence announced that way is filed as workspace work its owner can never
// find. The passport is the row's own answer to "whose authority is this".
func attributedTo(ctx context.Context, tx pgx.Tx, passportID *ids.PassportID) (context.Context, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return nil, fmt.Errorf("runner: no actor bound; an activity announcement cannot be attributed")
	}
	if passportID == nil {
		// No passport is a real state — a job seeded before one was bound — and
		// the occurrence belongs to nobody until there is. The projection files
		// it as workspace-scoped and shows it to no one, which is the honest
		// answer rather than a guess.
		p.OnBehalfOf = ids.Nil
		return principal.WithActor(ctx, p), nil
	}
	var onBehalfOf *ids.UUID
	err := tx.QueryRow(ctx, `SELECT on_behalf_of FROM passport WHERE id = $1`, *passportID).Scan(&onBehalfOf)
	if err != nil {
		return nil, fmt.Errorf("runner: resolve the human behind passport %s: %w", *passportID, err)
	}
	if onBehalfOf != nil {
		p.OnBehalfOf = *onBehalfOf
		p.UserID = *onBehalfOf
	}
	return principal.WithActor(ctx, p), nil
}
