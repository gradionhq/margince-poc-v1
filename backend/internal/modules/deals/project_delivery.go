// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Winning a deal starts the delivery it was sold for.
//
// A project is not born when a deal is won — it exists from `initiative`, is
// already accumulating conversations and leads while the deal is still being
// pursued, and outlives the deal that funded this round of it. So the win is a
// TRANSITION on a project that is already there, not the project's creation.
//
// It runs INSIDE the transaction that wins the deal, for the same reason the
// correspondence stamp does: a phase move that landed afterwards would leave a
// window in which the deal reads as won and the project still reads as being
// pursued, and every dashboard and brief that joins the two would report the
// contradiction as fact. The two states are one fact and they commit together.
//
// deals owns both records, so this needs no seam — it calls the same
// recordPhaseTransition every human-driven phase move goes through, which is
// what keeps the phase, its history row and project.phase_changed inseparable.
//
// Every guard here decides UNDER the project's row lock. A guard that reads
// the phase before locking is a check-then-act race: the window between the
// read and the write is long enough for a human to close the project, and the
// stale patch would then reinstate `delivering` over their close and append a
// history row for a transition that never happened.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// PhasePursuing is the phase a project sits in while a deal for it is in
// flight — the one the win moves it out of.
const PhasePursuing = "pursuing"

// PhaseDelivering is where a won deal puts its project: the work is now owed,
// not sought.
const PhaseDelivering = "delivering"

// deliveryEvidence is what the audit row records in place of a project.update
// check that never ran. audit_log.authorization_rule is derived from the
// entity and action, so this row reads `project.update` whatever the caller
// actually held — and on this path the caller was admitted by deal.update and
// the project's own row scope was deliberately not consulted. Saying so in
// evidence is the only way the ledger stops overclaiming.
var deliveryEvidence = map[string]any{
	"authorized_by":      "deal.update",
	"project_row_scope":  "not_checked",
	"transition_trigger": "deal_won",
}

// startDeliveryForWonDeal advances the project a just-won deal belongs to into
// `delivering`, inside the transaction that won it. Every case it declines to
// act on is a legitimate state of the world rather than a failure, so it
// returns only an error: there is no outcome the win path would do anything
// differently about.
//
// The advance is deliberately narrow, because a project carries several deals
// over years and a naive "won implies delivering" would rewrite history:
//
//   - the deal names no project — nothing to advance. Creating one, and
//     guessing which existing one a projectless deal meant, are separate
//     questions with their own answers.
//   - the project is archived — the grouping was ended deliberately, and a
//     win does not resurrect it. A no-op, never an error: failing here would
//     roll back the win itself over somebody else's archive.
//   - the project is already `delivering` — a second deal landing on work
//     already under way is not a transition, and recording one would claim a
//     restart that never happened.
//   - the project is `closed` — a renewal that closes in year three must not
//     silently re-open an engagement somebody deliberately ended. Re-opening
//     is a decision a human makes with the reason in hand, and this path has
//     no reason to offer; it does nothing and leaves the two states honestly
//     disagreeing, which is visible, rather than acting and being wrong
//     invisibly.
//
// The move is not gated on the caller's write authority over the project. The
// human's authority to win the deal is what authorizes it, exactly as it
// authorizes the correspondence stamp: a rep closing their own deal must not
// have the win refused because the delivery project belongs to another team.
// changed_by records the human who won, because that is who caused it — there
// is no separate system principal here to invent. The audit row carries
// deliveryEvidence so the ledger names that authority rather than implying a
// project grant.
//
// dealID must name a deal row this transaction already holds: the project
// pointer is re-read from it here rather than taken from a pre-lock snapshot,
// because a concurrent edit that repoints the deal from one project to another
// would otherwise send this advance at the project the deal no longer names.
func startDeliveryForWonDeal(ctx context.Context, tx pgx.Tx, dealID ids.DealID, by string) error {
	projectID, err := lockedDealProject(ctx, tx, dealID)
	if err != nil || projectID == nil {
		return err
	}
	id := ids.From[ids.ProjectKind](*projectID)
	// The lock comes FIRST and the phase is read under it, so the decision
	// below cannot go stale between reading and writing. A row the filter
	// cannot resolve is an archived project, which is a no-op.
	if _, err := storekit.LockRow(ctx, tx, projectObject, id.UUID, storekit.LiveOnly); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("lock the won deal's project: %w", err)
	}
	// A decision read, not a wire read — no custom columns needed, and the
	// project this deal already points at needs no row-scope probe: the FK
	// proves it is in this workspace, and the caller's authority came from the
	// deal.
	current, err := readProject(ctx, tx, id, storekit.LiveOnly, nil)
	if err != nil {
		return fmt.Errorf("read the won deal's project: %w", err)
	}
	if current.Phase == nil {
		return fmt.Errorf("project %s has no phase", id.UUID)
	}
	fromPhase := string(*current.Phase)
	if fromPhase != PhaseInitiative && fromPhase != PhasePursuing {
		return nil
	}
	return recordPhaseTransition(ctx, tx, id, current, fromPhase,
		AdvanceProjectPhaseInput{ToPhase: PhaseDelivering}, by, deliveryEvidence)
}

// lockedDealProject reads the project pointer off a deal row the caller's
// transaction already holds — the win path calls this only after its patch has
// applied, and applying is what takes the lock. Nil means the deal names no
// project.
func lockedDealProject(ctx context.Context, tx pgx.Tx, dealID ids.DealID) (*ids.UUID, error) {
	var projectID *ids.UUID
	if err := tx.QueryRow(ctx,
		`SELECT project_id FROM deal WHERE id = $1`, dealID).Scan(&projectID); err != nil {
		return nil, fmt.Errorf("read the won deal's project pointer: %w", err)
	}
	return projectID, nil
}
