// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cert_judge site: the rubric-scoring request the certification lane sends
// its grader, and the strict read of the verdict that comes back.
//
// It sits in this layer beside every other site's prompt because it is one —
// the task contract registers cert_judge/judge, and this package builds what
// that site sends. The harness that drives the grader (compose/aicert) imports
// this package and never the reverse, so a judge built inside that harness is a
// site the census could name but never certify: certification cases are bound
// here.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

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

// JudgeRequest builds the judge's own completion request: the rubric,
// the input the candidate was given for context, and the candidate's raw
// output to score — never the candidate's system prompt or history, only
// what a grader needs to judge the answer actually produced. The
// candidate's output is interpolated verbatim, so a candidate CAN address
// the judge in its answer — accepted for this manually-run internal lane
// (fixed grader system prompt, separate never-overridden router, strict
// range-checked verdict parse, self_judged surfaced on the record);
// anything higher-stakes than a committed QA record needs a delimited
// or tool-forced verdict channel first.
func JudgeRequest(rubric, scenarioInput, candidateOutput string) model.Request {
	user := fmt.Sprintf("Rubric:\n%s\n\nScenario input:\n%s\n\nCandidate output:\n%s", rubric, scenarioInput, candidateOutput)
	return model.Request{
		System:    judgeSystemPrompt,
		Messages:  []model.Message{{Role: chatRoleUser, Content: user}},
		MaxTokens: judgeMaxTokens,
	}
}

// JudgeVerdict is the judge's strict-JSON reply shape.
type JudgeVerdict struct {
	Score  int    `json:"score"`
	Reason string `json:"reason"`
}

// ParseJudgeVerdict parses the judge's raw text strictly: invalid JSON,
// an unexpected shape, or a score outside 0-100 are all refused so a
// caller's one retry has a genuine chance to recover a judge that emitted
// a stray token around its JSON, rather than silently accepting a
// nonsense score.
func ParseJudgeVerdict(text string) (JudgeVerdict, error) {
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &v); err != nil {
		return JudgeVerdict{}, fmt.Errorf("judge output is not the expected JSON object: %w", err)
	}
	if v.Score < 0 || v.Score > 100 {
		return JudgeVerdict{}, fmt.Errorf("judge score %d is outside 0-100", v.Score)
	}
	return v, nil
}
