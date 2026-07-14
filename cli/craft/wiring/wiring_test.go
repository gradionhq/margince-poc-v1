// Package wiring asserts the CI-ordering invariant that keeps the craftsmanship
// gate running only after the deterministic gates are green (ADR-0045). This is
// a structural assertion over the tracked workflow, not a live GitHub call.
package wiring

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// repoRoot is three levels up from this test's package dir (cli/craft/wiring).
const repoRoot = "../../.."

func TestCIWorkflow_craftsmanshipRunsAfterDeterministicGates(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	s := string(yml)

	for _, job := range []string{"deterministic-gates:", "craftsmanship:"} {
		if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(job)).MatchString(s) {
			t.Errorf("ci.yml missing job %q", job)
		}
	}
	// The ordering invariant: craftsmanship declares a dependency on the gate job.
	if !regexp.MustCompile(`needs:\s*\[?\s*deterministic-gates`).MatchString(s) {
		t.Error("craftsmanship job must `needs: [deterministic-gates]` so it runs only after the deterministic gates are green")
	}
}
