// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for voice_build/eval_scores — the bounded judge call a
// voice build asks about its own held-out drafts, and half of every score it
// activates or refuses a candidate on.
//
// It certifies the shipped path rather than a description of it: Run drives
// judgeVoiceDrafts, the function the evaluation loop calls, and Evaluate reads
// the answer with readVoiceJudgeScores, the reading that decides whether the
// evaluation has a verdict at all. A case that rebuilt either would measure a
// copy, and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here is an ORDER, not a score: which of the judged
// drafts must be scored above which. A score is one model's opinion on a
// continuous scale and no two models put the same number on the same prose —
// pinning one would fail a judge for agreeing in different units. What the site
// is FOR survives that: the evaluation's whole use of these numbers is
// comparative, so a judge that ranks the author's own rhythm above generic AI
// prose is doing its job whatever numbers it wrote, and one that does not is
// wrong however confident it sounds.
//
// The ranking is a chain and a SUBSET claim: a scenario names the drafts whose
// order it is willing to defend and says nothing about the rest, because three
// repeats of one prompt are routinely near-identical and demanding a total order
// over them would fail a judge for being unable to invent a difference.
//
// Ties break the claim. A tie is the judge saying it cannot tell two drafts
// apart, and telling drafts apart is the entire job.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// voiceEvalScoresSite names this site in every refusal it writes, so a corpus
// author reading one knows which scenario to open.
const voiceEvalScoresSite = "voice_build/eval_scores"

// voiceEvalScoresFixture is ONE judging call in exactly what the evaluation
// hands it: the held-out sample the drafts are compared against, and one
// prompt's whole repeat set in the order they were drafted.
//
// The drafts arrive already sanitized, because sanitized is what the evaluation
// judges — the drafting half rewrites the hard punctuation rule before anything
// is scored, and a fixture carrying raw drafts would certify a comparison the
// product never makes.
type voiceEvalScoresFixture struct {
	AuthorSample string   `json:"author_sample"`
	Drafts       []string `json:"drafts"`
}

// voiceEvalScoresCases serves the one site that scores held-out drafts against
// their author.
type voiceEvalScoresCases struct{}

func (voiceEvalScoresCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskVoiceBuild,
		Variant: "eval_scores",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one judging call and the order the scenario expects its drafts
// in into a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (voiceEvalScoresCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f voiceEvalScoresFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", voiceEvalScoresSite, err)
	}
	if err := refuseUnjudgeablePrompt(f); err != nil {
		return nil, err
	}
	// A correct answer differs from an incorrect one in the order alone, so the
	// expectation IS that order rather than a wrapper carrying it.
	var order []int
	if err := json.Unmarshal(expected, &order); err != nil {
		return nil, fmt.Errorf(
			"%s: the expected answer is not a ranking of draft numbers, best first: %w", voiceEvalScoresSite, err)
	}
	if err := refuseUnrankableOrder(order, f.Drafts); err != nil {
		return nil, err
	}
	return &voiceEvalScoresCase{sample: f.AuthorSample, drafts: f.Drafts, order: order}, nil
}

// refuseUnjudgeablePrompt names a fixture the evaluation could never have been
// handed. The judge exists to compare drafts against an author, so a call with
// no author sample compares them against nothing; and the evaluation judges one
// prompt's repeats together in a single call, which is what lets the answer be
// one score per repeat.
func refuseUnjudgeablePrompt(f voiceEvalScoresFixture) error {
	if strings.TrimSpace(f.AuthorSample) == "" {
		return fmt.Errorf(
			"%s: the fixture supplies no author sample, and the drafts are scored against one",
			voiceEvalScoresSite)
	}
	if len(f.Drafts) != voiceEvalRepeatsPerPrompt {
		return fmt.Errorf(
			"%s: the fixture supplies %d drafts, and the evaluation judges all %d repeats of one prompt in one call",
			voiceEvalScoresSite, len(f.Drafts), voiceEvalRepeatsPerPrompt)
	}
	return nil
}

// refuseUnrankableOrder names an expectation the judge can never satisfy: a
// chain too short to compare anything, a draft named twice, a draft this call
// never carried, and a draft the drafting half could not produce — an empty body
// is judged for shape and then discarded by the caller, so its score decides
// nothing and an order over it measures nothing.
//
// In the expectation's own numbering, which is the one the judge is asked in:
// drafts are 1..n, in the order the fixture lists them.
func refuseUnrankableOrder(order []int, drafts []string) error {
	if len(order) < 2 {
		return fmt.Errorf(
			"%s: the scenario ranks %d drafts, which compares nothing — a ranking needs two",
			voiceEvalScoresSite, len(order))
	}
	named := make(map[int]bool, len(order))
	for _, number := range order {
		switch {
		case number < 1 || number > len(drafts):
			return fmt.Errorf("%s: the scenario names draft %d, and this call judges drafts 1 to %d",
				voiceEvalScoresSite, number, len(drafts))
		case named[number]:
			return fmt.Errorf("%s: the scenario names draft %d twice, so it ranks that draft against itself",
				voiceEvalScoresSite, number)
		case strings.TrimSpace(drafts[number-1]) == "":
			return fmt.Errorf(
				"%s: the scenario ranks draft %d, and draft %d is empty — an unusable draft's score is discarded "+
					"before it is compared to anything",
				voiceEvalScoresSite, number, number)
		}
		named[number] = true
	}
	return nil
}

// voiceEvalScoresCase is one judging call ready to be answered, closed over the
// order the scenario expects the answer to put its drafts in.
type voiceEvalScoresCase struct {
	sample string
	drafts []string
	order  []int
}

// Run drives judgeVoiceDrafts and records the request it issued. The evaluation
// reads the answer through the same function, so what the trace carries is the
// text that reading was given.
//
// The judged scores are deliberately not carried out of Run: Evaluate re-reads
// the answer with the production reading, so the record's verdict is measured
// rather than inherited from a call that returns a neutral fallback on refusal.
func (c *voiceEvalScoresCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &voiceJudgeRecorder{completer: completer}
	if _, _, err := judgeVoiceDrafts(ctx, recorder, c.sample, c.drafts); err != nil {
		return aitasks.Trace{Requests: recorder.requests}, fmt.Errorf("%s: %w", voiceEvalScoresSite, err)
	}
	return aitasks.Trace{Requests: recorder.requests, Output: recorder.answer}, nil
}

// voiceJudgeRecorder is the brain the evaluation judges through: it records the
// request the judging call issued and the answer it read. It deliberately does
// NOT implement validatedBrain, so the call is sent bare — production wraps the
// same request in the shape-retry when the brain supports one, and a case that
// retried would certify the answer a model gives after being told to try again
// rather than the answer it gives.
type voiceJudgeRecorder struct {
	completer aitasks.Completer
	requests  []model.Request
	answer    string
}

func (r *voiceJudgeRecorder) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	r.requests = append(r.requests, req)
	resp, err := r.completer.Complete(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	r.answer = resp.Text
	return resp, nil
}

// Evaluate applies the evaluation's own reading first and only then asks about
// the order. The order is the meaning: an answer the evaluation refuses carries
// no verdict to disagree with, and the neutral scores that stand in for one are
// not the judge's opinion about anything.
func (c *voiceEvalScoresCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	scores, refused := readVoiceJudgeScores(trace.Output, len(c.drafts))
	if refused != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: refused.Error()}
	}
	measured := c.measured(scores)
	if broken := c.brokenComparisons(scores); len(broken) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(broken, measured), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: measured}
}

// brokenComparisons names every link of the expected chain the answer does not
// hold, all of them: an answer that got one comparison right and two wrong is
// not the near miss one line would read as. A tie counts as broken — the site
// exists to tell drafts apart.
func (c *voiceEvalScoresCase) brokenComparisons(scores []float64) []string {
	var out []string
	for i := 1; i < len(c.order); i++ {
		above, below := c.order[i-1], c.order[i]
		if scores[above-1] > scores[below-1] {
			continue
		}
		out = append(out, fmt.Sprintf(
			"draft %d scores %.4f and draft %d scores %.4f, where the scenario expects draft %d above draft %d",
			above, scores[above-1], below, scores[below-1], above, below))
	}
	return out
}

// measured renders what the judge actually said, whatever the verdict: the
// numbers are the diagnosis a corpus author needs to tell a judge that disagrees
// from one that cannot tell the drafts apart at all.
func (c *voiceEvalScoresCase) measured(scores []float64) string {
	rendered := make([]string, 0, len(scores))
	for i, score := range scores {
		rendered = append(rendered, fmt.Sprintf("draft %d at %.4f", i+1, score))
	}
	return "the judge scored " + strings.Join(rendered, ", ")
}
