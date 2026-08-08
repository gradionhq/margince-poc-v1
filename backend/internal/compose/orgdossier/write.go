// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The dossier's model lane, and the floor it degrades to.
//
// What the model adds over the floor is prose. The floor restates each recorded
// field as "Ideal customer: energy-intensive manufacturers." — true, checkable,
// and not a description of a company. A reader opening a page before a call
// wants three sentences about who these people are, which is a thing only a
// writer can produce from the same facts.
//
// What it does NOT add is knowledge. Every sentence still cites a row the
// reader can open, and the shared grounding filter drops any that does not —
// the same filter, applied to both writers, so a deployment with no model shows
// content the model path would also have shown, only plainer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

const dossierSystem = `You describe one company for a salesperson about to talk to them, from a JSON summary of what their CRM has recorded about it.
Return ONLY a JSON object: {"sections":[{"kind":"summary|products_services|markets|buying_center|differentiation|firmographics","sentences":[{"text":"...","nature":"fact","evidence":[{"entity_type":"organization|fact|profile_field","entity_id":"..."}]}]}]}.
The sections answer, in order: what this company is; what they sell; where and to whom; who decides; what they claim sets them apart; their size, age and registration. Omit a section you have nothing real to say in.
Describe THEM. This is not about our relationship with them, our pipeline, or whether they are a good fit — a different surface answers that, and a sentence here about either belongs there instead.
Every sentence is a FACT: it restates something the summary says and cites the record it came from. You are rewriting recorded values as prose a person would read, not drawing conclusions from them. If the summary does not say it, do not write it.
Cite the ids the summary gave you. Every sentence must cite at least one — a sentence you cannot attach a record to is one to leave out.
Put ids ONLY in evidence. An id must never appear in a sentence's text — the reader sees the text, and an id there is unreadable.
Write plainly, one claim per sentence, and never open two sentences with the company name.`

// dossierSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func dossierSystemFor(fence promptfence.Fence) string {
	return dossierSystem + "\n" + fence.Rule("company summary")
}

// DossierRequest builds the one-shot call. Exported so the AI cert case
// measures the request production actually sends rather than a copy of it.
func DossierRequest(in Input) model.Request {
	fence := promptfence.New()
	return model.Request{
		System:   dossierSystemFor(fence),
		Messages: []model.Message{{Role: "user", Content: fence.Wrap(encodeInput(in))}},
		// Deliberately NOT set: the dossier describes the company, and our own
		// context is only ever an input to a judgment about fit. A writer given
		// it would start comparing, which is the other surface's job.
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		SecretStripper: ai.NewSecretStripper(),
	}
}

// WriteDossier assembles the dossier, degrading to the deterministic floor on
// any model-side failure.
//
// The floor is a real answer here, unlike the growth fit's: it describes the
// company from the same fields, just plainly. So a deployment with no lane is
// not missing the surface, and `generated_by` says which of the two wrote it.
func WriteDossier(ctx context.Context, lane Completer, in Input) ([]Section, crmcontracts.WrittenBy, bool) {
	floor := keepGrounded(Deterministic(in), in)
	if lane == nil {
		return floor, crmcontracts.Deterministic, false
	}
	written, err := writeWithModel(ctx, lane, in)
	if err != nil {
		// The declared degrade posture (on_budget_exhausted: degrade), not a
		// swallowed error. A lane that is unavailable, over budget or answering
		// unparseable JSON must not take the page down: the reader gets the
		// floor, labelled as the floor's.
		//
		// The third return says the lane FAILED rather than being absent, so
		// the caller can decline to write this plainer answer over a model's.
		// Without it one transient outage replaces a written dossier with the
		// floor under the CURRENT fingerprint — which then reads as a cache
		// hit forever, until a fact moves or a human asks for a rewrite.
		return floor, crmcontracts.Deterministic, true
	}
	return written, crmcontracts.Model, false
}

func writeWithModel(ctx context.Context, lane Completer, in Input) ([]Section, error) {
	resp, err := lane.Complete(ctx, DossierRequest(in))
	if err != nil {
		return nil, err
	}
	kept, err := ParseDossier(resp.Text, in)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		// Everything was dropped for citing nothing this company holds. The
		// model wrote about something else, and production shows that as no
		// dossier at all — so the floor's plainer answer is the better one.
		return nil, errors.New("the dossier reply cited nothing about this company")
	}
	return kept, nil
}

// ParseDossier decodes the reply and keeps only what the reader can check.
// Exported so the AI cert case measures the parser production runs.
func ParseDossier(reply string, in Input) ([]Section, error) {
	var parsed struct {
		Sections []Section `json:"sections"`
	}
	if err := json.Unmarshal([]byte(ai.Unfence(reply)), &parsed); err != nil {
		return nil, fmt.Errorf("parse the dossier reply: %w", err)
	}
	known := KnownRecords(in)
	bySection := map[string][]claims.Sentence{}
	for _, section := range parsed.Sections {
		kept := claims.Keep(section.Sentences, known, knownNature, natureFact)
		bySection[section.Kind] = append(bySection[section.Kind], kept...)
	}
	// orderedSections emits only the kinds the contract declares, in reading
	// order, so a kind the model invented is dropped there rather than filtered
	// twice. A model asked for six kinds will eventually offer a seventh.
	return orderedSections(bySection), nil
}
