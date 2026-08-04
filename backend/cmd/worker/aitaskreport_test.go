// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestHitCapIsTheCeilingReached(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxTokens int
		out       int
		want      bool
	}{
		{"a reply that used its whole budget", 8192, 8192, true},
		{"a reply that somehow exceeded it", 8192, 8300, true},
		{"a reply with room to spare", 8192, 100, false},
		{"no ceiling asked for, so none can be reached", 0, 9000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hitCap(
				model.Response{OutputTokens: tc.out},
				model.Request{MaxTokens: tc.maxTokens},
			)
			if got != tc.want {
				t.Errorf("hitCap(out=%d, max=%d) = %v, want %v", tc.out, tc.maxTokens, got, tc.want)
			}
		})
	}
}

// The cap flag is the line that turned "the refresh errored" into a cause, so
// it is asserted on the rendered report rather than only on the predicate.
func TestReportFlagsAReplyThatFilledItsBudget(t *testing.T) {
	res := probeResult{
		Site: "rate_extract/pricing", Kind: "one_shot", Scope: "full_invocation",
		Binding: "routing x.yaml", Ladder: "premium,cheap_cloud",
		ContextCaveat: "company context not declared for this site",
		FixtureBytes:  589194, HasExpectation: true,
		Calls: []probeCall{{
			Request:  model.Request{System: "sys", MaxTokens: 8192},
			Response: model.Response{OutputTokens: 8192, InputTokens: 175453, Text: "{"},
			Reply:    probeReply{OutputTokens: 8192, InputTokens: 175453, Text: "{"},
			Latency:  time.Second,
		}},
	}
	var out strings.Builder
	if err := writeProbeReport(&out, res); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	got := out.String()
	for _, want := range []string{"rate_extract/pricing", "scope=full_invocation", "HIT CAP", "max_tokens 8192", "in 175453 tok"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}

	res.Calls[0].Response.OutputTokens = 12
	res.Calls[0].Reply.OutputTokens = 12
	var roomy strings.Builder
	if err := writeProbeReport(&roomy, res); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	if strings.Contains(roomy.String(), "HIT CAP") {
		t.Errorf("a reply with room to spare must not be flagged:\n%s", roomy.String())
	}
}

// A probe that hid its coverage would read as more than it bought, so the
// report must say when no expectation was supplied and when the company context
// production prepends was not assembled.
func TestReportRefusesToHideWhatItDidNotCover(t *testing.T) {
	res := probeResult{
		Site: "draft_reply/reply", Kind: "one_shot", Scope: "full_invocation",
		ContextCaveat:  contextCaveat(ai.TaskDraftReply),
		HasExpectation: false,
	}
	var out strings.Builder
	if err := writeProbeReport(&out, res); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "no expectation supplied") {
		t.Errorf("report must say a wrong answer cannot be detected:\n%s", got)
	}
	if !strings.Contains(got, "NOT assembled") {
		t.Errorf("draft_reply declares a company context this lane cannot build; the report must say so:\n%s", got)
	}
}

func TestContextCaveatSeparatesDeclaredFromAbsent(t *testing.T) {
	// rate_extract declares no company context, so there is nothing missing —
	// a warning here would be noise that trains the reader to skip the line.
	if got := contextCaveat(ai.TaskRateExtract); !strings.Contains(got, "not declared") {
		t.Errorf("contextCaveat(rate_extract) = %q, want it to report none is declared", got)
	}
	got := contextCaveat(ai.TaskDraftReply)
	if !strings.Contains(got, "positioning") || !strings.Contains(got, "NOT assembled") {
		t.Errorf("contextCaveat(draft_reply) = %q, want the declared scopes and that they are missing", got)
	}
}

func TestReportCarriesTheOutcomeAndItsDetail(t *testing.T) {
	outcome := aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: "parse extraction: unexpected end of JSON input"}
	var out strings.Builder
	if err := writeProbeReport(&out, probeResult{Site: "s/v", Outcome: &outcome}); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	if !strings.Contains(out.String(), "invalid — parse extraction") {
		t.Errorf("the validator's own reason must reach the report:\n%s", out.String())
	}
}

// A failure is a harness problem and an outcome is a measurement; a report that
// rendered them the same way would make a bad answer look like a broken tool.
func TestReportSeparatesFailureFromOutcome(t *testing.T) {
	var out strings.Builder
	if err := writeProbeReport(&out, probeResult{Site: "s/v", Failure: "the fixture is not the shape this site takes"}); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "failed") {
		t.Errorf("a harness failure must render as failed:\n%s", got)
	}
	if strings.Contains(got, "evaluate") {
		t.Errorf("nothing was evaluated, so no evaluate line may appear:\n%s", got)
	}
}

// passages is the count numberPassages would emit. A single-line body numbers
// to ONE passage however many bytes it carries — invisible in a byte count, and
// decisive for whether evidence citation can mean anything.
func TestFetchBoundaryLineCountsPassagesNotLines(t *testing.T) {
	oneLine := webread.Doc{Text: strings.Repeat("x", 500_000), MediaType: "application/json"}
	got := fetchBoundaryLine(oneLine)
	if !strings.Contains(got, "passages=1") {
		t.Errorf("a single-line body numbers to one passage, got %q", got)
	}
	if !strings.Contains(got, "bytes=500000") {
		t.Errorf("the byte count must survive, got %q", got)
	}

	threeLines := webread.Doc{Text: "a\n\nb\n   \nc\n", MediaType: "text/markdown"}
	if got := fetchBoundaryLine(threeLines); !strings.Contains(got, "passages=3") {
		t.Errorf("blank lines are not passages; want passages=3, got %q", got)
	}
}

func TestFetchBoundaryLineNamesAnUndeclaredMediaType(t *testing.T) {
	got := fetchBoundaryLine(webread.Doc{Text: "x"})
	if !strings.Contains(got, "(none declared)") {
		t.Errorf("a server declaring no media type must be reported as such, got %q", got)
	}
}

func TestHumanTokensReadsAtTheScaleThatMatters(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1k", 132464: "132k"} {
		if got := humanTokens(in); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

// The provider's own word beats the routing's binding: a vendor may silently
// substitute a model, and that difference is what a surprising result is
// explained by.
func TestServedIdentityPrefersWhatAnsweredOverWhatWasBound(t *testing.T) {
	both := probeCall{
		Reply: probeReply{ServedModel: "actually-served"},
		Route: ai.RouteInfo{ModelID: "was-bound"},
	}
	if got := servedIdentity(both); got != "actually-served" {
		t.Errorf("servedIdentity = %q, want the provider-reported identity", got)
	}
	boundOnly := probeCall{Route: ai.RouteInfo{ModelID: "was-bound"}}
	if got := servedIdentity(boundOnly); got != "was-bound" {
		t.Errorf("servedIdentity = %q, want the binding when the provider reported none", got)
	}
	if got := servedIdentity(probeCall{}); got != "" {
		t.Errorf("servedIdentity = %q, want empty when nothing is known", got)
	}
}

// By the time a request is built the page text has been WRAPPED: the fence adds
// an opening and a closing marker of its own. Counting non-empty lines would
// report a one-model payload as three passages — a false claim about evidence
// coverage, on the very number the guide tells operators to read.
func TestPayloadPassagesIgnoreTheFenceMarkers(t *testing.T) {
	req := model.Request{Messages: []model.Message{{Role: "user", Content: strings.Join([]string{
		"<untrusted-019fcad9-cec6-7533-b938-7be67a2f9ea9>",
		"[s0] vendor/a costs $1",
		"[s1] vendor/b costs $2",
		"</untrusted-019fcad9-cec6-7533-b938-7be67a2f9ea9>",
	}, "\n")}}}
	if got := payloadPassages(req); got != 2 {
		t.Errorf("payloadPassages = %d, want 2 — the fence markers are not passages", got)
	}
}

func TestIsNumberedPassageMatchesOnlyTheNumbering(t *testing.T) {
	for line, want := range map[string]bool{
		"[s0] a":        true,
		"[s12] a":       true,
		"[s0]":          true,
		"[sx] a":        false,
		"[s] a":         false,
		"<untrusted-1>": false,
		"plain text":    false,
		"":              false,
	} {
		if got := isNumberedPassage(line); got != want {
			t.Errorf("isNumberedPassage(%q) = %v, want %v", line, got, want)
		}
	}
}
