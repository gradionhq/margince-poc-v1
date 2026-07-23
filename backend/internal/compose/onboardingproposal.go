// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// GET /onboarding/company/proposal — the deterministic "AI prepared the
// mapping" payload. It reads the acting human's persisted onboarding
// state, loads the site read it references, and serves the
// evidence-carrying fields, the facts, the detectors' open questions,
// and the version/hash pair confirmation must echo. No model call:
// the narrating conversation turn is generated prose whose numbers come
// from HERE.

import (
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// onboardingProposalConfidenceFloor is the spec's cold-start
// evidence-or-omit floor (ai-operational-spec.md: below 0.55 the field is
// omitted, web text is noisier than mail). The read persists every gated
// finding; only findings at or above the floor surface as proposal
// fields.
const onboardingProposalConfidenceFloor = 0.55

// onboardingProposalEngine assembles the proposal from the two seams the
// conversation already reads through: identity's per-user wizard state
// and people's onboarding dossier.
type onboardingProposalEngine struct {
	state   onboardingStateReader
	people  onboardingSiteReadReader
	rollout string
}

func (e *onboardingProposalEngine) get(w http.ResponseWriter, r *http.Request, params crmcontracts.GetOnboardingCompanyProposalParams) {
	if !companyContextOnboardingEnabled(e.rollout) {
		httperr.NotImplemented(w, r, "getOnboardingCompanyProposal (company onboarding disabled)")
		return
	}
	// Locale mirrors the message endpoint's posture: an explicit client
	// concern, defaulting to English when absent.
	locale := string(crmcontracts.OnboardingProposalLocaleEN)
	if params.Locale != nil {
		if !params.Locale.Valid() {
			httperr.Write(w, r, httperr.Validation("locale", "invalid", "locale must be en or de"))
			return
		}
		locale = string(*params.Locale)
	}
	state, err := e.state.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if state.SiteReadID == nil {
		// A state without a read has nothing to propose from — the same
		// 404 shape as "no state yet", not an empty proposal.
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	read, comparisons, err := e.people.GetCompanySiteRead(r.Context(), *state.SiteReadID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, onboardingCompanyProposal(read, comparisons, state.CompanyDraft, locale))
}

// onboardingCompanyProposal maps the dossier onto the contract payload.
// It serves whatever the read has already grounded — a running read's
// progressive draft included — with ready reporting whether the read
// reached a terminal answer.
func onboardingCompanyProposal(read people.SiteRead, comparisons []people.SiteReadComparison, draft identity.OnboardingCompanyDraft, locale string) crmcontracts.OnboardingCompanyProposal {
	fields := make([]crmcontracts.OnboardingCompanyProposalField, 0, len(read.ProfileFields))
	for _, field := range read.ProfileFields {
		if field.Confidence < onboardingProposalConfidenceFloor || strings.TrimSpace(field.EvidenceSnippet) == "" {
			continue
		}
		fields = append(fields, crmcontracts.OnboardingCompanyProposalField{
			Field: field.Field, Value: field.Value, Confidence: field.Confidence,
			EvidenceSnippet: field.EvidenceSnippet, SourceUrl: field.SourceURL,
		})
	}
	facts := make([]crmcontracts.CompanySiteReadFact, 0, len(read.Facts))
	for _, fact := range read.Facts {
		facts = append(facts, crmcontracts.CompanySiteReadFact{
			Category: crmcontracts.CompanySiteReadFactCategory(fact.Category),
			Field:    crmcontracts.CompanySiteReadFactField(fact.Field),
			Value:    fact.Value, ValueKey: people.SiteReadFactKey(fact),
			EvidenceSnippet: fact.EvidenceSnippet, EvidenceUrl: fact.SourceURL,
			Confidence: fact.Confidence,
		})
	}
	// Only still-open questions ship: one the draft already answers with
	// an exact option value was resolved by an earlier authorized
	// selection and must not be re-asked on restore.
	openQuestions := openOnboardingClarifies(read, comparisons, locale, draft)
	if openQuestions == nil {
		openQuestions = []crmcontracts.OnboardingClarify{}
	}
	remaining := remainingOnboardingFields(draft)
	draftVersion := read.DraftVersion
	proposalHash := read.ProposalHash
	return crmcontracts.OnboardingCompanyProposal{
		Ready:                   read.Status == siteReadWireStatusDone || read.Status == siteReadWireStatusPartial,
		Fields:                  &fields,
		Facts:                   &facts,
		OpenQuestions:           &openQuestions,
		RemainingRequiredFields: &remaining,
		DraftVersion:            &draftVersion,
		ProposalHash:            &proposalHash,
	}
}

func (h onboardingStateHandlers) GetOnboardingCompanyProposal(w http.ResponseWriter, r *http.Request, params crmcontracts.GetOnboardingCompanyProposalParams) {
	if h.proposal == nil {
		httperr.NotImplemented(w, r, "getOnboardingCompanyProposal (no proposal engine configured)")
		return
	}
	h.proposal.get(w, r, params)
}
