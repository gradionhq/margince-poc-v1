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
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Completer is the model seam: the summarize lane, or nil.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// Sentence is one claim plus the records it was written from.
type Sentence struct {
	Text     string     `json:"text"`
	Evidence []Evidence `json:"evidence"`
}

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

// Evidence points at a record the READER can already open — the brief is
// assembled under their own row scope, so a citation can never name a row
// they would be refused.
type Evidence struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

// briefSystem is the summarize site's prompt.
//
// Two rules do the work. The model may only rewrite the facts it is given —
// a brief that infers is a brief nobody can check — and it must cite, by the
// ids it was handed, so every sentence stays traceable to a record the
// reader can open.
const briefSystem = `You write a two-to-four sentence account brief for a salesperson, from a JSON summary of one account in their CRM.
Return ONLY a JSON object: {"sentences":[{"text":"...","evidence":[{"entity_type":"deal|activity|person|organization","entity_id":"..."}]}]}.
State only what the summary states. Never infer a cause, a mood, an intent or a next step it does not contain.
Cite the ids the summary gave you; a sentence about the account itself cites the organization.
Write plainly, in the reader's second person where natural, and never open with the company name twice.
If the summary names sections_omitted, say nothing about those subjects at all — the reader is not allowed to see them.`

// briefSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func briefSystemFor(fence promptfence.Fence) string {
	return briefSystem + "\n" + fence.Rule("account summary")
}

// Write produces the brief. lane may be nil, which is not an error state:
// it is the deployment saying this role runs no model, and the
// deterministic floor is the answer.
func Write(ctx context.Context, lane Completer, orgID string, in Input) ([]Sentence, crmcontracts.WrittenBy, error) {
	deterministic := Deterministic(orgID, in)
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

// BriefRequest builds the one request this site sends. Exported because the
// certification case issues the SAME request production does — a case that
// rebuilt it would measure a copy, and a copy stays green through the change
// that breaks the original.
//
// The account summary carries activity subjects and contact names — text
// written by people outside this workspace. It is fenced with a nonce that
// writer has never seen, so no subject line can close the span and be read
// as instruction.
func BriefRequest(in Input) model.Request {
	fence := promptfence.New()
	return model.Request{
		System:         briefSystemFor(fence),
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
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &reply); err != nil {
		return nil, fmt.Errorf("parse the brief reply: %w", err)
	}
	return keepGroundedSentences(reply.Sentences, orgID, in), nil
}

func writeWithModel(ctx context.Context, lane Completer, orgID string, in Input) ([]Sentence, error) {
	req := BriefRequest(in)
	resp, err := lane.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	kept, err := ParseBrief(resp.Text, orgID, in)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		return nil, errors.New("the brief reply cited nothing in the account")
	}
	return kept, nil
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
func keepGroundedSentences(sentences []Sentence, orgID string, in Input) []Sentence {
	known := knownRecords(orgID, in)
	kept := make([]Sentence, 0, len(sentences))
	for _, sentence := range sentences {
		if strings.TrimSpace(sentence.Text) == "" || len(sentence.Evidence) == 0 {
			continue
		}
		if !allGrounded(sentence.Evidence, known) {
			// The WHOLE sentence goes, not just the bad citation. A sentence
			// citing one real record and one invented one is a sentence whose
			// claim may rest on the invented half — keeping it with the good
			// citation attached would present it as checked when it is not.
			continue
		}
		kept = append(kept, sentence)
	}
	return kept
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

func allGrounded(evidence []Evidence, known map[Evidence]bool) bool {
	for _, cited := range evidence {
		if !known[cited] {
			return false
		}
	}
	return true
}
