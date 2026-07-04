package golden

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/cli/craft/gate"
	"github.com/gradionhq/margince/cli/craft/rubric"
)

// scriptedClient stands in for the model: it returns a canned result based on
// what slop it "sees" in the prompt, so the harness is tested deterministically.
type scriptedClient struct{ alwaysPass bool }

func (s *scriptedClient) Complete(_ context.Context, prompt string) (string, error) {
	if !s.alwaysPass && strings.Contains(prompt, "increment i") {
		return finding("over-commenting"), nil
	}
	if !s.alwaysPass && strings.Contains(prompt, "var data any") {
		return finding("type-escape-hatch"), nil
	}
	return `{"scratchpad":"clean","verdict":"PASS","findings":[]}`, nil
}

func finding(category string) string {
	return `{"scratchpad":"slop","verdict":"BLOCK","findings":[{"id":"f1","file":"x.go","line":4,"category":"` +
		category + `","severity":"BLOCKER","confidence":"high","rationale":"r","suggested_fix":"x"}]}`
}

func newReviewer(t *testing.T, alwaysPass bool) *gate.Reviewer {
	t.Helper()
	r, err := rubric.Load()
	if err != nil {
		t.Fatalf("load rubric: %v", err)
	}
	return gate.NewReviewer(&scriptedClient{alwaysPass: alwaysPass}, r, "test-gate")
}

func TestCorpus_growthInvariant_confirmedCasesAllPresent(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	present := map[string]bool{}
	for _, cs := range c.Cases {
		present[cs.ID] = true
	}
	confirmed, err := ConfirmedIDs()
	if err != nil {
		t.Fatalf("load confirmed: %v", err)
	}
	if len(confirmed) == 0 {
		t.Fatal("confirmed set is empty")
	}
	for _, id := range confirmed {
		if !present[id] {
			t.Errorf("confirmed case %q was pruned from the corpus — the golden set only grows", id)
		}
	}
}

func TestRun_replaysEveryConfirmedCase(t *testing.T) {
	c, _ := Load()
	outcomes, err := Run(context.Background(), newReviewer(t, false), c.Cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcomes) != len(c.Cases) {
		t.Fatalf("ran %d cases, corpus has %d", len(outcomes), len(c.Cases))
	}
	for _, o := range outcomes {
		if !o.VerdictMatch {
			t.Errorf("case %s: got %s, want %s", o.CaseID, o.GotVerdict, o.WantVerdict)
		}
	}
}

func TestRun_detectsRegression(t *testing.T) {
	c, _ := Load()
	// A gate that never blocks must fail the slop cases — proving Run surfaces a
	// regression rather than rubber-stamping.
	outcomes, err := Run(context.Background(), newReviewer(t, true), c.Cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var mismatches int
	for _, o := range outcomes {
		if !o.VerdictMatch {
			mismatches++
		}
	}
	if mismatches == 0 {
		t.Error("an always-PASS gate should mismatch the slop cases")
	}
}
