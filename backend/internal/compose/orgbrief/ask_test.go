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
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
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
		OpenTasks: []TaskIn{
			{ID: "018f0000-0000-7000-8000-0000000000a2", Name: "Call the CFO", Due: "2026-07-21T09:00:00Z"},
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
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, askInput())
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

// TestWhatsChangedTakesTheLeadingEntriesInOrder proves the question follows the
// timeline it was handed and stops at a readable number, rather than replaying the
// account.
//
// It asserts input ORDER, which is what changedAnswer decides. That the input is
// newest-first is the 360's property, carried through foldRecent unchanged — this
// cannot see it, so it does not claim to.
func TestWhatsChangedTakesTheLeadingEntriesInOrder(t *testing.T) {
	in := askInput()
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsChanged, askOrgID, in)
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
	if answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, bare); len(answered) != 0 {
		t.Errorf("answer %+v for an account with nothing open", answered)
	}
	// A single entry reaches both the loop body and the mostRecent bound, so this
	// gates changedAnswer rather than the emptiness of a nil slice.
	one := Input{Name: "Quiet GmbH", Recent: []ActIn{
		{ID: "018f0000-0000-7000-8000-0000000000b1", Kind: "call", At: "2026-07-01T09:00:00Z"},
	}}
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsChanged, askOrgID, one)
	if len(answered) != 1 || answered[0].Evidence[0].EntityID != one.Recent[0].ID {
		t.Errorf("answer %+v for a one-entry timeline, want the one entry cited", answered)
	}
	// meeting_prep is different by design: the account itself is always
	// something to prep from, and it cites the organization.
	prep := deterministicAnswer(crmcontracts.OrganizationQuestionMeetingPrep, askOrgID, bare)
	if len(prep) != 1 || prep[0].Evidence[0].EntityID != askOrgID {
		t.Errorf("meeting_prep = %+v, want one sentence about the account itself", prep)
	}
}

// TestTheWriterIsToldWhichSubjectsToStayOffOf is the per-viewer guarantee at the
// only point it can fail.
//
// A withheld section reaches the writer as an instruction, not as an absence. Both
// prompts carry "if the summary names sections_omitted, say nothing about those
// subjects at all", and that line does nothing unless FromView actually carries the
// names into the payload — a model handed an account with no deals and no note of
// why is free to remark on the empty pipeline it infers.
//
// The instruction is what this gates, because it is the part that can break. The
// records of a withheld section are absent from the input whatever this package
// does — the 360 hands it a nil section — and no reader can be served another's
// brief whatever it says, because org_brief is keyed per user.
func TestTheWriterIsToldWhichSubjectsToStayOffOf(t *testing.T) {
	restricted := crmcontracts.Organization360{
		Organization:    crmcontracts.Organization{DisplayName: "Nordwind AG"},
		SectionsOmitted: []crmcontracts.Organization360SectionsOmitted{"deals"},
		People: &struct {
			Data []crmcontracts.Organization360Contact `json:"data"`
			Page crmcontracts.PageInfo                 `json:"page"`
		}{Data: []crmcontracts.Organization360Contact{{
			PersonId: openapi_types.UUID(ids.NewV7()), FullName: "Dana Buyer",
		}}},
	}

	// End to end into the bytes the model receives, not just into the struct: the
	// instruction is worthless if the payload never names the section.
	payload := AskRequest(crmcontracts.OrganizationQuestionMeetingPrep, FromView(restricted)).Messages[0].Content
	if !strings.Contains(payload, `"sections_omitted":["deals"]`) {
		t.Errorf("the prompt payload does not name the withheld section, so the "+
			"instruction to stay silent about it applies to nothing: %s", payload)
	}
	// The section that DID come back is there too, so the assertion above is about
	// omission rather than about an empty payload.
	if !strings.Contains(payload, "Dana Buyer") {
		t.Errorf("the payload lost the section this reader can see: %s", payload)
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
	req := AskRequest(crmcontracts.OrganizationQuestionWhatsOpen, askInput())
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
	if !strings.Contains(req.System, askInstruction[crmcontracts.OrganizationQuestionWhatsOpen]) {
		t.Error("the system prompt carries no per-question instruction, so every question would answer alike")
	}
}

// threeCheckIns is the account that produced the answer this shape was built
// to stop: three tasks whose subjects are identical, which the old writer
// joined into one sentence carrying three citations. The reader got three
// unseparated chips and a wall of ids.
func threeCheckIns() Input {
	return Input{
		Name: "Brandt Automotive GmbH",
		OpenTasks: []TaskIn{
			{ID: "019fac6c-60d1-732b-ab2b-d93611745625", Name: "Check in", Due: "2026-07-21T09:00:00Z"},
			{ID: "019fac6c-60c8-745f-8c5f-1503a9a4ea48", Name: "Check in", Due: "2026-07-17T09:00:00Z"},
			{ID: "019fac6c-60f8-77d4-a267-dabced904009", Name: "Check in", Due: "2026-07-28T09:00:00Z"},
		},
	}
}

// TestOpenTasksAnswerIsOneSentencePerTask is the shape rule at the case that
// broke: a count sentence the reader can read, then one sentence per task, and
// every sentence carrying exactly the one citation it is about.
func TestOpenTasksAnswerIsOneSentencePerTask(t *testing.T) {
	in := threeCheckIns()
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, in)
	if len(answered) != 1+len(in.OpenTasks) {
		t.Fatalf("got %d sentences for %d tasks, want one count sentence and one per task: %q",
			len(answered), len(in.OpenTasks), texts(answered))
	}
	for _, sentence := range answered {
		if len(sentence.Evidence) != 1 {
			t.Errorf("sentence %q carries %d citations, want the one record it is about",
				sentence.Text, len(sentence.Evidence))
		}
		if claims.SpellsRecordID(sentence.Text) {
			t.Errorf("sentence %q spells a record id at the reader", sentence.Text)
		}
	}
	// The count sentence is anchored on the task due first, so the reader opens
	// the one that is most overdue from the sentence that counts them.
	if !strings.Contains(answered[0].Text, "3 open tasks") {
		t.Errorf("the count sentence %q does not state how many are open", answered[0].Text)
	}
	if !strings.Contains(answered[0].Text, "17 Jul 2026") {
		t.Errorf("the count sentence %q does not name the earliest due date", answered[0].Text)
	}
	if answered[0].Evidence[0].EntityID != in.OpenTasks[1].ID {
		t.Errorf("the count sentence cites %q, want the task due first", answered[0].Evidence[0].EntityID)
	}
}

// The per-task list stops at listedRecords, and the count sentence still states
// the TRUE total — a reader told "five open tasks" who has nine is worse off
// than one told nothing.
func TestOpenTasksAnswerCapsTheListButNotTheCount(t *testing.T) {
	in := Input{Name: "Brandt Automotive GmbH"}
	const tasks = listedRecords + 4
	for i := range tasks {
		in.OpenTasks = append(in.OpenTasks, TaskIn{
			ID:   fmt.Sprintf("019fac6c-60d1-732b-ab2b-d3611745%04d", i),
			Name: fmt.Sprintf("Task %d", i),
		})
	}
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, in)
	if len(answered) != 1+listedRecords {
		t.Fatalf("got %d sentences, want the count plus %d listed tasks", len(answered), listedRecords)
	}
	if !strings.Contains(answered[0].Text, fmt.Sprintf("%d open tasks", tasks)) {
		t.Errorf("the count sentence %q reports the capped list, not the true total", answered[0].Text)
	}
}

// One task needs no count in front of it: its own sentence names it and says
// when it is due, which is everything the count would have added.
func TestASingleOpenTaskIsOneSentence(t *testing.T) {
	answered := deterministicAnswer(crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, Input{
		Name:      "Brandt Automotive GmbH",
		OpenTasks: []TaskIn{{ID: "019fac6c-60d1-732b-ab2b-d93611745625", Name: "Call the CFO"}},
	})
	if len(answered) != 1 {
		t.Fatalf("got %d sentences for one task: %q", len(answered), texts(answered))
	}
	if !strings.Contains(answered[0].Text, "Call the CFO") {
		t.Errorf("the one task is not named: %q", answered[0].Text)
	}
}

// idSpellingLane answers exactly the way the shipped answer failed: grounded
// citations, and the same ids spelled out in the prose.
type idSpellingLane struct{ tasks []TaskIn }

func (l idSpellingLane) Complete(context.Context, model.Request) (model.Response, error) {
	written := make([]string, 0, len(l.tasks))
	for _, task := range l.tasks {
		written = append(written, fmt.Sprintf(
			`{"text":"You have an open task %q (ID: %s).","evidence":[{"entity_type":"activity","entity_id":%q}]}`,
			task.Name, task.ID, task.ID))
	}
	return model.Response{Text: `{"sentences":[` + strings.Join(written, ",") + `]}`}, nil
}

// A model that spells ids at the reader loses every sentence it wrote, and the
// reader gets the deterministic floor rather than developer output. This is the
// defect the user reported, at the seam that decides what they see.
func TestAnAnswerSpellingIDsFallsBackToTheFloor(t *testing.T) {
	in := threeCheckIns()
	lane := idSpellingLane{tasks: in.OpenTasks}

	answered, by, err := Answer(context.Background(), lane, crmcontracts.OrganizationQuestionWhatsOpen, askOrgID, in)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if by != crmcontracts.Deterministic {
		t.Errorf("generated_by = %q, want the floor — every model sentence spelled an id", by)
	}
	if len(answered) == 0 {
		t.Fatal("no sentences: dropping the model's prose must not drop the answer")
	}
	for _, sentence := range answered {
		if claims.SpellsRecordID(sentence.Text) {
			t.Errorf("the floor spells an id too: %q", sentence.Text)
		}
	}
}

// Every deterministic answer is readable prose: no answer may hand the reader a
// record id in the text, whichever question produced it.
func TestNoDeterministicAnswerSpellsAnIDAtTheReader(t *testing.T) {
	for _, question := range declaredQuestions(t) {
		for _, sentence := range deterministicAnswer(question, askOrgID, askInput()) {
			if claims.SpellsRecordID(sentence.Text) {
				t.Errorf("%s answered with an id in the text: %q", question, sentence.Text)
			}
		}
	}
}

func texts(sentences []Sentence) []string {
	out := make([]string, 0, len(sentences))
	for _, sentence := range sentences {
		out = append(out, sentence.Text)
	}
	return out
}
