// Package wiring asserts the CI ordering and branch-protection invariants that
// make the craftsmanship gate a required, no-override check that runs only after
// the deterministic gates (foundation architecture/16; B-EP11.3). These are
// structural assertions over the tracked config, not a live GitHub call.
package wiring

import (
	"encoding/json"
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

func TestBranchProtection_craftsmanshipRequiredWithNoOverride(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "infra/branch-protection.json"))
	if err != nil {
		t.Fatalf("read branch-protection.json: %v", err)
	}
	var cfg struct {
		RequiredStatusChecks struct {
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
		EnforceAdmins bool `json:"enforce_admins"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse branch-protection.json: %v", err)
	}
	if !contains(cfg.RequiredStatusChecks.Contexts, "craftsmanship") {
		t.Error("craftsmanship must be a required status check")
	}
	if !contains(cfg.RequiredStatusChecks.Contexts, "deterministic-gates") {
		t.Error("deterministic-gates must be a required status check")
	}
	// No-override: admins cannot bypass (ADR-0045).
	if !cfg.EnforceAdmins {
		t.Error("enforce_admins must be true — the craftsmanship block has no override path")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
