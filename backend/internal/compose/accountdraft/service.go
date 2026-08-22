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

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Assembler is the caller's own composite read of the account — the same seam
// the brief uses, for the same reason: a draft may only mention records this
// caller could open, and one gated read already answers that. The scoped
// form is what a draft about one project reads: correspondence filed under
// another project is not in the view to be drawn on.
type Assembler interface {
	AssembleScoped(ctx context.Context, orgID ids.OrganizationID, opts org360.AssembleOptions) (crmcontracts.Organization360, error)
}

// Request is the transport's body, narrowed to what the writer needs.
type Request struct {
	PersonID string
	DealID   string
	// ProjectID names the body of work the message is about. When set, the
	// draft is grounded in the 360 scoped to that project and the project's
	// own facts are folded in; empty is the account in general.
	ProjectID *ids.ProjectID
	Intent    string
	// Envelope is the correspondence this draft opens: the language it must be
	// written in, the current time, and who is writing it. Resolved by the
	// service, never by the model.
	//
	// The sender identity is what tells the model who "I" is. It is NOT a
	// sign-off: the draft still carries none, because the composer knows who is
	// signed in and a server that guessed would sometimes sign a message with
	// the wrong person's name.
	Envelope draftfloor.Envelope
}

// Dossier answers what an account is KNOWN TO BE, from facts already
// assembled — as distinct from how it stands with us, which the 360 carries.
//
// It is a READ and must never generate: a rep pressing "Write email" has not
// asked for a dossier, and a drafting screen that stalls behind one has spent
// the workspace's model budget on something nobody requested. A cold cache
// answers nothing and the draft is written without these facts.
type Dossier interface {
	CachedSections(ctx context.Context, orgID ids.OrganizationID) []string
}

// Service writes one draft per call.
type Service struct {
	view     Assembler
	lane     Completer
	envelope *draftfloor.Resolver
	dossier  Dossier
}

// WithDossier feeds the draft what the company IS, from a dossier already
// assembled for this reader. Without it the field stays empty, which is what it
// was before: declared on the input, advertised in the prompt, and never
// populated by anything.
func (s *Service) WithDossier(dossier Dossier) *Service {
	s.dossier = dossier
	return s
}

// facts is what this account is known to be, or nothing.
func (s *Service) facts(ctx context.Context, orgID ids.OrganizationID) []string {
	if s.dossier == nil {
		return nil
	}
	return s.dossier.CachedSections(ctx, orgID)
}

// WithEnvelope replaces the resolver that answers what language to write in,
// what time it is and who is signing. Compose binds one carrying the real
// identity lookup; the default resolves a language and a time but no sender,
// so drafts are unsigned rather than failing (DRAFT-AC-E-6).
func (s *Service) WithEnvelope(resolver *draftfloor.Resolver) *Service {
	if resolver != nil {
		s.envelope = resolver
	}
	return s
}

// envelopeFor resolves the correspondence this draft opens.
//
// The band comes from the account's own history rather than being forced to
// BandNone. "Account-started" describes where the draft was requested from —
// a company page with no anchoring activity — not a claim that nobody at the
// company has ever been written to. Forcing the first-touch band would tell an
// account we have corresponded with for a year that we are writing for the
// first time, which is as false as the "just following up" this program set out
// to remove, only in the other direction.
func (s *Service) envelopeFor(ctx context.Context, view crmcontracts.Organization360) draftfloor.Envelope {
	return s.envelope.Resolve(ctx, CorrespondenceText(view), ConversationState(view, s.envelope.Now()))
}

// NewService binds the draft to the composite read it is grounded in and the
// model lane that writes it. lane may be nil: that is a deployment running no
// model, and the deterministic floor is the answer.
func NewService(view Assembler, lane Completer) *Service {
	return &Service{view: view, lane: lane, envelope: draftfloor.NewResolver()}
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
	// account they cannot read refuses before a word is written, a contact
	// or deal they cannot see is not in the view to be found, and a project
	// they cannot see refuses the scoped read itself (activities.RequireProjectScope).
	view, err := s.view.AssembleScoped(ctx, orgID, org360.AssembleOptions{ProjectID: req.ProjectID})
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	req.Envelope = s.envelopeFor(ctx, view)
	in, err := FromView(view, req)
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	in.Dossier = s.facts(ctx, orgID)
	draft, by, err := Write(ctx, s.lane, in)
	if err != nil {
		return crmcontracts.AccountEmailDraft{}, err
	}
	out := wire(draft, by)
	// The scoped read's own report of what the narrowing kept, so the
	// composer's scope line counts what the draft was actually written from.
	out.Scope = view.Scope
	return out, nil
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
