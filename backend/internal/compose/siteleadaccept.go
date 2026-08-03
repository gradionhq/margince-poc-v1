// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The site_lead ACCEPT executor (R5): a human approval of one staged
// published person captures them as a LEAD through the one capture Sink —
// never directly a person (ADR-0008: leads graduate; the Sink's own
// cross-source email dedupe stages the 🟡 merge when the person later
// emails in, and that staged merge is the promotion trigger, not this
// effect). Redeem-then-execute like every 🟡 executor: the single-use
// redemption is the exactly-once claim, and the Sink's natural key makes
// a re-read's re-accept land on the same lead row.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// siteLeadCapturedBy is the executor identity the captured lead carries:
// the Sink requires captured_by to equal the acting principal's id, so
// the one spelling covers both.
const siteLeadCapturedBy = "agent:siteread"

// siteLeadAcceptEffect builds the approvals.ApprovedEffect compose
// injects for kind "site_lead".
func siteLeadAcceptEffect(svc *approvals.Service, sink connector.Sink) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		// The single-use redemption IS the idempotency claim on the APPROVAL;
		// the Sink's natural key is the idempotency claim on the LEAD.
		if _, _, err := svc.Redeem(ctx, approvalID, siteLeadProposalKind, diffHash); err != nil {
			return err
		}
		var proposal siteLeadProposal
		if err := json.Unmarshal(proposedChange, &proposal); err != nil {
			return fmt.Errorf("compose: decoding the site_lead proposal: %w", err)
		}
		// A proposal staged before the payload carried its natural key still
		// has to land on the right lead — and an empty key would collide every
		// such person onto ONE row. It is derivable from what those payloads do
		// carry, so derive it rather than refusing a decision a human already
		// made.
		if proposal.NaturalKey == "" {
			proposal.NaturalKey = siteLeadSourceID(
				proposal.OrganizationID, proposal.Name, proposal.PublishedEmail)
		}
		// The capture executes as the siteread executor on behalf of the
		// human whose approval released it. The Sink admits connector
		// principals only, so the executor is one — carrying the decider's
		// own live RBAC and row scope, exactly like the capture registry
		// builds a connector principal from its granting human: the effect
		// can capture nothing the deciding human could not.
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: site_lead effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:        principal.PrincipalConnector,
			ID:          siteLeadCapturedBy,
			UserID:      decider.UserID,
			OnBehalfOf:  decider.UserID,
			TeamIDs:     decider.TeamIDs,
			SeatType:    decider.SeatType,
			Permissions: decider.Permissions,
		})
		// PublishedEmail may be empty — the Sink tolerates an email-less
		// lead (the column goes NULL and the email dedupe is skipped); the
		// natural key alone keeps the capture idempotent. The staged
		// proposal itself is the raw original: it carries the role, the
		// evidence snippet, the source URL, and the org the read targeted.
		_, err := sink.Upsert(execCtx, connector.NormalizedRecord{
			EntityType: datasource.EntityLead,
			NaturalKey: connector.NaturalKey{
				SourceSystem: "siteread",
				// The key the proposal was STAGED under, so the row a human
				// accepts is the row the inbox deduplicated on. Recomputing it
				// here would agree only by both sides calling the same
				// function, and would drift the moment one of them changed.
				SourceID: proposal.NaturalKey,
			},
			Fields: capture.LeadFields{
				FullName: proposal.Name,
				Email:    proposal.PublishedEmail,
				Title:    proposal.Role,
			},
			Source:     "siteread",
			CapturedBy: siteLeadCapturedBy,
			Raw:        proposedChange,
		})
		return err
	}
}
