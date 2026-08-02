// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The one phase-transition verb. It exists as its own path — rather than a
// settable column on update — so the row change, the history row and the
// first-class project.phase_changed event are written from one
// transaction: "where does this stand" and "how did it get there" can
// never disagree, because nothing can write one without the other.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AdvanceProjectPhaseInput is one transition. Movement is free-form along
// the ladder — a closed project may re-open, and pretending otherwise
// would make the system argue with what actually happened — but every
// transition is recorded, and closing demands a reason.
type AdvanceProjectPhaseInput struct {
	ToPhase   string
	Reason    *string
	IfVersion *int64
}

// AdvanceProjectPhase moves a project along the ladder, writing the row
// change, its history row and project.phase_changed from one transaction.
func (s *Store) AdvanceProjectPhase(ctx context.Context, id ids.ProjectID, in AdvanceProjectPhaseInput) (crmcontracts.Project, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Project{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Project{}, err
	}
	// Closing without a reason is refused here, before the transaction, so
	// the caller gets the rule rather than a constraint name. The schema
	// CHECK stays as the net under it.
	if in.ToPhase == PhaseClosed && (in.Reason == nil || *in.Reason == "") {
		return crmcontracts.Project{}, &ClosedReasonRequiredError{}
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return crmcontracts.Project{}, err
	}

	var out crmcontracts.Project
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, projectObject, id.UUID); err != nil {
			return err
		}
		// A decision read, not a wire read — no custom columns needed.
		current, err := readProject(ctx, tx, id, storekit.LiveOnly, nil)
		if err != nil {
			return fmt.Errorf("read project before advance: %w", err)
		}
		// Phase is never NULL in the schema; a nil pointer here would mean
		// the read shape and the column disagree, which is a fault, not a
		// state to guess at.
		if current.Phase == nil {
			return fmt.Errorf("project %s has no phase", id.UUID)
		}
		fromPhase := string(*current.Phase)
		// Re-asserting the current phase is not a transition: writing a
		// history row for it would inflate the record with movement that
		// never happened.
		if fromPhase != in.ToPhase {
			if err := recordPhaseTransition(ctx, tx, id, current, fromPhase, in, by); err != nil {
				return err
			}
		}
		if out, err = readProject(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read advanced project: %w", err)
		}
		return nil
	})
	return out, err
}

// recordPhaseTransition applies the row change, appends its history row and
// emits the first-class event — the three writes that must land together or
// not at all, which is the whole reason this verb exists apart from update.
func recordPhaseTransition(ctx context.Context, tx pgx.Tx, id ids.ProjectID,
	current crmcontracts.Project, fromPhase string, in AdvanceProjectPhaseInput, by string,
) error {
	p := storekit.NewPatch()
	p.Set("phase", fromPhase, in.ToPhase)
	// closed_reason belongs to the closed state: leaving `closed` clears it,
	// so a re-opened project never carries the explanation of a close that
	// no longer applies.
	switch {
	case in.ToPhase == PhaseClosed:
		p.Set("closed_reason", current.ClosedReason, *in.Reason)
	case current.ClosedReason != nil:
		p.Set("closed_reason", current.ClosedReason, nil)
	}
	if err := p.ApplyGuarded(ctx, tx, projectObject, id.UUID, in.IfVersion); err != nil {
		if constraint, ok := storekit.CheckViolation(err); ok {
			return projectCheckError(constraint, "ended_at")
		}
		return fmt.Errorf("apply project phase patch: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO project_phase_history (workspace_id, project_id, from_phase, to_phase, reason, changed_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		storekit.MustWorkspace(ctx), id, fromPhase, in.ToPhase, in.Reason, by); err != nil {
		return fmt.Errorf("record project phase history: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "advance_phase", projectObject, id.UUID, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit project advance: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, projectPhaseChangedPayload(fromPhase, in)); err != nil {
		return fmt.Errorf("emit project.phase_changed: %w", err)
	}
	return nil
}

// projectPhaseChangedPayload builds the event body in one place, so a
// field rename lands here rather than at every emit site.
func projectPhaseChangedPayload(fromPhase string, in AdvanceProjectPhaseInput) crmcontracts.PublicEventProjectPhaseChanged {
	payload := crmcontracts.PublicEventProjectPhaseChanged{
		FromPhase: &fromPhase,
		ToPhase:   in.ToPhase,
	}
	if in.Reason != nil && *in.Reason != "" {
		payload.Reason = in.Reason
	}
	return payload
}
