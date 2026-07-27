// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// What the grader is shown as the input the candidate answered. The corpus
// fixture is not that input: a site's own code turns the fixture into a prompt,
// and several sites MINT the identifiers they ask the model to answer by, so the
// ids in a correct reply exist nowhere in the fixture. A grader shown the
// fixture reads every one of them as invented.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// mintedIDVariant names the stand-in site that mints the identifier it asks the
// model to answer by — the shape five shipped sites have (the verdict row id,
// classify's message ids, the ranker's candidate ids, offer_draft's product ids,
// enrich's activity id). The id is a fact of the REQUEST and of nothing else,
// which makes it the one thing that tells the two candidate inputs apart.
const mintedIDVariant = "widget_minted_id"

func mintedIDSite() aitasks.Site {
	return aitasks.Site{Task: ai.TaskSummarize, Variant: mintedIDVariant, Kind: ai.SiteKindOneShot}
}

// mintingCases is widgetCases that identifies its subject by an id it mints
// itself. It keeps every id it minted so a test can ask what THIS run's prompt
// carried; Prepare is called once per run and once more by the stamp, and the
// ids are appended in that order.
type mintingCases struct {
	mu     sync.Mutex
	minted []string
}

func (c *mintingCases) Site() aitasks.Site { return mintedIDSite() }

func (c *mintingCases) Prepare(fixture, _ json.RawMessage) (aitasks.PreparedCase, error) {
	var f struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, err
	}
	id := ids.NewV7().String()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minted = append(c.minted, id)
	return mintingCase{id: id, subject: f.Subject}, nil
}

// lastMinted is the id the most recent Prepare handed its case.
func (c *mintingCases) lastMinted(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.minted) == 0 {
		t.Fatal("no case was prepared, so no id was minted")
	}
	return c.minted[len(c.minted)-1]
}

type mintingCase struct{ id, subject string }

func (c mintingCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := model.Request{
		System:    "Describe the subject in one sentence, under the id you are given.",
		Messages:  []model.Message{{Role: roleUser, Content: "id: " + c.id + "\nsubject: " + c.subject}},
		MaxTokens: 1024,
	}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, err
	}
	trace.Output = resp.Text
	return trace, nil
}

func (mintingCase) Evaluate(aitasks.Trace) aitasks.Outcome {
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// The load-bearing case: the grader must be able to check an answer's ids
// against the ids the candidate was handed. A grader shown the fixture instead
// sees an answer identified by something the input it holds never mentions,
// which reads as invention on every correct reply.
func TestTheJudgeIsShownTheTurnTheCandidateWasGiven(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script("the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90))

	factory := &mintingCases{}
	census := aitasks.NewRegistry()
	census.Register(mintedIDSite())
	census.BindCase(mintedIDSite(), factory)

	sc := testScenarioOnSite("basic", mintedIDVariant, wideBands)
	if _, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, census,
		ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
		}); err != nil {
		t.Fatalf("certifyTask: %v", err)
	}

	judgeCalls := judgeFake.Calls()
	if len(judgeCalls) != 1 {
		t.Fatalf("the judge was called %d times, want the one call this single run scores", len(judgeCalls))
	}
	minted := factory.lastMinted(t)
	if !strings.Contains(string(judgeCalls[0].Payload), minted) {
		t.Fatalf("the grader was never shown the id %s the candidate was asked to answer under:\n%s",
			minted, judgeCalls[0].Payload)
	}
}

func TestCandidateAskIsTheFirstRequestsUserTurns(t *testing.T) {
	t.Run("the first request, not the last", func(t *testing.T) {
		trace := aitasks.Trace{Requests: []model.Request{
			{Messages: []model.Message{{Role: roleUser, Content: "what the model was asked"}}},
			{Messages: []model.Message{{Role: roleUser, Content: "a follow-up built around a reply"}}},
		}}
		ask, err := candidateAsk(trace)
		if err != nil {
			t.Fatalf("candidateAsk: %v", err)
		}
		if ask != "what the model was asked" {
			t.Fatalf("candidateAsk = %q, want the first request's ask", ask)
		}
	})

	t.Run("every user turn of it, and no assistant turn", func(t *testing.T) {
		trace := aitasks.Trace{Requests: []model.Request{{Messages: []model.Message{
			{Role: roleUser, Content: "context block"},
			{Role: "assistant", Content: "a turn the model itself wrote"},
			{Role: roleUser, Content: "the question"},
		}}}}
		ask, err := candidateAsk(trace)
		if err != nil {
			t.Fatalf("candidateAsk: %v", err)
		}
		if ask != "context block\n\nthe question" {
			t.Fatalf("candidateAsk = %q, want both user turns and neither the assistant's", ask)
		}
	})
}

// A trace with no ask in it is a harness fault, and the grader would otherwise
// be handed an empty input to score an answer against.
func TestCandidateAskRefusesATraceThatAskedNothing(t *testing.T) {
	t.Run("no request at all", func(t *testing.T) {
		if _, err := candidateAsk(aitasks.Trace{Output: "answered without asking"}); err == nil {
			t.Fatal("want an error for a trace carrying no request")
		}
	})

	t.Run("a first request with no user turn", func(t *testing.T) {
		trace := aitasks.Trace{Requests: []model.Request{{System: "instructions only"}}}
		if _, err := candidateAsk(trace); err == nil {
			t.Fatal("want an error for a first request that asks nothing")
		}
	})
}
