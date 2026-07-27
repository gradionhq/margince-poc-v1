// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"os"
	"strings"
	"testing"
)

// minimalContract is a 2-tier, 2-task contract shaped like
// api/ai-tasks.yaml but small enough to assert exact literals against.
const minimalContract = `
tiers: [alpha, beta]

tasks:
  foo: {ladder: [alpha, beta], execution_mode: background, on_budget_exhausted: queue, status: shipped, sites: [only]}
  bar: {ladder: [beta, alpha], execution_mode: interactive, on_budget_exhausted: degrade, status: planned}

degrade_to:
  beta: alpha
  alpha: alpha
`

// TestEmitGoProducesTaskConstantsLaddersAndExecutionModes is the
// Step 1 mechanical property: feeding a minimal contract to emitGo
// produces the constant, the ladder literal, and the derived
// execution-mode table — the shape tasks_gen.go must have for the real contract.
func TestEmitGoProducesTaskConstantsLaddersAndExecutionModes(t *testing.T) {
	c, err := parseContract([]byte(minimalContract))
	if err != nil {
		t.Fatalf("parseContract: %v", err)
	}

	out, err := emitGo(c, "deadbeef")
	if err != nil {
		t.Fatalf("emitGo: %v", err)
	}

	if !strings.Contains(out, `TaskFoo Task = "foo"`) {
		t.Errorf("generated source missing the TaskFoo constant:\n%s", out)
	}
	if !strings.Contains(out, "TaskFoo: {TierAlpha, TierBeta}") {
		t.Errorf("generated source missing the foo ladder literal:\n%s", out)
	}
	if !strings.Contains(out, "TaskFoo: ExecutionModeBackground") {
		t.Errorf("generated source does not emit TaskFoo as background:\n%s", out)
	}
	if !strings.Contains(out, "TaskBar: ExecutionModeInteractive") {
		t.Errorf("generated source does not emit TaskBar as interactive:\n%s", out)
	}
	if !strings.Contains(out, `const TaskContractHash = "deadbeef"`) {
		t.Errorf("generated source missing TaskContractHash:\n%s", out)
	}
}

// TestParseContractRejectsUnknownLadderTier is the fail-closed property:
// a ladder naming a tier absent from the top-level tiers list is a
// contract defect, not a runtime surprise — the error must name both the
// offending task and the unknown tier so the fix is obvious.
func TestParseContractRejectsUnknownLadderTier(t *testing.T) {
	const bad = `
tiers: [alpha]

tasks:
  foo: {ladder: [alpha, gamma], execution_mode: background, on_budget_exhausted: queue, status: planned}

degrade_to:
  alpha: alpha
`
	_, err := parseContract([]byte(bad))
	if err == nil {
		t.Fatal("parseContract accepted a ladder naming an unknown tier")
	}
	if !strings.Contains(err.Error(), "foo") || !strings.Contains(err.Error(), "gamma") {
		t.Errorf("error does not name both the task and the unknown tier: %v", err)
	}
}

// TestParseContractRejectsUnknownDegradeToTier extends the same
// fail-closed rule to degrade_to: a key or value outside the tiers list
// is a contract defect.
func TestParseContractRejectsUnknownDegradeToTier(t *testing.T) {
	const bad = `
tiers: [alpha]

tasks:
  foo: {ladder: [alpha], execution_mode: background, on_budget_exhausted: queue, status: planned}

degrade_to:
  alpha: gamma
`
	_, err := parseContract([]byte(bad))
	if err == nil {
		t.Fatal("parseContract accepted a degrade_to naming an unknown tier")
	}
	if !strings.Contains(err.Error(), "gamma") {
		t.Errorf("error does not name the unknown tier: %v", err)
	}
}

func TestParseContractRejectsExecutionModeBudgetPolicyMismatch(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		policy     string
		wantDetail string
	}{
		{name: "interactive queues", mode: "interactive", policy: "queue", wantDetail: "interactive execution_mode requires"},
		{name: "background degrades", mode: "background", policy: "degrade", wantDetail: "background execution_mode requires"},
		{name: "unknown mode", mode: "scheduled", policy: "queue", wantDetail: "execution_mode must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := strings.ReplaceAll(`
tiers: [alpha]

tasks:
  foo: {ladder: [alpha], execution_mode: MODE, on_budget_exhausted: POLICY, status: planned}

degrade_to:
  alpha: alpha
`, "MODE", tt.mode)
			raw = strings.ReplaceAll(raw, "POLICY", tt.policy)
			_, err := parseContract([]byte(raw))
			if err == nil {
				t.Fatal("parseContract accepted an invalid execution-mode and budget-policy pairing")
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error %q does not explain the invalid pairing", err)
			}
		})
	}
}

// TestEmitSchemaSourcesTierEnumFromContract proves the routing schema's
// tier enum is derived, not hand-copied: the propertyNames enum lists
// exactly the contract's tiers, in contract order.
func TestEmitSchemaSourcesTierEnumFromContract(t *testing.T) {
	c, err := parseContract([]byte(minimalContract))
	if err != nil {
		t.Fatalf("parseContract: %v", err)
	}

	out, err := emitSchema(c.Tiers)
	if err != nil {
		t.Fatalf("emitSchema: %v", err)
	}
	if !strings.Contains(out, `"enum": ["alpha", "beta"]`) {
		t.Errorf("generated schema does not source the tier enum from the contract:\n%s", out)
	}
	if !strings.Contains(out, "GENERATED by tools/gen-aitasks") {
		t.Errorf("generated schema is missing the generated-file $comment header:\n%s", out)
	}
}

// The shipped contract must parse under the generator that compiles it. This
// is the pair-landing guard: a mirrored contract carrying fields the parser
// does not know fails generation rather than silently dropping policy.
func TestParseContractAcceptsTheShippedDeclaration(t *testing.T) {
	raw, err := os.ReadFile("../../api/ai-tasks.yaml")
	if err != nil {
		t.Fatalf("reading the shipped contract: %v", err)
	}
	c, err := parseContract(raw)
	if err != nil {
		t.Fatalf("the shipped contract does not parse: %v", err)
	}

	verdict, ok := c.Tasks["capture_counterparty_verdict"]
	if !ok {
		t.Fatal("capture_counterparty_verdict is missing from the contract")
	}
	if !verdict.NoPayload {
		t.Error("capture_counterparty_verdict must declare no_payload: true")
	}
	if verdict.Status != "shipped" {
		t.Errorf("status = %q, want shipped", verdict.Status)
	}

	// A task is not one prompt: rate_extract has always had two.
	if got := len(c.Tasks["rate_extract"].Sites); got != 2 {
		t.Errorf("rate_extract declares %d sites, want 2 (pricing, fx)", got)
	}
	if got := len(c.Tasks["cold_start"].Sites); got != 4 {
		t.Errorf("cold_start declares %d sites, want 4", got)
	}

	// agent_loop is a cumulative tool-fed window, not a request factory.
	loop := c.Tasks["agent_loop"].Sites
	if len(loop) != 1 || loop[0].Kind != "agent_loop" {
		t.Errorf("agent_loop sites = %+v, want one site of kind agent_loop", loop)
	}

	// A bare site name defaults to one_shot.
	if got := c.Tasks["rate_extract"].Sites[0].Kind; got != "one_shot" {
		t.Errorf("a bare site name got kind %q, want one_shot", got)
	}

	if c.Embed.CostUnit != "per_entity" {
		t.Errorf("embed.cost_unit = %q, want per_entity", c.Embed.CostUnit)
	}
}

// status governs the whole census: a shipped task owes sites, a planned task
// owes none. Both directions are refused at generation, not discovered later.
func TestValidateRefusesAStatusAndSitesMismatch(t *testing.T) {
	base := `tiers: [cheap_cloud]
degrade_to: {cheap_cloud: cheap_cloud}
tasks:
  t:
    ladder: [cheap_cloud]
    execution_mode: background
    on_budget_exhausted: queue
`
	for name, tail := range map[string]string{
		"shipped without sites": "    status: shipped\n",
		"planned with sites":    "    status: planned\n    sites: [x]\n",
		"unknown status":        "    status: someday\n    sites: [x]\n",
		"unknown kind":          "    status: shipped\n    sites: [{name: x, kind: loop}]\n",
		"duplicate site":        "    status: shipped\n    sites: [x, x]\n",
	} {
		if _, err := parseContract([]byte(base + tail)); err == nil {
			t.Errorf("%s: parsed successfully, want a refusal", name)
		}
	}
}
