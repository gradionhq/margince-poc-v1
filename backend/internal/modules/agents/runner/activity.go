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
	"encoding/json"
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

// The projection's own state vocabulary, named here because this module writes
// it. They are deliberately NOT the runner's column values — runner_job says
// "done" where agent_run says "completed" — and a reader needs one word for one
// idea.
// statusAwaitingApproval is agent_run's own column value for a run parked on a
// human decision. Named here because two rules read it — the state mapping and
// the lease — and a literal in either is one typo from a run that either cannot
// be announced or is leased when it must not be.
const statusAwaitingApproval = "awaiting_approval"

const (
	stateQueued   = "queued"
	stateRunning  = "running"
	stateDone     = "done"
	stateDegraded = "degraded"
	stateFailed   = "failed"
)

// ProjectionStateFor is the exported reader of the mapping below, so a
// root-package fitness test can hold it TOTAL over the column's CHECK without
// this module exporting a map for anyone to edit.
func ProjectionStateFor(status string) (string, bool) {
	state, ok := runProjectionState[status]
	return state, ok
}

// runProjectionState maps agent_run.status onto that vocabulary.
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
var runProjectionState = map[string]string{
	"running":              stateRunning,
	statusAwaitingApproval: stateRunning,
	"completed":            stateDone,
	"degraded":             stateDegraded,
	"failed":               stateFailed,
}

// occurrence is one trigger occurrence, as the projection needs to hear about
// it. Every field is read from the row that just changed, so no call site can
// disagree with another about what it wrote.
type occurrence struct {
	spec       string
	triggerRef string
	state      string
	// waitingOnAHuman suppresses the lease. A suspended run reports as running —
	// the agent is still working on the reader's behalf — but it is waiting on a
	// PERSON, and the abandoned-run sweep deliberately never touches it ("may
	// wait indefinitely"). Leasing it would have the rail call it stalled half
	// an hour into a perfectly healthy wait, which is a verdict the server never
	// reaches and cannot act on.
	waitingOnAHuman bool
	// attempt is the row's own, and it rises only where a run legitimately gets
	// a SECOND terminal state — the abandoned-run sweep failing it, then the
	// slow worker correcting it. A queued job carries none and reports 1.
	attempt    int
	passportID *ids.PassportID
	startedAt  *time.Time
	finishedAt *time.Time
	// degradeReason is one of the runner's OWN closed reasons. It is never a
	// provider's or a parser's message: those carry vendor text and can echo
	// credential material, and this column reaches an ordinary rep.
	degradeReason *string
	// summary is the run's OWN prose, pulled out of the result the model wrote.
	// It is what the rail shows under a finished run, and it is optional by
	// construction: nothing requires a finishing run to write one.
	summary *string
}

// summaryOf pulls the run's prose out of the result column. SaveOutcome stores
// the model's `final` object verbatim, so the summary sits at its top level.
//
// Any shape this does not recognise yields no summary rather than an error: a
// finishing run is never required to write one, and a degrade writes a partial-
// state object that has none at all. A malformed result is a run that produced
// no summary, not a broken announcement of everything else about it.
func summaryOf(result []byte) *string {
	if len(result) == 0 {
		return nil
	}
	var final struct {
		Summary *string `json:"summary"`
	}
	if err := json.Unmarshal(result, &final); err != nil {
		return nil
	}
	return final.Summary
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

// attemptOrFirst is the row's attempt, defaulting to the first.
//
// A queued job has no run row to carry one, and it is always the occurrence's
// beginning, so 1 is the truth rather than a fallback.
func (o occurrence) attemptOrFirst() int {
	if o.attempt < 1 {
		return 1
	}
	return o.attempt
}

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
	queued, err := queuedAt(ctx, tx, o)
	if err != nil {
		return err
	}
	task := ActivityAITask
	payload := crmcontracts.InternalEventAiTaskStateChanged{
		Source:        ActivitySource,
		OccurrenceKey: o.key(),
		// The spec's catalog name is the display kind: the rail's copy is keyed
		// on the same string the operator reads in the catalog, so a spec with
		// no copy renders no line rather than a wrong one.
		Kind:          o.spec,
		AiTask:        &task,
		Attempt:       o.attemptOrFirst(),
		State:         o.state,
		QueuedAt:      queued,
		StartedAt:     o.startedAt,
		FinishedAt:    o.finishedAt,
		LeaseSeconds:  o.lease(),
		DegradeReason: o.degradeReason,
		Summary:       o.summary,
	}
	if err := storekit.EmitPipelinePayload(ctx, tx, ledgerID, payload); err != nil {
		return fmt.Errorf("runner: publish activity state change: %w", err)
	}
	return nil
}

// lease is how long this occurrence stays believable while live, or none.
//
// None for a settled occurrence — it is not claiming to work — and none for one
// waiting on a human, because nothing will ever time that out.
func (o occurrence) lease() *int {
	if o.waitingOnAHuman || o.state != stateQueued && o.state != stateRunning {
		return nil
	}
	seconds := int(RunStaleAfter.Seconds())
	return &seconds
}

// queuedAt is when this occurrence became current, which the projection ages a
// live row from. A claimed occurrence dates from its claim; a queued one has
// only the instant it was seeded.
//
// Both come from the DATABASE, and that is the point rather than tidiness:
// stale_after is derived from this and compared against the database's now() at
// read time, so a value stamped on a worker host whose clock had drifted would
// decide when an occurrence reads stalled by the size of the drift.
func queuedAt(ctx context.Context, tx pgx.Tx, o occurrence) (time.Time, error) {
	if o.startedAt != nil {
		return *o.startedAt, nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("runner: reading the database clock: %w", err)
	}
	return now, nil
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
