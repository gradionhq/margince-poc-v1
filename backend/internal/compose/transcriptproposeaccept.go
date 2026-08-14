// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What confirming a transcript-derived next step actually does.
//
// Nothing until here: the reading staged a question and wrote no task, no deal
// change and no contact (GATE-AI-2). This is the only place a transcript
// proposal becomes a row, and it is the same RBAC-gated write a rep's own "add
// task" takes — the confirmation grants the act, it does not bypass the gate.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// transcriptProposalSourceSystem keys the created task to the decision that
// created it. Together with SourceID = the approval id it is the second of two
// independent idempotency claims (the first being the single-use redemption),
// so a re-driven decision creates nothing twice.
const transcriptProposalSourceSystem = "transcript-proposal"

// transcriptProposalActor is who the created task is captured by. The next step
// is the reader's suggestion, not the confirming human's own note — the human
// is on the decision's audit row, which is where "who approved this" belongs.
const transcriptProposalActor = "agent:transcript-proposer"

// transcriptProposalEffect executes an approved next step: redeem-then-create
// like every 🟡 executor, then log the (possibly human-edited) task through the
// activities store. The single-use redemption is the exactly-once claim, and
// the write is additionally idempotent on (source_system, source_id).
func transcriptProposalEffect(svc *approvals.Service, store *activities.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		if _, _, err := svc.Redeem(ctx, approvalID, TranscriptProposalKind, diffHash); err != nil {
			return err
		}
		proposal, err := UnmarshalTranscriptStepProposal(proposedChange)
		if err != nil {
			return err
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: transcript proposal effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         transcriptProposalActor,
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		subject := proposal.Summary
		body := transcriptTaskBody(proposal)
		sourceSystem := transcriptProposalSourceSystem
		sourceID := approvalID.String()
		_, _, err = store.LogActivity(execCtx, activities.LogActivityInput{
			Kind:         "task",
			Subject:      &subject,
			Body:         &body,
			SourceSystem: &sourceSystem,
			SourceID:     &sourceID,
			Source:       transcriptProposalSourceSystem,
			Links:        proposal.Links,
		})
		return err
	}
}

// transcriptTaskBody says where the task came from, in the terms the rep can
// go and check: whose commitment it was, and which lines of which transcript
// said so. The task outlives the approval it came from, so the provenance is
// written into it rather than left as a link to a row that may be swept.
func transcriptTaskBody(proposal TranscriptStepProposal) string {
	lines := make([]string, 0, len(proposal.SourceLines))
	for _, line := range proposal.SourceLines {
		lines = append(lines, strconv.Itoa(line))
	}
	where := "line " + strings.Join(lines, ", ")
	if len(lines) > 1 {
		where = "lines " + strings.Join(lines, ", ")
	}
	return fmt.Sprintf("%s committed to this in the meeting transcript (%s).", proposal.Owner, where)
}
