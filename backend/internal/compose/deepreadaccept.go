// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep-read ACCEPT executor (R4): a human approval of a staged
// "deepread" proposal lands the whole read in one transaction — profile
// fields fill-empty like a quick scrape, category facts into
// organization_fact — with human-set values taking precedence on both.
// Redeem-then-execute like every 🟡 executor: the single-use redemption
// is the exactly-once claim, so a replayed or re-driven decision applies
// nothing twice.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// deepReadAcceptEffect builds the approvals.ApprovedEffect compose
// injects for kind "deepread".
func deepReadAcceptEffect(svc *approvals.Service, store *people.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		// The single-use redemption IS the idempotency claim: whoever consumes
		// the approval executes; anyone else finds it consumed.
		if err := svc.Redeem(ctx, approvalID, deepReadProposalKind, diffHash); err != nil {
			return err
		}
		proposal, err := people.UnmarshalDeepRead(proposedChange)
		if err != nil {
			return err
		}
		// The write executes as the deep-read executor: captured_by =
		// agent:deepread on behalf of the human whose approval released it —
		// that human is on the decision's own audit row, this one carries the
		// machine provenance the 360 renders as "read from the company's site".
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: deepread effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         "agent:deepread",
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		return store.ApplyDeepRead(execCtx, proposal)
	}
}
