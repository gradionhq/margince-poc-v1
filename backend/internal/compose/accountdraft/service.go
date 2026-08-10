// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// The service: one gated composite read, one grounded write, no transaction.
//
// There is no pool here and no store with a write method, which is the
// zero-write guarantee stated as a dependency rather than as a rule. A
// contributor who wanted this endpoint to persist something would have to add
// a field to this struct first.

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Assembler is the caller's own composite read of the account — the same seam
// the brief uses, for the same reason: a draft may only mention records this
// caller could open, and one gated read already answers that.
type Assembler interface {
	Assemble(ctx context.Context, orgID ids.OrganizationID) (crmcontracts.Organization360, error)
}

// Request is the transport's body, narrowed to what the writer needs.
type Request struct {
	PersonID string
	DealID   string
	Intent   string
	// Sender is deliberately NOT here and not resolved server-side. The draft
	// carries no sign-off: the composer knows who is signed in and puts their
	// name on it, and a server that guessed would sometimes sign a message
	// with the wrong person's name — the one error a rep notices last and
	// cares about most.
}

// Service writes one draft per call.
type Service struct {
	view Assembler
	lane Completer
}

// NewService binds the draft to the composite read it is grounded in and the
// model lane that writes it. lane may be nil: that is a deployment running no
// model, and the deterministic floor is the answer.
func NewService(view Assembler, lane Completer) *Service {
	return &Service{view: view, lane: lane}
}

// Draft writes one email. It performs no write of any kind.
func (s *Service) Draft(
	ctx context.Context, orgID ids.OrganizationID, req Request,
) (crmcontracts.AccountEmailDraft, error) {
	// Human-only: drafting spends the workspace's model budget on prose for a
	// person to send under their own name.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	// The gates that matter run HERE, in the caller's own composite read: an
	// account they cannot read refuses before a word is written, and a contact
	// or deal they cannot see is not in the view to be found.
	view, err := s.view.Assemble(ctx, orgID)
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	in, err := FromView(view, req)
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	draft, by, err := Write(ctx, s.lane, in)
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	return wire(draft, by), nil
}

// wire maps the written draft onto the contract.
//
// draft_ref is deliberately absent. The reply drafter returns one so the voice
// model can learn from what the rep changed — and recording a served draft is
// a WRITE, which this operation does not perform.
func wire(draft Draft, by crmcontracts.WrittenBy) crmcontracts.AccountEmailDraft {
	aiWritten := by == crmcontracts.Model
	out := crmcontracts.AccountEmailDraft{
		Subject:     draft.Subject,
		Body:        draft.Body,
		GeneratedBy: by,
		AiGenerated: &aiWritten,
		Reasoning:   wireReasons(draft.Reasoning),
	}
	if len(draft.To) > 0 {
		to := make([]openapi_types.Email, 0, len(draft.To))
		for _, address := range draft.To {
			to = append(to, openapi_types.Email(address))
		}
		out.To = &to
	}
	if aiWritten {
		disclosure := aiDisclosure
		out.AiDisclosure = &disclosure
	}
	return out
}

// The machine-readable Art. 50 line, the same sentence the reply drafter
// stamps. Written once here rather than assembled per call: a disclosure that
// varies by call site is one a reader learns to skim.
const aiDisclosure = "This message was drafted with AI assistance."

func wireReasons(reasons []Reason) []crmcontracts.AccountDraftReason {
	out := make([]crmcontracts.AccountDraftReason, 0, len(reasons))
	for _, reason := range reasons {
		wired := crmcontracts.AccountDraftReason{Kind: reason.Kind, Label: reason.Label}
		if reason.EntityID != "" {
			id, err := ids.Parse(reason.EntityID)
			if err != nil {
				// An id that will not parse cannot be opened, so the chip would
				// lead nowhere. The reason still stands without its citation.
				out = append(out, wired)
				continue
			}
			wired.EvidenceRef = &crmcontracts.OrganizationBriefEvidence{
				EntityType: crmcontracts.OrganizationBriefEvidenceEntityType(reason.EntityType),
				EntityId:   openapi_types.UUID(id),
			}
		}
		out = append(out, wired)
	}
	return out
}

// fieldError is the one refusal shape this package answers with, so a bad
// person_id and a bad deal_id read the same way to a client.
func fieldError(field, message string) error {
	return httperr.Validation(field, "not_found", message)
}
