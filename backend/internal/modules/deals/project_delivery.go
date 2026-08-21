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

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
// is no separate system principal here to invent.
func startDeliveryForWonDeal(ctx context.Context, tx pgx.Tx, projectID *openapi_types.UUID, by string) error {
	if projectID == nil {
		return nil
	}
	id := ids.From[ids.ProjectKind](ids.UUID(*projectID))
	// A decision read, not a wire read — no custom columns needed, and the
	// project this deal already points at needs no row-scope probe: the FK
	// proves it is in this workspace, and the caller's authority came from the
	// deal.
	current, err := readProject(ctx, tx, id, storekit.LiveOnly, nil)
	if errors.Is(err, apperrors.ErrNotFound) {
		// The deal points at an archived project. The grouping was ended
		// deliberately; the win does not resurrect it.
		return nil
	}
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
		AdvanceProjectPhaseInput{ToPhase: PhaseDelivering}, by)
}
