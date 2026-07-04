package wiring

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestPRTemplate_requiresTheAccountabilitySections(t *testing.T) {
	tmpl := readRepoFile(t, ".github/PULL_REQUEST_TEMPLATE.md")
	for _, want := range []string{"## What", "## Why", "How verified", "AI involvement", "explain every line"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("PR template missing %q", want)
		}
	}
}

func TestContributing_statesAccountabilityAndDisclosure(t *testing.T) {
	doc := readRepoFile(t, "CONTRIBUTING.md")
	for _, want := range []string{"accountable", "explain every line", "DCO", "Signed-off-by", "craftsmanship gate"} {
		if !strings.Contains(doc, want) {
			t.Errorf("CONTRIBUTING.md missing %q", want)
		}
	}
}

func TestCIWorkflow_externalPRsHitTheGateJobs(t *testing.T) {
	yml := readRepoFile(t, ".github/workflows/ci.yml")
	// The dco + craftsmanship + craft-residue jobs are defined so external/fork PRs
	// hit the same gate as internal work.
	if !regexp.MustCompile(`(?m)^\s+dco:`).MatchString(yml) {
		t.Error("ci.yml missing the dco job")
	}
	for _, job := range []string{"craftsmanship:", "craft-residue:"} {
		if !strings.Contains(yml, job) {
			t.Errorf("ci.yml missing job %q that external PRs must also hit", job)
		}
	}
	// CI is enabled: the automatic pull_request trigger is what makes external PRs
	// hit these jobs without a maintainer, and ready_for_review fires the full suite
	// when a draft PR is marked ready (the dark-factory swarm's final-state pass).
	if !regexp.MustCompile(`(?m)^\s+pull_request:`).MatchString(yml) {
		t.Error("ci.yml must trigger on pull_request so external PRs hit the gate jobs")
	}
	if !strings.Contains(yml, "ready_for_review") {
		t.Error("ci.yml pull_request trigger must include ready_for_review")
	}
}
