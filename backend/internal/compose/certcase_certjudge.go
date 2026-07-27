// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for cert_judge/judge — the grader every other site's
// score comes from, which is why it is graded itself: an unmeasured grader makes
// every record it signs a claim about a model nobody checked.
//
// It certifies the shipped path rather than a description of it: Run issues
// JudgeRequest and Evaluate reads the answer with ParseJudgeVerdict, the two the
// harness's own judge call runs (judgeScore in compose/aicert calls the same two
// symbols, not copies of them). Those two moved out of the harness for this
// case: aicert imports compose, so a builder living there could never be reached
// by a case bound here.
//
// What the expectation MEANS here is a score BAND, not a score: the range a
// competent grader must place this candidate output in. An exact number is one
// grader's units — no two models put the same integer on the same answer — but
// the band is the only thing the harness ever asks of a score: Verdict compares
// the median against certified_min and the worst run against floor, and never
// reads the number for anything else. So the band certifies exactly the property
// the product acts on, and a scenario supplying a plainly good or plainly bad
// output can defend it.
//
// The verdict's reason is deliberately ungraded. judgeScore keeps the score and
// discards the reason, so a case that failed a run over its wording would refuse
// answers production uses without complaint.
//
// Run issues ONE call, where judgeScore retries once. The retry asks the same
// question again — same rubric, same input, same output, under this call's own
// freshly minted boundary — as the harness's tolerance for a grader that fumbled
// its JSON, and its double-failure fallback is a score of 0 that no model
// actually said. Certifying that would report a second attempt at the same
// question as if it were the grader's answer, and hide an unusable reply inside
// a number the bands would read as a terrible one. The seam already has the
// honest word for it: OutcomeInvalid.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// certJudgeSite names this site in every refusal it writes, so a corpus author
// reading one knows which scenario to open.
const certJudgeSite = "cert_judge/judge"

// certJudgeFixture is one grading call in exactly what the harness hands it: the
// rubric to score against, the input the candidate was answering, and the raw
// output to score.
//
// The rubric is this codebase's own text and the input is the scenario's; only
// the candidate output is a model's. All three reach the same user turn today,
// which is why the fixture holds all three — the fixture is what production is
// given, not what is safe to give it.
type certJudgeFixture struct {
	Rubric          string `json:"rubric"`
	ScenarioInput   string `json:"scenario_input"`
	CandidateOutput string `json:"candidate_output"`
}

// certJudgeBand is the score range the scenario says a competent grader must
// place its candidate output in, inclusive at both ends.
type certJudgeBand struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// certJudgeCases serves the one site that scores every other site's answers.
type certJudgeCases struct{}

func (certJudgeCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskCertJudge,
		Variant: "judge",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one grading call and the band the scenario expects its score in
// into a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (certJudgeCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f certJudgeFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", certJudgeSite, err)
	}
	if err := refuseUngradeableCall(f); err != nil {
		return nil, err
	}
	var band certJudgeBand
	if err := json.Unmarshal(expected, &band); err != nil {
		return nil, fmt.Errorf(
			"%s: the expected answer is not a score band {min, max}: %w", certJudgeSite, err)
	}
	if err := refuseUnreachableBand(band); err != nil {
		return nil, err
	}
	return &certJudgeCase{fixture: f, band: band}, nil
}

// refuseUngradeableCall names a fixture that asks the grader for a judgment it
// was given nothing to make. The instruction is "score the candidate's output
// against the rubric below", so a call carrying no rubric asks for a score
// against nothing, and one carrying no input asks whether an answer fits a
// question it was never shown — the two things every rubric in the corpus is
// written in terms of.
//
// An empty candidate output is NOT refused. A model really does return one — a
// reasoning model that spends its whole budget thinking stops with zero visible
// text — and "the grader must not pass nothing" is a claim worth a scenario.
func refuseUngradeableCall(f certJudgeFixture) error {
	if strings.TrimSpace(f.Rubric) == "" {
		return fmt.Errorf(
			"%s: the fixture supplies no rubric, and the grader is instructed to score against one", certJudgeSite)
	}
	if strings.TrimSpace(f.ScenarioInput) == "" {
		return fmt.Errorf(
			"%s: the fixture supplies no scenario input, so the grader is asked whether an answer suits a question it cannot see",
			certJudgeSite)
	}
	return nil
}

// refuseUnreachableBand names a band no run could ever measure the grader
// against. Each would cost a paid run to discover and report nothing when it
// arrived.
func refuseUnreachableBand(band certJudgeBand) error {
	switch {
	// A band ending at 0 is the shape an omitted expectation takes, and the one
	// a forgotten field decodes to — the same reason aicert refuses a zero
	// certified_min rather than auto-certifying every run.
	case band.Max == 0:
		return fmt.Errorf(
			"%s: the scenario's band ends at 0, which is what an omitted expectation decodes to — name the range a competent grader must score this output in",
			certJudgeSite)
	case band.Min < 0 || band.Max > 100:
		return fmt.Errorf(
			"%s: the scenario expects %d-%d, and a verdict outside 0-100 is refused before it is ever compared to a band",
			certJudgeSite, band.Min, band.Max)
	case band.Min > band.Max:
		return fmt.Errorf(
			"%s: the scenario expects %d-%d, which no score is inside", certJudgeSite, band.Min, band.Max)
	case band.Min == band.Max:
		return fmt.Errorf(
			"%s: the scenario expects exactly %d, and no two graders put the same number on the same answer — express the judgment as a range",
			certJudgeSite, band.Min)
	case band.Min == 0 && band.Max == 100:
		return fmt.Errorf(
			"%s: the scenario expects 0-100, which every readable verdict is inside — no grader could disagree with it",
			certJudgeSite)
	}
	return nil
}

// certJudgeCase is one grading call ready to be answered, closed over the band
// the scenario expects its score in.
type certJudgeCase struct {
	fixture certJudgeFixture
	band    certJudgeBand
}

// Run issues the one request this site sends.
func (c *certJudgeCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := JudgeRequest(c.fixture.Rubric, c.fixture.ScenarioInput, c.fixture.CandidateOutput)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("%s: %w", certJudgeSite, err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the harness's own read first and only then asks about the
// band. The order is the meaning: a verdict the harness cannot read carries no
// score to disagree with, and the 0 that stands in for one is not the grader's
// opinion of anything.
func (c *certJudgeCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	verdict, err := ParseJudgeVerdict(trace.Output)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	measured := fmt.Sprintf("the grader scored %d (%q)", verdict.Score, verdict.Reason)
	if verdict.Score < c.band.Min || verdict.Score > c.band.Max {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("%s, where the scenario expects %d-%d", measured, c.band.Min, c.band.Max),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: measured}
}
