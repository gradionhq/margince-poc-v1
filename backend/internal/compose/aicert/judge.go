// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The candidate's request shape, the judge's rubric-scoring prompt and
// its strict-JSON parse/retry, and the per-run caps gate — everything
// runner.go's certifyTask needs to turn one Scenario into one scored
// RunResult, split out of runner.go to keep that file to the
// orchestration loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// defaultRunMaxTokens bounds a candidate completion when a scenario
// names no caps.max_tokens. It is the shared reasoning-headroom output
// ceiling (ai.ReasoningOutputMaxTokens): a reasoning model spends output
// tokens on internal thinking before its answer, so a cap sized for the
// answer alone starves it into a MAX_TOKENS stop with zero visible text.
// See that constant's doc for the full rationale.
const defaultRunMaxTokens = ai.ReasoningOutputMaxTokens

// judgeMaxTokens bounds the judge's own reply. The verdict is one line
// of JSON, but reasoning models (Gemini 2.5, o-series) spend output
// tokens on internal thinking BEFORE the verdict — a tight cap starves
// the reply into a MAX_TOKENS stop with zero visible text, so the cap
// carries thinking headroom, not just verdict length.
const judgeMaxTokens = 4096

// judgeSystemPrompt is the fixed rubric-scorer instruction every judge
// call carries — never the candidate's own system prompt, so a
// candidate that tried to redirect its instructions cannot also redirect
// its grader.
const judgeSystemPrompt = `You are a strict grader for an AI certification harness. Score the candidate's output 0-100 against the rubric below. Reply with EXACTLY one JSON object and nothing else — no prose, no markdown fence: {"score": <integer 0-100>, "reason": "<one sentence>"}.`

// buildRequest turns one scenario into the candidate's completion
// request: its prior turns replayed as history, Input as the final
// user turn.
func buildRequest(sc Scenario) model.Request {
	messages := make([]model.Message, 0, len(sc.History)+1)
	for _, turn := range sc.History {
		messages = append(messages, model.Message{Role: turn.Role, Content: turn.Text})
	}
	messages = append(messages, model.Message{Role: "user", Content: sc.Input})
	maxTokens := sc.Expect.Caps.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultRunMaxTokens
	}
	return model.Request{System: sc.System, Messages: messages, MaxTokens: maxTokens}
}

// judgeRequest builds the judge's own completion request: the rubric,
// the scenario's input for context, and the candidate's raw output to
// score — never the candidate's system prompt or history, only what a
// grader needs to judge the answer actually produced. The candidate's
// output is interpolated verbatim, so a candidate CAN address the judge
// in its answer — accepted for this manually-run internal lane (fixed
// grader system prompt, separate never-overridden router, strict
// range-checked verdict parse, self_judged surfaced on the record);
// anything higher-stakes than a committed QA record needs a delimited
// or tool-forced verdict channel first.
func judgeRequest(sc Scenario, candidateOutput string) model.Request {
	user := fmt.Sprintf("Rubric:\n%s\n\nScenario input:\n%s\n\nCandidate output:\n%s", sc.Expect.Rubric, sc.Input, candidateOutput)
	return model.Request{
		System:    judgeSystemPrompt,
		Messages:  []model.Message{{Role: "user", Content: user}},
		MaxTokens: judgeMaxTokens,
	}
}

// judgeVerdict is the judge's strict-JSON reply shape.
type judgeVerdict struct {
	Score  int    `json:"score"`
	Reason string `json:"reason"`
}

// parseJudgeVerdict parses the judge's raw text strictly: invalid JSON,
// an unexpected shape, or a score outside 0-100 are all refused so a
// caller's one retry (judgeScore) has a genuine chance to recover a
// judge that emitted a stray token around its JSON, rather than
// silently accepting a nonsense score.
func parseJudgeVerdict(text string) (judgeVerdict, error) {
	var v judgeVerdict
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &v); err != nil {
		return judgeVerdict{}, fmt.Errorf("judge output is not the expected JSON object: %w", err)
	}
	if v.Score < 0 || v.Score > 100 {
		return judgeVerdict{}, fmt.Errorf("judge score %d is outside 0-100", v.Score)
	}
	return v, nil
}

// judgeScore drives the judge router for one candidate output: one call,
// one retry on a parse failure, then a 0 score with the parse error
// logged rather than propagated — a flaky grader must never abort an
// otherwise-healthy certification run. judgeServedModel is read back
// from rec's own terminal trace (never resp.ServedModel directly) so it
// carries the same resolved identity (response vs. echo vs. configured
// fallback) the candidate side reports. judgeDegraded mirrors the same
// rec's terminal Degraded flag off WHICHEVER attempt actually happened
// last (the retry's, when one ran) — the spec's "any Degraded attempt
// voids the record" rule applies to the judge exactly like the
// candidate: a budget-forced demotion here means the score itself came
// from a weaker grader, which must never be trusted silently.
func judgeScore(ctx context.Context, judge *ai.Router, rec *traceRecorder, sc Scenario, candidateOutput string, log *slog.Logger) (score int, judgeServedModel string, judgeDegraded bool, err error) {
	req := judgeRequest(sc, candidateOutput)
	resp, _, callErr := judge.Complete(ctx, ai.TaskCertJudge, req)
	if callErr != nil {
		return 0, "", false, fmt.Errorf("judge call: %w", callErr)
	}
	term, ok := rec.lastTerminal()
	if !ok {
		return 0, "", false, fmt.Errorf("judge call: no terminal trace recorded")
	}
	judgeServedModel = term.ServedModel
	judgeDegraded = term.Degraded

	verdict, parseErr := parseJudgeVerdict(resp.Text)
	if parseErr == nil {
		return verdict.Score, judgeServedModel, judgeDegraded, nil
	}
	log.WarnContext(ctx, "aicert: judge output failed to parse, retrying once",
		"scenario", sc.Name, "err", parseErr)

	resp2, _, callErr2 := judge.Complete(ctx, ai.TaskCertJudge, req)
	if callErr2 != nil {
		return 0, judgeServedModel, judgeDegraded, fmt.Errorf("judge retry call: %w", callErr2)
	}
	if term2, ok2 := rec.lastTerminal(); ok2 {
		judgeServedModel = term2.ServedModel
		judgeDegraded = term2.Degraded
	}
	verdict2, parseErr2 := parseJudgeVerdict(resp2.Text)
	if parseErr2 != nil {
		log.ErrorContext(ctx, "aicert: judge output failed to parse twice — scoring this run 0",
			"scenario", sc.Name, "err", parseErr2)
		return 0, judgeServedModel, judgeDegraded, nil
	}
	return verdict2.Score, judgeServedModel, judgeDegraded, nil
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

// checkCaps reports whether term's usage stays within sc's resource
// ceilings, alongside a human-readable reason per breach — a run over
// cap fails HardPass exactly like a failed structural check, never
// silently.
func checkCaps(caps Caps, term ai.Call) (ok bool, failures []string) {
	if caps.MaxTokens > 0 {
		total := term.TokensIn + term.TokensOut
		if total > caps.MaxTokens {
			failures = append(failures, fmt.Sprintf("max_tokens cap %d exceeded: %d tokens", caps.MaxTokens, total))
		}
	}
	if caps.P95LatencyMS > 0 && cloudServed(term.Provider) && term.LatencyMS > caps.P95LatencyMS {
		failures = append(failures, fmt.Sprintf("p95_latency_ms cap %d exceeded: %dms", caps.P95LatencyMS, term.LatencyMS))
	}
	return len(failures) == 0, failures
}
