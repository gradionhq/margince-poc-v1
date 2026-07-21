// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The enrichment ACCEPT executor (EP05): a human approval of a staged
// scrapeCompany proposal WRITES the accepted fields onto the org the proposal
// named — fill-empty-only, evidence queryable. Redeem-then-execute like every
// 🟡 executor: the single-use redemption is the exactly-once claim, so a
// replayed or re-driven decision applies nothing twice.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// scrapeAcceptEffect builds the approvals.ApprovedEffect compose injects for
// kind "enrich".
func scrapeAcceptEffect(svc *approvals.Service, store *people.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		// The single-use redemption IS the idempotency claim: whoever consumes
		// the approval executes; anyone else finds it consumed.
		if err := svc.Redeem(ctx, approvalID, enrichProposalKind, diffHash); err != nil {
			return err
		}
		orgID, sourceURL, fields, err := people.UnmarshalEnrichment(proposedChange)
		if err != nil {
			return err
		}
		// The write executes as the scrape executor: captured_by = agent:scrape
		// on behalf of the human whose approval released it — that human is on
		// the decision's own audit row, this one carries the machine
		// provenance the 360 renders as "read from the company's site".
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: enrich effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         "agent:scrape",
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		return store.ApplyEnrichment(execCtx, orgID, people.ApplyColdStartProfileInput{
			SourceURL: sourceURL,
			Fields:    fields,
		})
	}
}
