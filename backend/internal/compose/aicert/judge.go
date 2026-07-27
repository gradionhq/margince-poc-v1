// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The judge's call-and-retry drive and the per-run caps gate — what
// runner.go's certifyTask needs beyond the prepared case itself to turn one
// scored answer into one RunResult, split out of runner.go to keep that file
// to the orchestration loop.
//
// The candidate's request is NOT built here, and no longer anywhere in this
// package: each site's own case issues the request its production code
// issues. A scenario's caps.max_tokens therefore grades the answer the model
// gave (checkCaps below); the ceiling the model was handed is the shipped
// builder's, which is the whole point of certifying it.
//
// The judge's own prompt and verdict parse are NOT here either: cert_judge is
// a registered invocation site, and a site's prompt is built in compose
// (compose.JudgeRequest / compose.ParseJudgeVerdict) so the census can
// certify it like every other.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// judgeScore drives the judge router for one candidate output: one call,
// one retry on a parse failure, then a 0 score with the parse error
// logged rather than propagated — a flaky grader must never abort an
// otherwise-healthy certification run. judgeServedModel is read back
// from rec's own terminal trace (never resp.ServedModel directly) so it
// carries the same resolved identity (response vs. echo vs. configured
// fallback) the candidate side reports, and names the attempt the score
// came from. judgeDegraded is true when ANY attempt was demoted, the
// retry included: the spec's "any Degraded attempt voids the record"
// rule applies to the judge exactly like the candidate, and a demotion
// the retry recovered from still means this run's grading budget ran
// out — which must never be certified silently.
func judgeScore(ctx context.Context, judge *ai.Router, rec *traceRecorder, sc Scenario, candidateOutput string, log *slog.Logger) (score int, judgeServedModel string, judgeDegraded bool, err error) {
	mark := rec.mark()
	score, judgeServedModel, err = judgeVerdict(ctx, judge, rec, sc, candidateOutput, log)
	if err != nil {
		return 0, "", false, err
	}
	calls, err := rec.terminalsSince(mark)
	if err != nil {
		return 0, "", false, fmt.Errorf("judge call: %w", err)
	}
	// The degrade is folded across every attempt by the one spelling the
	// candidate side uses, rather than read off the attempt that scored: a
	// demotion the retry recovered from still means this run was graded on a
	// budget that had run out.
	graded, err := poolRunCalls(calls)
	if err != nil {
		return 0, "", false, fmt.Errorf("judge call: %w", err)
	}
	return score, judgeServedModel, graded.Degraded, nil
}

// judgeVerdict drives the call and its one retry, returning the score and the
// served identity of the attempt the score actually came from — which is the
// retry's whenever one ran, since that is the reply that was parsed.
func judgeVerdict(ctx context.Context, judge *ai.Router, rec *traceRecorder, sc Scenario, candidateOutput string, log *slog.Logger) (int, string, error) {
	// The fixture is what the candidate was answering ABOUT, so it is what the
	// grader is shown alongside the answer: under the fixture format there is no
	// scenario-authored prompt to show it instead.
	resp, _, callErr := judge.Complete(ctx, ai.TaskCertJudge,
		compose.JudgeRequest(sc.Expect.Rubric, string(sc.Fixture), candidateOutput))
	if callErr != nil {
		return 0, "", fmt.Errorf("judge call: %w", callErr)
	}
	term, ok := rec.lastTerminal()
	if !ok {
		return 0, "", fmt.Errorf("judge call: no terminal trace recorded")
	}
	judgeServedModel := term.ServedModel

	verdict, parseErr := compose.ParseJudgeVerdict(resp.Text)
	if parseErr == nil {
		return verdict.Score, judgeServedModel, nil
	}
	log.WarnContext(ctx, "aicert: judge output failed to parse, retrying once",
		"scenario", sc.Name, "err", parseErr)

	// The retry is BUILT again, never re-sent: JudgeRequest mints the call's data
	// boundary, and the attempt that just failed was shown the first one.
	resp2, _, callErr2 := judge.Complete(ctx, ai.TaskCertJudge,
		compose.JudgeRequest(sc.Expect.Rubric, string(sc.Fixture), candidateOutput))
	if callErr2 != nil {
		return 0, "", fmt.Errorf("judge retry call: %w", callErr2)
	}
	if term2, ok2 := rec.lastTerminal(); ok2 {
		judgeServedModel = term2.ServedModel
	}
	verdict2, parseErr2 := compose.ParseJudgeVerdict(resp2.Text)
	if parseErr2 != nil {
		log.ErrorContext(ctx, "aicert: judge output failed to parse twice — scoring this run 0",
			"scenario", sc.Name, "err", parseErr2)
		return 0, judgeServedModel, nil
	}
	return verdict2.Score, judgeServedModel, nil
}

// selfJudged reports whether the judge and the candidate were served by
// the same resolved model identity — a judge grading its own family's
// output is a weaker signal than an independent one, so the record
// names it rather than hiding it inside an unqualified score. An empty
// candidate identity never counts as self-judged — that is a missing
// trace, not a match.
func selfJudged(candidateServedModel, judgeServedModel string) bool {
	return candidateServedModel != "" && candidateServedModel == judgeServedModel
}

// cloudServed reports whether provider names a network-hosted vendor, so
// the scenario's P95 latency cap only ever judges a call whose latency
// reflects a real network round-trip, never a same-host inference
// engine's hardware (spec: "Caps.P95LatencyMS applies to cloud-served
// candidates only"). Delegates to ai.ProviderIsLocal rather than
// re-encoding that set here — a second copy could drift from the one
// ai's own conformance test binds.
func cloudServed(provider string) bool {
	return !ai.ProviderIsLocal(provider)
}

// checkCaps reports whether a run's usage stays within sc's resource
// ceilings, alongside a human-readable reason per breach — a run over
// cap fails HardPass exactly like a failed structural check, never
// silently.
//
// The ceilings govern the RUN, so they are read off its pooled calls: a site
// that answers in three requests spent all three, and a cap charged to the last
// one alone would pass a run that blew its budget twice over on the way there.
func checkCaps(caps Caps, run runCalls) (ok bool, failures []string) {
	if caps.MaxTokens > 0 {
		// caps.max_tokens budgets the model's ANSWER — the reply it
		// generates — never the scenario's fixed input (which the model
		// cannot shrink) nor the internal thinking a reasoning model spends
		// before answering (that thinking is not the answer the cap governs;
		// see runMaxOutputTokens and ai.ReasoningOutputMaxTokens). Grade the
		// answer alone, so a rich-input scenario with a tight OUTPUT cap
		// tests what it means to — did the model draft within budget — rather
		// than failing on input size a bigger prompt would always blow.
		answer := run.TokensOut - run.ReasoningTokens
		if answer > caps.MaxTokens {
			failures = append(failures, fmt.Sprintf("max_tokens cap %d exceeded: %d answer tokens", caps.MaxTokens, answer))
		}
	}
	if caps.P95LatencyMS > 0 && cloudServed(run.Provider) && run.LatencyMS > caps.P95LatencyMS {
		failures = append(failures, fmt.Sprintf("p95_latency_ms cap %d exceeded: %dms", caps.P95LatencyMS, run.LatencyMS))
	}
	return len(failures) == 0, failures
}
