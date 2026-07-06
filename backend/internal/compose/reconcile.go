// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Overnight follow-up reconciliation wiring (features/07 §8a,
// B-E06.2a): the deals module owns the nightly read pass and the
// approvals module owns the 🟡 morning inbox — this file is the
// cross-module edge between them, injected here like every other one.
// The pass stages kind "deal_follow_up" through the adapter below; a
// human approval releases the confirm effect, which redeems the staging
// and creates the drafted follow-up task through the activities store's
// own gated write path.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// followUpStager adapts the approvals service onto the deals module's
// FollowUpStager seam. The proposal carries no target_version: a
// follow-up is a new activity, independent of the deal's current field
// values, so a concurrent deal edit must not invalidate the human's yes
// (unlike a close-date correction, which overwrites a deal field).
type followUpStager struct {
	svc *approvals.Service
}

func (s followUpStager) HasPendingFollowUp(ctx context.Context, dealID ids.UUID) (bool, error) {
	return s.svc.HasPendingKind(ctx, deals.FollowUpReconcileKind, dealID)
}

func (s followUpStager) StageFollowUp(ctx context.Context, dealID ids.UUID, summary string, proposal deals.FollowUpProposal) error {
	raw, err := json.Marshal(proposal)
	if err != nil {
		return fmt.Errorf("compose: marshal follow-up proposal: %w", err)
	}
	canonical, hash, err := diffhash.Canonical(raw)
	if err != nil {
		return fmt.Errorf("compose: canonicalize follow-up proposal: %w", err)
	}
	_, err = s.svc.Stage(ctx, approvals.StageInput{
		Kind:           deals.FollowUpReconcileKind,
		ProposedChange: canonical,
		DiffHash:       hash,
		TargetType:     "deal",
		TargetID:       dealID,
		Summary:        summary,
	})
	return err
}

// NewFollowUpReconciler assembles the nightly follow-up reconciler for
// the worker process role.
func NewFollowUpReconciler(pool *pgxpool.Pool, log *slog.Logger) *deals.FollowUpReconciler {
	return deals.NewFollowUpReconciler(pool, followUpStager{svc: approvals.NewService(pool)}, log)
}

// followUpConfirmEffect executes an approved follow-up: redeem-then-
// create like every 🟡 executor, then log the drafted (possibly human-
// edited) follow-up task through the activities store — the same
// RBAC-gated write a rep's own "add task" takes. The single-use
// redemption is the exactly-once claim, and the write is additionally
// idempotent on (source_system, source_id) keyed to the approval, so a
// re-driven decision creates nothing twice. It runs as the overnight
// agent on behalf of the deciding human: captured_by=agent:overnight
// (the follow-up is the agent's suggestion), the human is on the
// decision's own audit row.
func followUpConfirmEffect(svc *approvals.Service, store *activities.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.UUID, proposedChange json.RawMessage, diffHash string) error {
		if err := svc.Redeem(ctx, approvalID, deals.FollowUpReconcileKind, diffHash); err != nil {
			return err
		}
		proposal, err := deals.UnmarshalFollowUpProposal(proposedChange)
		if err != nil {
			return err
		}
		due, err := time.Parse(time.DateOnly, proposal.DueDate)
		if err != nil {
			return fmt.Errorf("compose: follow-up due date: %w", err)
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: follow-up effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         "agent:overnight",
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		subject := proposal.Subject
		body := proposal.Body
		sourceSystem := "overnight-reconcile"
		sourceID := approvalID.String()
		_, _, err = store.LogActivity(execCtx, activities.LogActivityInput{
			Kind:         "task",
			Subject:      &subject,
			Body:         &body,
			DueAt:        &due,
			SourceSystem: &sourceSystem,
			SourceID:     &sourceID,
			Source:       "overnight-reconcile",
			Links:        []activities.ActivityLinkInput{{EntityType: "deal", EntityID: proposal.DealID}},
		})
		return err
	}
}
