// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The prepared questions' deterministic floor, and the door in front of it.
//
// The floor is what a deployment with no model lane serves, and what every
// ungrounded model reply degrades to — so it is the answer most readers get
// most often. Each question is checked twice: over an account that can answer
// it, and over one that cannot.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"gopkg.in/yaml.v3"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// openingTag matches the fence's opening marker, whatever promptfence names it
// — the test asserts that the prompt and the wrap agree, not what the marker
// is called.
var openingTag = regexp.MustCompile(`<[a-z-]+-[0-9a-f-]{36}>`)

const askOrgID = "018f0000-0000-7000-8000-000000000001"

func askInput() Input {
	return Input{
		Name:         "Brandt Automotive GmbH",
		Industry:     "Automotive",
		Strength:     41,
		ContactCount: 2,
		Contacts: []NamedIn{
			{ID: "018f0000-0000-7000-8000-0000000000c1", Name: "Dana Buyer"},
		},
		OpenDeals: []DealIn{
			{
				ID: "018f0000-0000-7000-8000-0000000000d1", Name: "Fleet retrofit 2026",
				Stage: "Proposal", AmountMinor: 4_800_000, Currency: "EUR", Stalled: true,
			},
		},
		OpenTasks: []NamedIn{
			{ID: "018f0000-0000-7000-8000-0000000000a2", Name: "Call the CFO"},
		},
		Recent: []ActIn{
			{ID: "018f0000-0000-7000-8000-0000000000a1", Kind: "email", Subject: "Re: proposal", At: "2026-07-10T09:00:00Z"},
			{ID: "018f0000-0000-7000-8000-0000000000a3", Kind: "call", Subject: "Intro", At: "2026-07-02T09:00:00Z"},
			{ID: "018f0000-0000-7000-8000-0000000000a4", Kind: "note", Subject: "Handover", At: "2026-06-20T09:00:00Z"},
			{ID: "018f0000-0000-7000-8000-0000000000a5", Kind: "email", Subject: "Kickoff", At: "2026-06-01T09:00:00Z"},
		},
	}
}

// TestEveryPreparedQuestionAnswersFromItsOwnRecords is the promise the whole
// surface rests on: each question is answered from the slice of the account it
// names, and every sentence cites a record the input actually carried.
func TestEveryPreparedQuestionAnswersFromItsOwnRecords(t *testing.T) {
	in := askInput()
	known := knownRecords(askOrgID, in)

	for _, question := range declaredQuestions(t) {
		t.Run(string(question), func(t *testing.T) {
			answered := deterministicAnswer(question, askOrgID, in)
			if len(answered) == 0 {
				t.Fatal("no answer from an account that carries the records this question is about")
			}
			for _, sentence := range answered {
				if strings.TrimSpace(sentence.Text) == "" {
					t.Error("an empty sentence")
				}
				if len(sentence.Evidence) == 0 {
					t.Errorf("sentence %q carries no citation — the reader cannot check it", sentence.Text)
				}
				for _, cited := range sentence.Evidence {
					if !known[cited] {
						t.Errorf("sentence %q cites %+v, which this input never carried", sentence.Text, cited)
					}
				}
			}
		})
	}
}

// TestWhatsOpenAnswersThePipelineNotTheHistory pins what the question means: it
// names the open deals and the open tasks, and does not narrate the timeline.
func TestWhatsOpenAnswersThePipelineNotTheHistory(t *testing.T) {
	answered := deterministicAnswer(crmcontracts.WhatsOpen, askOrgID, askInput())
	text := strings.Join(texts(answered), " ")
	if !strings.Contains(text, "open deal") {
		t.Errorf("answer %q never mentions the open pipeline", text)
	}
	if !strings.Contains(text, "Call the CFO") {
		t.Errorf("answer %q never names the open task", text)
	}
	if strings.Contains(text, "Last contact") {
		t.Errorf("answer %q narrates the history instead of answering what is open", text)
	}
}

// TestWhatsChangedWalksTheTimelineNewestFirst proves the question reads the
// timeline in the order a rep catches up, and stops at a readable number of
// entries rather than replaying the account.
func TestWhatsChangedWalksTheTimelineNewestFirst(t *testing.T) {
	in := askInput()
	answered := deterministicAnswer(crmcontracts.WhatsChanged, askOrgID, in)
	if len(answered) != 3 {
		t.Fatalf("got %d sentences, want the three most recent entries", len(answered))
	}
	for i, sentence := range answered {
		want := Evidence{EntityType: citeActivity, EntityID: in.Recent[i].ID}
		if sentence.Evidence[0] != want {
			t.Errorf("sentence %d cites %+v, want the %dth-newest entry %+v", i, sentence.Evidence[0], i, want)
		}
	}
}

// TestAnEmptyAccountAnswersNothingRatherThanSomethingEmpty is the honest-absent
// case the contract advertises. `whats_open` over an account with no deals and
// no tasks has no answer, and saying nothing beats a sentence written around
// the gap.
func TestAnEmptyAccountAnswersNothingRatherThanSomethingEmpty(t *testing.T) {
	bare := Input{Name: "Quiet GmbH"}
	if answered := deterministicAnswer(crmcontracts.WhatsOpen, askOrgID, bare); len(answered) != 0 {
		t.Errorf("answer %+v for an account with nothing open", answered)
	}
	if answered := deterministicAnswer(crmcontracts.WhatsChanged, askOrgID, bare); len(answered) != 0 {
		t.Errorf("answer %+v for an account with an empty timeline", answered)
	}
	// meeting_prep is different by design: the account itself is always
	// something to prep from, and it cites the organization.
	prep := deterministicAnswer(crmcontracts.MeetingPrep, askOrgID, bare)
	if len(prep) != 1 || prep[0].Evidence[0].EntityID != askOrgID {
		t.Errorf("meeting_prep = %+v, want one sentence about the account itself", prep)
	}
}

// TestTwoReadersOfTheSameAccountCannotShareABrief is the per-viewer guarantee
// where it has a real failure mode.
//
// Two callers, one account, one instant: the difference is that the second may not
// read deals. Their inputs must fingerprint differently, because the fingerprint IS
// the cache key — if they collide, whichever reader arrives first writes a brief
// the other then reads, and the restricted one is served sentences about a pipeline
// they were refused.
//
// Asserting instead that a withheld section leaves no deals in the Input would
// prove nothing: a nil section yields nothing by Go's own semantics, and no change
// to how omission is handled could make that fail. What CAN break is
// Input.SectionsOmitted no longer riding the hash, and that is what this catches.
func TestTwoReadersOfTheSameAccountCannotShareABrief(t *testing.T) {
	account := crmcontracts.Organization360{
		Organization: crmcontracts.Organization{DisplayName: "Nordwind AG"},
		People: &struct {
			Data []crmcontracts.Organization360Contact `json:"data"`
			Page crmcontracts.PageInfo                 `json:"page"`
		}{Data: []crmcontracts.Organization360Contact{{
			PersonId: openapi_types.UUID(ids.NewV7()), FullName: "Dana Buyer",
		}}},
	}
	// The reader who may not see deals. Everything else about the account, and the
	// instant it was read at, is identical.
	restricted := account
	restricted.SectionsOmitted = []crmcontracts.Organization360SectionsOmitted{"deals"}

	full, err := Fingerprint(FromView(account), "routing-1")
	if err != nil {
		t.Fatalf("fingerprint the unrestricted input: %v", err)
	}
	narrow, err := Fingerprint(FromView(restricted), "routing-1")
	if err != nil {
		t.Fatalf("fingerprint the restricted input: %v", err)
	}
	if full == narrow {
		t.Error("two readers with different grants fingerprint the same, so they share " +
			"one cached brief — the restricted one would be served the other's pipeline")
	}

	// And the writer is told which subject to stay off, rather than being left to
	// infer around the gap.
	if got := FromView(restricted).SectionsOmitted; len(got) != 1 || got[0] != "deals" {
		t.Errorf("sections_omitted = %v, want the withheld section named for the prompt", got)
	}
}

// TestParseQuestionRefusesAnythingNotPrepared is the door: a question this
// package does not answer is a stated error, never a default. Silently
// answering a different question than the one asked is indistinguishable from
// answering the one asked badly.
func TestParseQuestionRefusesAnythingNotPrepared(t *testing.T) {
	for _, prepared := range declaredQuestions(t) {
		if got, err := ParseQuestion(prepared); err != nil || got != prepared {
			t.Errorf("ParseQuestion(%q) = (%q, %v), want it accepted", prepared, got, err)
		}
	}
	for _, refused := range []crmcontracts.OrganizationQuestion{"", "why_did_they_ghost_me", "WHATS_OPEN"} {
		if _, err := ParseQuestion(refused); err == nil {
			t.Errorf("ParseQuestion(%q) was accepted", refused)
		}
	}
}

// TestEveryPreparedQuestionCarriesItsOwnInstruction is the completeness gate
// between the contract and this package, and it reads the CONTRACT's own enum
// rather than a list beside it.
//
// A hand-typed list is not a gate: a fourth question declared upstream would
// compile, pass a list that never mentions it, and reach deterministicAnswer's
// default to answer nothing. Reading api/crm.yaml means the declaration is what
// fails the build.
func TestEveryPreparedQuestionCarriesItsOwnInstruction(t *testing.T) {
	declared := declaredQuestions(t)
	if len(declared) == 0 {
		t.Fatal("the contract declares no OrganizationQuestion — the gate would pass on nothing")
	}
	if len(askInstruction) != len(declared) {
		t.Errorf("askInstruction has %d entries for %d declared questions: %v",
			len(askInstruction), len(declared), declared)
	}
	for _, question := range declared {
		instruction, wired := askInstruction[question]
		if !wired || strings.TrimSpace(instruction) == "" {
			t.Errorf("question %q has no instruction, so its answer would not differ from the others", question)
			continue
		}
		if _, err := ParseQuestion(question); err != nil {
			t.Errorf("ParseQuestion(%q) refuses a question the contract declares: %v", question, err)
		}
		if len(deterministicAnswer(question, askOrgID, askInput())) == 0 {
			t.Errorf("question %q has an instruction but no deterministic answer", question)
		}
	}
}

// declaredQuestions reads OrganizationQuestion's enum out of the authoritative
// contract document, so this package cannot drift from it silently.
func declaredQuestions(t *testing.T) []crmcontracts.OrganizationQuestion {
	t.Helper()
	const contractPath = "../../../api/crm.yaml"
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("reading %s: %v", contractPath, err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", contractPath, err)
	}
	schema, declared := doc.Components.Schemas["OrganizationQuestion"]
	if !declared {
		t.Fatalf("%s declares no OrganizationQuestion schema", contractPath)
	}
	out := make([]crmcontracts.OrganizationQuestion, 0, len(schema.Enum))
	for _, value := range schema.Enum {
		out = append(out, crmcontracts.OrganizationQuestion(value))
	}
	return out
}

// TestTheAskPromptFencesTheAccountWithItsOwnNonce proves the boundary the
// prompt names is the boundary wrapping the data. The account summary carries
// activity subjects written outside this workspace; a prompt naming a different
// nonce than the wrap would fence nothing.
func TestTheAskPromptFencesTheAccountWithItsOwnNonce(t *testing.T) {
	req := AskRequest(crmcontracts.WhatsOpen, askInput())
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want the one fenced summary", len(req.Messages))
	}
	// The opening tag of the wrap carries this call's nonce; the system prompt
	// has to name that same tag, or the boundary it declares is not the one the
	// data sits behind.
	marker := openingTag.FindString(req.Messages[0].Content)
	if marker == "" {
		t.Fatalf("the wrapped summary carries no boundary marker: %q", req.Messages[0].Content)
	}
	if !strings.Contains(req.System, marker) {
		t.Errorf("the system prompt does not name the boundary %q that wraps the data", marker)
	}
	if !strings.Contains(req.System, askInstruction[crmcontracts.WhatsOpen]) {
		t.Error("the system prompt carries no per-question instruction, so every question would answer alike")
	}
}

func texts(sentences []Sentence) []string {
	out := make([]string, 0, len(sentences))
	for _, sentence := range sentences {
		out = append(out, sentence.Text)
	}
	return out
}
