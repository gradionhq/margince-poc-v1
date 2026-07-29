// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The summarize/org_brief certification case.
//
// A case that measures nothing is worse than no case: it stays green through
// every regression and costs a paid run to discover. So Prepare refuses the
// fixtures and expectations no reply could ever disagree with, and these
// tests are how that refusal is kept honest.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// recordingLane answers one canned reply and remembers nothing else — the
// case's own Run is what is under test, not a conversation.
type recordingLane struct{ reply string }

func (l *recordingLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: l.reply}, nil
}

const orgBriefFixtureJSON = `{
  "name": "Brandt Automotive GmbH",
  "industry": "Automotive",
  "strength": 41,
  "contact_count": 2,
  "open_deals": [
    {"label":"stalled_retrofit","name":"Fleet retrofit","stage":"Proposal","amount_minor":4800000,"currency":"EUR","stalled":true}
  ],
  "recent": [
    {"label":"last_email","kind":"email","subject":"Re: proposal","at":"2026-07-10T09:00:00Z"}
  ]
}`

// prepareOrgBrief prepares the one fixture these tests vary the EXPECTATION
// against; the refusal tests below pass their own fixtures directly.
func prepareOrgBrief(t *testing.T, expected string) aitasks.PreparedCase {
	t.Helper()
	prepared, err := orgBriefCases{}.Prepare(
		json.RawMessage(orgBriefFixtureJSON), json.RawMessage(expected))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return prepared
}

func TestOrgBriefCaseSiteMatchesTheCensusedSite(t *testing.T) {
	site := orgBriefCases{}.Site()
	if site.Variant != "org_brief" || string(site.Task) != "summarize" {
		t.Errorf("Site() = %+v, want the summarize/org_brief one-shot", site)
	}
}

// The ids the model sees are MINTED here, never supplied by the corpus:
// an id the fixture author could write into the expected answer would make a
// model echoing it indistinguishable from one that read the account.
func TestOrgBriefCaseMintsTheIdsTheModelSees(t *testing.T) {
	first := prepareOrgBrief(t, `["stalled_retrofit"]`)
	second := prepareOrgBrief(t, `["stalled_retrofit"]`)

	firstIDs := requestIDs(t, first)
	secondIDs := requestIDs(t, second)
	if firstIDs == secondIDs {
		t.Error("two preparations of one fixture sent the same ids — they are not being minted")
	}
	if strings.Contains(firstIDs, "stalled_retrofit") {
		t.Error("a corpus label reached the prompt; the model must only ever see minted ids")
	}
}

func requestIDs(t *testing.T, prepared aitasks.PreparedCase) string {
	t.Helper()
	lane := &recordingLane{reply: `{"sentences":[]}`}
	trace, err := prepared.Run(t.Context(), lane)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the case issued %d requests, want the one this site sends", len(trace.Requests))
	}
	return trace.Requests[0].Messages[0].Content
}

func TestOrgBriefCaseRefusesAnExpectationNoReplyCouldFail(t *testing.T) {
	for name, expected := range map[string]string{
		"nothing expected":                    `[]`,
		"a label the fixture does not supply": `["a_deal_that_is_not_there"]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (orgBriefCases{}).Prepare(
				json.RawMessage(orgBriefFixtureJSON), json.RawMessage(expected)); err == nil {
				t.Error("Prepare accepted an expectation no reply could disagree with")
			}
		})
	}
}

func TestOrgBriefCaseRefusesAFixtureNoExpectationCouldName(t *testing.T) {
	unlabelled := `{"name":"Acme","open_deals":[{"label":"","name":"X"}]}`
	if _, err := (orgBriefCases{}).Prepare(
		json.RawMessage(unlabelled), json.RawMessage(`["x"]`)); err == nil {
		t.Error("Prepare accepted a fixture whose record carries no label")
	}
}

// The four outcomes are distinct because they mean different things: a reply
// that cited the wrong records is a measurement, one that cited nothing of
// this account is an abstention, and unparseable output is neither.
func TestOrgBriefCaseEvaluatesEachOutcome(t *testing.T) {
	prepared := prepareOrgBrief(t, `["stalled_retrofit"]`)
	sent := requestIDs(t, prepared)
	dealID := firstRecordID(t, sent)

	cases := map[string]struct {
		output string
		want   string
	}{
		"cited what it had to": {
			output: `{"sentences":[{"text":"The retrofit has stalled.","evidence":[{"entity_type":"deal","entity_id":"` + dealID + `"}]}]}`,
			want:   aitasks.OutcomeAccepted,
		},
		"cited nothing of this account": {
			output: `{"sentences":[{"text":"Looks promising.","evidence":[{"entity_type":"deal","entity_id":"11111111-1111-4111-8111-111111111111"}]}]}`,
			want:   aitasks.OutcomeAbstained,
		},
		"not json at all": {
			output: `I'm afraid I can't do that.`,
			want:   aitasks.OutcomeInvalid,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := prepared.Evaluate(aitasks.Trace{Output: tc.output})
			if got.Result != tc.want {
				t.Errorf("Evaluate = %q (%s), want %q", got.Result, got.Detail, tc.want)
			}
		})
	}
}

// A brief that cited SOME record of the account but not the one the scenario
// says matters is a wrong answer, not an abstention: the model wrote about
// this account and picked the wrong thing to say.
func TestOrgBriefCaseReportsAMissedRecordAsAWrongAnswer(t *testing.T) {
	prepared := prepareOrgBrief(t, `["stalled_retrofit","last_email"]`)
	sent := requestIDs(t, prepared)
	dealID := firstRecordID(t, sent)

	got := prepared.Evaluate(aitasks.Trace{
		Output: `{"sentences":[{"text":"One open deal.","evidence":[{"entity_type":"deal","entity_id":"` + dealID + `"}]}]}`,
	})
	if got.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Evaluate = %q (%s), want a wrong answer", got.Result, got.Detail)
	}
	if !strings.Contains(got.Detail, "last_email") {
		t.Errorf("the detail does not name what was missed: %q", got.Detail)
	}
}

// firstRecordID pulls the first minted RECORD id out of what the prompt
// carried, so a test can answer with an id the model was actually handed.
//
// It anchors on the id field rather than scanning for anything uuid-shaped:
// the prompt also carries the fence nonce, which is uuid-shaped and is not a
// record — answering with it would test nothing.
func firstRecordID(t *testing.T, prompt string) string {
	t.Helper()
	const field = `"id":"`
	at := strings.Index(prompt, field)
	if at < 0 {
		t.Fatalf("no record id in the prompt: %q", prompt)
	}
	rest := prompt[at+len(field):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatalf("an unterminated record id in the prompt: %q", prompt)
	}
	return rest[:end]
}
