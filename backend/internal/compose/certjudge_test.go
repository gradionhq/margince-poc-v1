// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the grader's own prompt and verdict read owe their caller: a request that
// carries the three things a grader is given, and a read strict enough that the
// harness's one retry has something to recover from.

import (
	"strings"
	"testing"
)

func TestParseJudgeVerdictAcceptsTheStrictShape(t *testing.T) {
	v, err := ParseJudgeVerdict(`{"score": 82, "reason": "grounded, on-topic"}`)
	if err != nil {
		t.Fatalf("valid judge output rejected: %v", err)
	}
	if v.Score != 82 || v.Reason != "grounded, on-topic" {
		t.Fatalf("parsed %+v, want score=82 reason=%q", v, "grounded, on-topic")
	}
}

func TestParseJudgeVerdictRefusesInvalidJSON(t *testing.T) {
	if _, err := ParseJudgeVerdict("not json at all"); err == nil {
		t.Fatal("want an error for non-JSON judge output")
	}
}

func TestParseJudgeVerdictRefusesAnOutOfRangeScore(t *testing.T) {
	cases := []string{
		`{"score": 101, "reason": "too high"}`,
		`{"score": -1, "reason": "negative"}`,
	}
	for _, raw := range cases {
		if _, err := ParseJudgeVerdict(raw); err == nil {
			t.Fatalf("want an error for out-of-range score in %q", raw)
		}
	}
}

// The grader is shown what it grades and what it grades against, and nothing
// else: a candidate that tried to redirect its own instructions must not find
// them here.
func TestJudgeRequestCarriesTheRubricTheInputAndTheOutput(t *testing.T) {
	req := JudgeRequest("Score higher for a concrete answer.", "Describe the widget.", "The widget is blue.")

	if req.System != judgeSystemPrompt {
		t.Errorf("System = %q, want the fixed grader instruction", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != chatRoleUser {
		t.Fatalf("want one user turn, got %+v", req.Messages)
	}
	content := req.Messages[0].Content
	for _, want := range []string{"Score higher for a concrete answer.", "Describe the widget.", "The widget is blue."} {
		if !strings.Contains(content, want) {
			t.Errorf("the user turn does not carry %q: %q", want, content)
		}
	}
	if req.MaxTokens != judgeMaxTokens {
		t.Errorf("MaxTokens = %d, want the grader's reasoning-headroom cap %d", req.MaxTokens, judgeMaxTokens)
	}
}
