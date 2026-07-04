package gate

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

// fakeClient records the prompt it was asked to complete and returns a canned
// response, so the test can assert both the assembled inputs and the parsing.
type fakeClient struct {
	gotPrompt string
	response  string
}

func (f *fakeClient) Complete(_ context.Context, prompt string) (string, error) {
	f.gotPrompt = prompt
	return f.response, nil
}

func TestReview_seededSlopDiff_producesScratchpadAndPerHunkFindings(t *testing.T) {
	r, err := rubric.Load()
	if err != nil {
		t.Fatalf("load rubric: %v", err)
	}

	// A seeded slop diff: an over-commented line (T1) and an `any` escape hatch (T6).
	in := Inputs{
		Diff: `--- a/crm/crm-core/handler_person.go
+++ b/crm/crm-core/handler_person.go
@@ -10,3 +10,5 @@
+	i++ // increment i
+	var data any = person`,
		TouchedFiles: map[string]string{
			"crm/crm-core/handler_person.go": "package crmcore\n// ... full file ...\n",
		},
		SiblingFiles: map[string]string{
			"crm/crm-core/handler_deal.go": "package crmcore\n",
		},
	}

	// The model is wrapped to keep the test deterministic; it returns a fenced
	// JSON result with two per-hunk findings.
	fc := &fakeClient{response: "```json\n" + `{
  "scratchpad": "hunk 1: comment restates code (T1); hunk 2: any cast (T6)",
  "verdict": "BLOCK",
  "findings": [
    {"id":"f1","file":"crm/crm-core/handler_person.go","line":11,"category":"over-commenting","severity":"BLOCKER","confidence":"high","rationale":"comment restates the code","suggested_fix":"delete the comment"},
    {"id":"f2","file":"crm/crm-core/handler_person.go","line":12,"category":"type-escape-hatch","severity":"BLOCKER","confidence":"high","rationale":"any dodges the real type","suggested_fix":"use crmcore.Person"}
  ]
}` + "\n```"}

	rv := NewReviewer(fc, r, "test-gate-v1")
	res, err := rv.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	if res.GateVersion != "test-gate-v1" {
		t.Errorf("gate version = %q, want test-gate-v1", res.GateVersion)
	}
	if res.Scratchpad == "" {
		t.Error("expected a non-empty scratchpad")
	}
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 per-hunk findings, got %d", len(res.Findings))
	}
	if res.Findings[0].Category != "over-commenting" || res.Findings[1].Category != "type-escape-hatch" {
		t.Errorf("unexpected finding categories: %q, %q", res.Findings[0].Category, res.Findings[1].Category)
	}

	// The runner must have assembled the rubric and the diff into the prompt.
	for _, want := range []string{"increment i", "var data any", "over-commenting", in.Diff} {
		if !strings.Contains(fc.gotPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestParseResult_rejectsOutputWithNoJSON(t *testing.T) {
	if _, err := parseResult("the model refused and wrote only prose"); err == nil {
		t.Error("expected an error when there is no JSON object")
	}
}

func TestExtractJSON_ignoresBracesInsideStrings(t *testing.T) {
	got := extractJSON(`prefix {"rationale":"a } brace in a string","line":1} suffix`)
	want := `{"rationale":"a } brace in a string","line":1}`
	if got != want {
		t.Errorf("extractJSON = %q, want %q", got, want)
	}
}
