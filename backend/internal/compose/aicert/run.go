// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// ONE run: the case driven, the answer scored, and everything the run cost
// folded across every call it made. A run is not one model call — how many a
// site sends is the site's own decision, a retry, a fallback or a whole tool
// loop — so this file's job is to keep "the run" and "the last call" from ever
// being the same thing.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// runScenario drives repeats runs of one scenario, folding each into acc, and
// returns the scenario's own verdict for certifyTask to fold into the task's
// worst-case verdict. The per-run degrade gates sit here rather than inside
// certifyTask because they void the WHOLE task: a demoted answer or a demoted
// grader anywhere in the set means no record, not a lower band.
func runScenario(ctx context.Context, task ai.Task, sc Scenario, census *aitasks.Registry, repeats int,
	candidateRouter *ai.Router, candidateRec *traceRecorder, judgeRouter *ai.Router, judgeRec *traceRecorder,
	log *slog.Logger, acc *taskAccumulation, trace *payloadTrace,
) (string, error) {
	scenarioResults := make([]RunResult, 0, repeats)
	for i := 0; i < repeats; i++ {
		outcome, runErr := runOnce(ctx, candidateRouter, candidateRec, judgeRouter, judgeRec, sc, task, census, log, trace, i+1)
		if runErr != nil {
			return "", fmt.Errorf("aicert: task %s scenario %s run %d: %w", task, sc.Name, i+1, runErr)
		}
		if outcome.Degraded {
			return "", fmt.Errorf(
				"aicert: task %s scenario %s run %d: candidate attempt served on a budget-degraded route — refusing to certify a demoted answer",
				task, sc.Name, i+1)
		}
		if outcome.JudgeDegraded {
			return "", fmt.Errorf(
				"aicert: task %s scenario %s run %d: judge attempt served on a budget-degraded route — refusing to trust a demoted grader",
				task, sc.Name, i+1)
		}
		if err := acc.addRun(task, sc, i, outcome); err != nil {
			return "", err
		}
		scenarioResults = append(scenarioResults, outcome.RunResult)
	}
	scenarioVerdict, _ := Verdict(scenarioResults, sc.Expect.Bands)
	return scenarioVerdict, nil
}

// runOutcome is one scored run plus the identity fields Record needs
// that RunResult itself has no room for (RunResult is score.go's public,
// runner-agnostic shape). JudgeDegraded mirrors RunResult.Degraded's
// candidate-side signal for the judge's own trace — certifyTask checks
// both before ever trusting an outcome.
// CertifiedScope is read off the case's OWN site rather than the scenario's
// name for it, because the site is what says how much of the invocation the
// case actually drives.
type runOutcome struct {
	RunResult
	Provider, ServedModel, ServedIdentitySource, JudgeServedModel string
	CertifiedScope                                                string
	JudgeDegraded                                                 bool
}

// runOnce drives exactly one prepared case and its judge score, cache off, so
// no repeat ever collapses onto a prior one's answer. A degraded CANDIDATE
// attempt short-circuits before the judge is ever called: certifyTask voids the
// whole task's record on outcome.Degraded regardless of what the judge says, so
// scoring a demoted answer would be a real, paid judge call spent on a result
// guaranteed to be thrown away.
//
// One run is not one call. How many calls a site makes is the site's own
// decision — a retry, a fallback, a whole tool loop — so everything recorded
// between the mark taken here and the case's return is what this run did, and
// all of it is pooled (runCalls). Accounting a run by its last call alone would
// let a degraded first attempt certify, hide a mid-run model swap, and
// under-count what the run spent.
//
// The case is prepared, run and evaluated here rather than built from the
// scenario: the request is the one the site's own code issues and the verdict
// is the one the site's own validator reaches, so a run measures what ships
// instead of a corpus author's description of it.
func runOnce(ctx context.Context, candidate *ai.Router, candidateRec *traceRecorder, judge *ai.Router, judgeRec *traceRecorder, sc Scenario, task ai.Task, census *aitasks.Registry, log *slog.Logger, trace *payloadTrace, run int) (runOutcome, error) {
	factory, bound := census.CaseFor(task, sc.Site)
	if !bound {
		return runOutcome{}, fmt.Errorf("no certification case is bound to site %s/%s", task, sc.Site)
	}
	prepared, err := factory.Prepare(json.RawMessage(sc.Fixture), json.RawMessage(sc.Expect.Answer))
	if err != nil {
		return runOutcome{}, fmt.Errorf("preparing the case: %w", err)
	}

	caseTrace, pooled, err := driveCandidate(ctx, prepared, candidate, candidateRec, task, sc, run, trace, log)
	if err != nil {
		return runOutcome{}, err
	}
	if pooled.Degraded {
		return runOutcome{RunResult: RunResult{Degraded: true}}, nil
	}

	// The site's own validator, over the site's own trace: Evaluate reports a
	// measurement, so a refused reply and a wrong answer stay distinguishable
	// instead of collapsing into one failed run.
	evaluated := prepared.Evaluate(caseTrace)
	if !aitasks.KnownOutcome(evaluated.Result) {
		return runOutcome{}, fmt.Errorf(
			"the case for site %s/%s evaluated to %q, which is not one of the outcomes a reply can have — a run counted under no outcome would leave the record's own totals unable to add up",
			task, sc.Site, evaluated.Result)
	}
	capsOK, capFailures := checkCaps(sc.Expect.Caps, pooled)
	// A run passes when what happened is what the scenario said should happen.
	// Comparing against a fixed "accepted" instead would make expect.outcome a
	// declaration the harness ignores — and would leave a scenario whose right
	// answer is a refusal unable to say so.
	outcomeAsExpected := evaluated.Result == sc.Expect.Outcome
	if !outcomeAsExpected || !capsOK {
		log.WarnContext(ctx, "aicert: run did not pass its validator/caps gate",
			"task", string(task), "scenario", sc.Name, "site", sc.Site,
			"outcome", evaluated.Result, "want_outcome", sc.Expect.Outcome,
			"detail", evaluated.Detail, "cap_failures", capFailures)
	}

	// The judge reads what production's parsers read: the unfenced text (every
	// serving path strips markdown fences before json.Unmarshal, so a fence is
	// presentation, not a defect).
	output := ai.Unfence(caseTrace.Output)

	judgeMark := judgeRec.mark()
	score, judgeServedModel, judgeDegraded, err := judgeScore(ctx, judge, judgeRec, sc, output, log)
	if err != nil {
		return runOutcome{}, fmt.Errorf("judge: %w", err)
	}
	judgeCalls, err := judgeRec.terminalsSince(judgeMark)
	if err != nil {
		return runOutcome{}, fmt.Errorf("judge: %w", err)
	}
	traceCalls(ctx, trace, "judge", task, sc, run, judgeCalls, log)

	return runOutcome{
		RunResult: RunResult{
			Output:           output,
			Outcome:          evaluated.Result,
			LatencyMS:        pooled.LatencyMS,
			TokensIn:         pooled.TokensIn,
			TokensOut:        pooled.TokensOut,
			CachedTokens:     pooled.CachedTokens,
			CacheWriteTokens: pooled.CacheWriteTokens,
			HardPass:         outcomeAsExpected && capsOK,
			Score:            score,
		},
		Provider:             pooled.Provider,
		ServedModel:          pooled.ServedModel,
		ServedIdentitySource: pooled.ServedIdentitySource,
		CertifiedScope:       factory.Site().CertifiedScope(),
		JudgeServedModel:     judgeServedModel,
		JudgeDegraded:        judgeDegraded,
	}, nil
}

// driveCandidate runs the prepared case over the candidate router and returns
// what the case did together with what its calls cost, pooled.
//
// The mark is taken before the case is handed the router, so what comes back is
// every call THIS run made and nothing a previous one left behind. A run whose
// calls were not all served by one model is refused here rather than pooled: it
// has no single (provider, model) heading to be certified under.
func driveCandidate(ctx context.Context, prepared aitasks.PreparedCase, candidate *ai.Router, candidateRec *traceRecorder,
	task ai.Task, sc Scenario, run int, trace *payloadTrace, log *slog.Logger,
) (aitasks.Trace, runCalls, error) {
	mark := candidateRec.mark()
	caseTrace, err := prepared.Run(ctx, routedCompleter{router: candidate, task: task})
	if err != nil {
		return aitasks.Trace{}, runCalls{}, fmt.Errorf("candidate call: %w", err)
	}
	calls, err := candidateRec.terminalsSince(mark)
	if err != nil {
		return aitasks.Trace{}, runCalls{}, fmt.Errorf("candidate call: %w", err)
	}
	traceCalls(ctx, trace, "candidate", task, sc, run, calls, log)
	pooled, err := poolRunCalls(calls)
	if err != nil {
		return aitasks.Trace{}, runCalls{}, fmt.Errorf("candidate call: %w", err)
	}
	if err := pooled.servedUniformly(); err != nil {
		return aitasks.Trace{}, runCalls{}, fmt.Errorf("candidate call: %w", err)
	}
	return caseTrace, pooled, nil
}

// routedCompleter hands a prepared case the one model call it may make,
// bound to the task under certification. A case names no task and no router:
// it knows the request its site sends, and the harness decides which candidate
// binding answers it.
type routedCompleter struct {
	router *ai.Router
	task   ai.Task
}

func (c routedCompleter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := c.router.Complete(ctx, c.task, req)
	if err != nil {
		return model.Response{}, fmt.Errorf("aicert: %s: %w", c.task, err)
	}
	return resp, nil
}

// runCalls is every logical call one run made, folded into the single
// accounting a RunResult keeps.
//
// Each field folds the way its own meaning demands, and none of them is the
// last call's value:
//
//   - Degraded is true when ANY call was served on a budget-degraded route. A
//     demoted first attempt followed by a healthy retry is still a demoted
//     answer inside a certified run, and §5 voids the record for it.
//   - The four token buckets and the latency SUM: they are what the run spent,
//     and a run that spent it over three calls spent it.
//   - Provider/ServedModel/ServedIdentitySource are the FIRST call's, which is
//     the whole run's whenever servedUniformly says so. A caller that certifies
//     a (provider, model) heading asks that question; the judge, whose score is
//     whichever attempt parsed, does not.
type runCalls struct {
	Calls                                       []ai.Call
	Degraded                                    bool
	Provider, ServedModel, ServedIdentitySource string
	TokensIn, TokensOut                         int
	CachedTokens, CacheWriteTokens              int
	ReasoningTokens                             int
	LatencyMS                                   int64
}

// poolRunCalls folds one run's calls into that accounting. A run with no call
// at all is refused rather than folded to zeroes: a scored run that made no
// model call is a harness fault, and zeroes would report it as a free, instant,
// healthy one.
func poolRunCalls(calls []ai.Call) (runCalls, error) {
	if len(calls) == 0 {
		return runCalls{}, fmt.Errorf("no model call was recorded, so there is nothing to score")
	}
	first := calls[0]
	pooled := runCalls{
		Calls:                calls,
		Provider:             first.Provider,
		ServedModel:          first.ServedModel,
		ServedIdentitySource: first.ServedIdentitySource,
	}
	for _, c := range calls {
		pooled.Degraded = pooled.Degraded || c.Degraded
		pooled.TokensIn += c.TokensIn
		pooled.TokensOut += c.TokensOut
		pooled.CachedTokens += c.CachedTokens
		pooled.CacheWriteTokens += c.CacheWriteTokens
		pooled.ReasoningTokens += c.ReasoningTokens
		pooled.LatencyMS += c.LatencyMS
	}
	return pooled, nil
}

// servedUniformly reports whether one model answered the whole run, naming both
// identities when one did not.
//
// A mid-run ladder fallback is the same defect as a mid-SET one: a record that
// pooled it would report an answer partly produced by one model and partly by
// another under a single (provider, model) heading, and nothing in the record
// would ever show it. The fix is a re-run once the ladder is stable, not an
// edit, so the message names what to compare rather than what to change.
func (r runCalls) servedUniformly() error {
	for i, c := range r.Calls {
		if c.Provider != r.Provider || c.ServedModel != r.ServedModel {
			return fmt.Errorf(
				"call %d of %d was served by %s:%s, but call 1 was served by %s:%s — refusing to certify one run answered by two models",
				i+1, len(r.Calls), c.Provider, c.ServedModel, r.Provider, r.ServedModel)
		}
	}
	return nil
}
