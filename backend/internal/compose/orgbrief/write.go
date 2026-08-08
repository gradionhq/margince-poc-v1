// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// Turning the assembled input into a brief — two ways, one of which always
// works.
//
// Deterministic first, because it is the floor: no model lane configured,
// budget exhausted, or a reply the validator refuses, and the reader still
// gets a brief. The model lane rewrites the same facts more readably; it
// never adds one. Both paths cite the same records, so a sentence is
// checkable whichever wrote it.

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

// Completer is the model seam: the summarize lane, or nil.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// Sentence is one claim plus the records it was written from — the SHARED shape
// (internal/compose/claims), because the brief, the dossier and growth fit all
// render claims and one grounding rule over one shape is the point.
type Sentence = claims.Sentence

// Evidence points at a record the READER can already open, and is shared for
// the same reason as Sentence.
type Evidence = claims.Evidence

// The citable record kinds, DERIVED from the contract's own enum rather than
// re-spelled. Both the writer and the grounding filter key on them, and a
// literal copy would let a contract rename leave the filter matching a type
// the wire no longer carries — a citation that silently stops grounding.
var (
	citeOrganization = string(crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization)
	citeDeal         = string(crmcontracts.OrganizationBriefEvidenceEntityTypeDeal)
	citeActivity     = string(crmcontracts.OrganizationBriefEvidenceEntityTypeActivity)
	citePerson       = string(crmcontracts.OrganizationBriefEvidenceEntityTypePerson)
)

// One sentence, one record — the shape rule both writers follow.
//
// A sentence that joined three task names and hung all three citations off
// itself rendered as three chips with nothing between them, and read as
// developer output rather than an answer. So a list of records becomes one
// sentence per record, each citing only the record it is about, and a
// sentence that counts a list cites the one record it names. listedRecords
// bounds the list: past it the reader is scanning, not reading, and the
// count sentence still states the true total.
const listedRecords = 5

// perRecordSentences renders a record list the house way: one sentence per
// record, each carrying its own single citation, stopping at listedRecords.
func perRecordSentences[T any](
	records []T, entityType string, id func(T) string, line func(T) string,
) []Sentence {
	out := make([]Sentence, 0, min(len(records), listedRecords))
	for _, record := range records {
		if len(out) == listedRecords {
			break
		}
		out = append(out, Sentence{
			Text:     line(record),
			Evidence: []Evidence{{EntityType: entityType, EntityID: id(record)}},
		})
	}
	return out
}

// briefSystem is the summarize site's prompt.
//
// The brief is allowed to JUDGE now, which it was not before, and one rule
// makes that safe: every claim is labelled with what kind of claim it is. A
// fact restates the summary and cites its record. An assessment is a judgment
// drawn against our own company profile, said plainly and labelled, still
// citing what supports it. The reader can therefore tell a stored fact from a
// reading of one, which is the whole difference between a brief they can check
// and a brief they must trust.
//
// The company context describes US and is never citable — our own profile is
// not a record the reader can open, and pretending otherwise would invent a
// citation to make a recommendation look grounded.
const briefSystem = `You write a pre-meeting account briefing for a salesperson, from a JSON summary of one account in their CRM.
Return ONLY a JSON object: {"sections":[{"kind":"snapshot|fit|health|activity|next_step","sentences":[{"text":"...","nature":"fact|assessment|recommendation","evidence":[{"entity_type":"deal|activity|person|organization|fact","entity_id":"..."}]}]}]}.
The sections answer, in order: what this company is; why it matters to US; how the relationship stands; what actually happened; what to do next. Omit a section you have nothing real to say in.
Label every sentence. A FACT restates what the summary says and cites the record it came from. An ASSESSMENT is a judgment you draw by combining the summary with the company context — say it plainly, and cite the records that support it. A RECOMMENDATION is one concrete move; cite the account-side record that motivates it.
Facts may appear in any section. Assessments belong only in fit and health. Recommendations belong only in next_step, and there are at most two.
Never invent a fact. If the summary does not say it, you may still ASSESS it — but then it is an assessment and must be labelled one.
The company context describes US, the people reading this. It is never a fact about THEM, and never a citation: our own profile is not a record the reader can open.
Cite the ids the summary gave you. A sentence about the account itself cites the organization.
Put ids ONLY in evidence. An id must never appear in a sentence's text — the reader sees the text, and an id there is unreadable.
Write one claim per sentence, plainly, in the reader's second person where natural, and never open with the company name twice.
If the summary names sections_omitted, say nothing about those subjects at all — the reader is not allowed to see them.`

// briefSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func briefSystemFor(fence promptfence.Fence) string {
	return briefSystem + "\n" + fence.Rule("account summary")
}

// Write produces the brief. lane may be nil, which is not an error state:
// it is the deployment saying this role runs no model, and the
// deterministic floor is the answer.
func Write(ctx context.Context, lane Completer, orgID string, in Input) ([]Section, crmcontracts.WrittenBy, error) {
	deterministic := DeterministicSections(orgID, in)
	if lane == nil {
		return deterministic, crmcontracts.Deterministic, nil
	}
	written, err := writeWithModel(ctx, lane, orgID, in)
	if err != nil {
		// The declared degrade posture, not a swallowed error. A model that
		// is unavailable, over budget, or answering unparseable JSON must
		// not take the card down with it: the reader gets the floor, and
		// generated_by tells them which of the two they are reading.
		//nolint:nilerr // on_budget_exhausted: degrade — the fallback IS the answer, and generated_by reports it
		return deterministic, crmcontracts.Deterministic, nil
	}
	return written, crmcontracts.Model, nil
}

// accountEvidence cites the account itself: the company description is about
// the company, not about any one deal or message.
func accountEvidence(orgID string) []Evidence {
	return []Evidence{{EntityType: citeOrganization, EntityID: orgID}}
}

// BriefRequest builds the one request this site sends. Exported because the
// certification case issues the SAME request production does — a case that
// rebuilt it would measure a copy, and a copy stays green through the change
// that breaks the original.
//
// The account summary carries activity subjects and contact names — text
// written by people outside this workspace. It is fenced with a nonce that
// writer has never seen, so no subject line can close the span and be read
// as instruction.
//
// The company profile is withheld from the request. Appending those lines
// afterwards only guarantees the approved wording APPEARS; a model that had
// read them could still put its own wording next to it, cited to the
// organization and so accepted by the grounding check. Withholding them is
// what makes "the model never rewrites the company description" true of the
// request rather than of the concatenation.
func BriefRequest(in Input) model.Request {
	return groundedRequest(briefSystemFor, in)
}

// groundedRequest is the one request shape both of this package's sites send:
// the assembled account fenced with a nonce minted for THIS call, and a system
// prompt that names that same nonce as the data boundary. systemFor receives
// the fence so the two can never disagree — a request whose prompt named a
// different boundary than the one wrapping the data would fence nothing.
func groundedRequest(systemFor func(promptfence.Fence) string, in Input) model.Request {
	fence := promptfence.New()
	return model.Request{
		System:         systemFor(fence),
		Messages:       []model.Message{{Role: "user", Content: fence.Wrap(encodeInput(in))}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		SecretStripper: ai.NewSecretStripper(),
	}
}

// encodeInput renders the assembled account as the JSON the prompts read.
//
// A summary that cannot be encoded is a programming error, not a runtime one:
// Input is our own struct of scalars and slices. An empty prompt still reaches
// the model fenced, and the grounding filter refuses the reply that comes back.
func encodeInput(in Input) string {
	encoded, _ := json.Marshal(in) //nolint:errchkjson // Input is a plain struct of scalars; marshal cannot fail
	return string(encoded)
}

// ParseBrief reads a model reply into grounded sentences. Exported for the
// same reason as BriefRequest: the certification case must run the filter
// production runs, because that filter is what stands between a reader and a
// sentence about a record they cannot open.
//
// orgID pins the one account this brief is about, so an organization
// citation cannot name a different one.
func ParseBrief(text, orgID string, in Input) ([]Sentence, error) {
	var reply struct {
		Sentences []Sentence `json:"sentences"`
	}
	// ai.Unfence, not a bare TrimSpace: a model that wraps its JSON in a
	// ```json fence is answering correctly, and every other model-reply parser
	// in the tree reduces through the same helper. Trimming whitespace alone
	// drops the whole model lane to the deterministic floor on those providers.
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &reply); err != nil {
		return nil, fmt.Errorf("parse the brief reply: %w", err)
	}
	return keepGroundedSentences(reply.Sentences, orgID, in), nil
}

func writeWithModel(ctx context.Context, lane Completer, orgID string, in Input) ([]Section, error) {
	req := BriefRequest(in)
	resp, err := lane.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	kept, err := ParseBriefSections(resp.Text, orgID, in)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		return nil, errors.New("the brief reply cited nothing in the account")
	}
	return kept, nil
}

// briefSectionOrder is the order a reader asks the questions, and the order
// the sections are rendered in whatever order the model returned them.
var briefSectionOrder = []string{
	sectionSnapshot, sectionFit, sectionHealth, sectionActivity, sectionNextStep,
}

// natureAllowed says which kinds of claim each section may carry.
//
// Facts are welcome anywhere. An ASSESSMENT is a judgment, so it belongs only
// where the brief is meant to judge — what this account is to us, and how the
// relationship stands. A RECOMMENDATION is a move, and a move belongs under
// the heading that promises one; scattered through the narrative it reads as
// the account's history rather than as advice.
var natureAllowed = map[string]map[string]bool{
	sectionSnapshot: {natureFact: true},
	sectionFit:      {natureFact: true, natureAssessment: true},
	sectionHealth:   {natureFact: true, natureAssessment: true},
	sectionActivity: {natureFact: true},
	sectionNextStep: {natureFact: true, natureRecommendation: true},
}

// maxRecommendations bounds the advice. Two moves are a plan; five are a list
// the reader triages, which is the job the brief was supposed to do.
const maxRecommendations = 2

// ParseBriefSections reads a sectioned reply, keeping only what is grounded and
// permitted. Exported for the same reason as BriefRequest: the certification
// case must run the filter production runs.
//
// Everything it drops, it drops for one of three reasons — an unknown section,
// a claim of a kind that section may not carry, or a citation the input never
// held. Each is a sentence the reader could not have checked.
func ParseBriefSections(reply, orgID string, in Input) ([]Section, error) {
	var parsed struct {
		Sections []Section `json:"sections"`
	}
	// ai.Unfence for the same reason ParseBrief uses it: a model that wraps its
	// JSON in a ```json fence is answering correctly.
	if err := json.Unmarshal([]byte(ai.Unfence(reply)), &parsed); err != nil {
		return nil, fmt.Errorf("parse brief sections: %w", err)
	}
	known := knownRecords(orgID, in)
	byKind := map[string][]Sentence{}
	// The quota is on the BRIEF, not on one section: a model that returns two
	// next_step sections would otherwise be handed the allowance twice, and the
	// merged output would carry twice the advice the reader was promised.
	recommendations := 0
	for _, section := range parsed.Sections {
		allowed, ok := natureAllowed[section.Kind]
		if !ok {
			continue
		}
		for _, sentence := range section.Sentences {
			nature := sentence.Nature
			if nature == "" {
				// An unlabelled claim is a fact, which is the strictest
				// reading: it must be grounded and it may not judge.
				nature = natureFact
			}
			if !allowed[nature] {
				continue
			}
			if nature == natureRecommendation && recommendations >= maxRecommendations {
				continue
			}
			sentence.Nature = nature
			// Grounding decides FIRST. Counting an ungrounded recommendation
			// would let one malformed claim spend the quota and suppress the
			// valid advice behind it — the reader loses the advice and is told
			// nothing about why.
			if !claims.Grounded(sentence, known) {
				continue
			}
			if nature == natureRecommendation {
				recommendations++
			}
			byKind[section.Kind] = append(byKind[section.Kind], sentence)
		}
	}
	out := make([]Section, 0, len(briefSectionOrder))
	for _, kind := range briefSectionOrder {
		if sentences := byKind[kind]; len(sentences) > 0 {
			out = append(out, Section{Kind: kind, Sentences: sentences})
		}
	}
	return out, nil
}

// keepGroundedSentences drops any sentence whose citations do not point at
// records this input actually carried.
//
// The reader's trust in the brief is the citation: a sentence pointing at an
// id that was never in the input is either invented or points somewhere the
// reader cannot go, and neither is worth showing. Dropping the sentence is
// the honest response — the remaining ones still say true things.
//
// The ACCOUNT is pinned, not merely allowed by type. Accepting any
// organization citation would let a reply hand back an id this reader never
// saw — rendered as a link they could click into a record their scope may
// hide. The one organization a brief may cite is the one it is about.
// groundedSentence is the ONE test both parsers apply, so the flat answer and
// the sectioned brief can never disagree about what counts as checkable.
func keepGroundedSentences(sentences []Sentence, orgID string, in Input) []Sentence {
	return claims.Keep(sentences, knownRecords(orgID, in), knownNature, natureFact)
}

// knownRecords is what this brief was written from, keyed by TYPE AND ID.
//
// Keying on the id alone accepted a real deal id cited as a person: the id
// passes, and the card then routes the reader to the wrong screen — or to a
// record of a kind they were never shown. The pair is the reference, so the
// pair is what is checked.
func knownRecords(orgID string, in Input) map[Evidence]bool {
	known := map[Evidence]bool{{EntityType: citeOrganization, EntityID: orgID}: true}
	for _, deal := range in.OpenDeals {
		known[Evidence{EntityType: citeDeal, EntityID: deal.ID}] = true
	}
	for _, act := range in.Recent {
		known[Evidence{EntityType: citeActivity, EntityID: act.ID}] = true
	}
	for _, contact := range in.Contacts {
		known[Evidence{EntityType: citePerson, EntityID: contact.ID}] = true
	}
	for _, task := range in.OpenTasks {
		known[Evidence{EntityType: citeActivity, EntityID: task.ID}] = true
	}
	return known
}

// The section kinds, DERIVED from the contract's enum rather than re-spelled,
// so a rename upstream fails to compile here instead of laundering a
// hand-typed string past the type.
const (
	sectionSnapshot = string(crmcontracts.OrganizationBriefSectionKindSnapshot)
	sectionFit      = string(crmcontracts.OrganizationBriefSectionKindFit)
	sectionHealth   = string(crmcontracts.OrganizationBriefSectionKindHealth)
	sectionActivity = string(crmcontracts.OrganizationBriefSectionKindActivity)
	sectionNextStep = string(crmcontracts.OrganizationBriefSectionKindNextStep)
)

// The natures a sentence can carry, same derivation, same reason.
const (
	natureFact           = string(crmcontracts.Fact)
	natureAssessment     = string(crmcontracts.Assessment)
	natureRecommendation = string(crmcontracts.Recommendation)
)

// knownNature is every nature ANY section may carry, derived from natureAllowed
// so the two cannot drift. The flat lane needs it because it has no section
// whose own allow-set would answer the question.
var knownNature = func() map[string]bool {
	all := map[string]bool{}
	for _, allowed := range natureAllowed {
		for nature := range allowed {
			all[nature] = true
		}
	}
	return all
}()

// Section is one part of the brief: a heading's worth of claims about one
// question. The order they arrive in is the order a reader asks them.
type Section struct {
	Kind      string     `json:"kind"`
	Sentences []Sentence `json:"sentences"`
}
