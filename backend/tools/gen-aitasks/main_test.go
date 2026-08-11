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

// A company-context policy that cannot do what it says is an editing mistake,
// and generation is where every task is in view at once. A repeated scope, a
// scope list with no budget to render it under, and a budget or a condition on
// a policy that selects nothing are all refused rather than compiled into a
// table whose entry silently does nothing.
func TestValidateRefusesAnIncoherentCompanyContextPolicy(t *testing.T) {
	base := `tiers: [cheap_cloud]
degrade_to: {cheap_cloud: cheap_cloud}
tasks:
  t:
    ladder: [cheap_cloud]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
`
	for name, tc := range map[string]struct {
		tail       string
		wantDetail string
	}{
		"repeated scope": {
			tail:       "    company_context: {scopes: [identity, identity], token_budget: 300}\n",
			wantDetail: `scope "identity" is declared twice`,
		},
		"scopes without a budget": {
			tail:       "    company_context: {scopes: [identity]}\n",
			wantDetail: "positive token_budget",
		},
		"budget without scopes": {
			tail:       "    company_context: {token_budget: 300}\n",
			wantDetail: "selects no scopes",
		},
		"conditional without scopes": {
			tail:       "    company_context: {conditional: true}\n",
			wantDetail: "selects no scopes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseContract([]byte(base + tc.tail))
			if err == nil {
				t.Fatal("parsed successfully, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("error %q does not explain the defect (want %q)", err, tc.wantDetail)
			}
		})
	}
}

// mergeSafetyBase is a minimal-but-valid contract the merge-safety tests
// append a defect to. Each of them asks one question: can a second
// declaration of something reach this generator without being seen?
const mergeSafetyBase = `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: shipped
    sites: [{name: only, kind: one_shot}]
`

// TestAITasksRejectsDuplicateKey pins the property a merged contract rests
// on: a key declared twice is refused, never last-write-wins. An extension
// fragment that re-declared a core task would otherwise silently override
// its routing ladder — and whichever copy survived would be the one nobody
// reviewed.
//
// yaml.v3 already refuses duplicate mapping keys (its decoder's uniqueKeys
// default), at every depth and irrespective of KnownFields. That makes this
// test a pin on a third-party default rather than on our own code, which is
// exactly why it is worth having: the property is load-bearing here and
// currently owned by nothing in this repo.
func TestAITasksRejectsDuplicateKey(t *testing.T) {
	for name, raw := range map[string]string{
		"a task declared twice": mergeSafetyBase + `  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
`,
		"a top-level block declared twice": mergeSafetyBase + "tiers: [alpha]\n",
		"a field declared twice": `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
`,
		"a duplicate key inside a site mapping": `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: shipped
    sites:
      - name: only
        name: other
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseContract([]byte(raw))
			if err == nil {
				t.Fatal("parseContract accepted a duplicate key")
			}
			if !strings.Contains(err.Error(), "already defined") {
				t.Fatalf("error %q does not report the duplicate", err)
			}
		})
	}
}

// TestAITasksRejectsSecondDocument refuses a `---` and anything after it.
// Only the first document is decoded, but the fingerprint tasks_gen.go
// carries (TaskContractHash) is the sha256 of the whole FILE — so tasks
// after a separator would be hashed as if they governed routing while
// reaching no table at all, and every downstream drift gate compares one
// generated half against the other rather than against the file.
func TestAITasksRejectsSecondDocument(t *testing.T) {
	raw := mergeSafetyBase + `---
tiers: [beta]
tasks:
  smuggled:
    ladder: [beta]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
`
	c, err := parseContract([]byte(raw))
	if err == nil {
		t.Fatalf("parseContract accepted a second document; the smuggled task reached neither table (tasks = %v)", c.sortedTaskNames())
	}
	if !strings.Contains(err.Error(), "more than one YAML document") {
		t.Fatalf("error %q does not explain the second-document refusal", err)
	}

	// Malformed bytes after the separator are refused too, and reported as
	// what they are. Treating an unreadable tail as "no second document"
	// would let a truncated or corrupted file through on the strength of
	// its first half.
	t.Run("an unreadable tail is reported, not swallowed", func(t *testing.T) {
		_, err := parseContract([]byte(mergeSafetyBase + "---\n*undefined_anchor\n"))
		if err == nil || !strings.Contains(err.Error(), "reading past the first document") {
			t.Fatalf("err = %v, want the read failure to surface", err)
		}
	})
}

// TestAITasksRejectsUnknownField closes the hole KnownFields(true) alone
// does not: yaml.Node.Decode builds its own decoder and does NOT inherit
// the outer decoder's KnownFields setting, so every block with a custom
// UnmarshalYAML is a gap in the strictness parseContract thinks it has. A
// typo there is not a missing declaration but a DIFFERENT one — `kinds:
// agent_loop` on a site leaves Kind at the one_shot default, which is the
// opposite certification posture.
func TestAITasksRejectsUnknownField(t *testing.T) {
	cases := map[string]struct {
		raw   string
		field string
	}{
		"top level": {raw: mergeSafetyBase + "unexpected: 1\n", field: "unexpected"},
		"inside a site mapping": {raw: `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: shipped
    sites: [{name: only, kinds: agent_loop}]
`, field: "kinds"},
		"inside a company_context mapping": {raw: `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
    company_context: {scopes: [identity], token_budget: 300, conditionals: true}
`, field: "conditionals"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseContract([]byte(tc.raw))
			if err == nil {
				t.Fatalf("parseContract silently dropped the unknown field %q", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q does not name the unknown field %q", err, tc.field)
			}
		})
	}
}

// The strict read must not cost the shorthand spellings the contract
// actually uses: a bare site name, and `company_context: none`. Both reach
// their custom unmarshaller as SCALAR nodes, which never touch the mapping
// decoder — this pins that the strictness was added on the mapping arm only.
func TestStrictDecodingKeepsTheScalarShorthands(t *testing.T) {
	raw := `tiers: [alpha]
degrade_to: {alpha: alpha}
tasks:
  foo:
    ladder: [alpha]
    execution_mode: background
    on_budget_exhausted: queue
    status: shipped
    sites: [bare]
    company_context: none
`
	c, err := parseContract([]byte(raw))
	if err != nil {
		t.Fatalf("a shorthand spelling was refused: %v", err)
	}
	site := c.Tasks["foo"].Sites[0]
	if site.Name != "bare" || site.Kind != kindOneShot {
		t.Fatalf("site = %+v, want the bare name defaulted to %s", site, kindOneShot)
	}
	if cc := c.Tasks["foo"].CompanyContext; cc == nil || len(cc.Scopes) != 0 {
		t.Fatalf("company_context = %+v, want the empty policy", cc)
	}
}

// The coherent spellings must keep parsing: a scoped policy with a budget, the
// conditional variant, and the `none` scalar every task without context uses.
func TestValidateAcceptsEveryCoherentCompanyContextPolicy(t *testing.T) {
	base := `tiers: [cheap_cloud]
degrade_to: {cheap_cloud: cheap_cloud}
tasks:
  t:
    ladder: [cheap_cloud]
    execution_mode: background
    on_budget_exhausted: queue
    status: planned
`
	for name, tail := range map[string]string{
		"scoped":      "    company_context: {scopes: [identity, offer], token_budget: 300}\n",
		"conditional": "    company_context: {scopes: [identity], token_budget: 300, conditional: true}\n",
		"none":        "    company_context: none\n",
		"undeclared":  "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseContract([]byte(base + tail)); err != nil {
				t.Fatalf("a coherent policy was refused: %v", err)
			}
		})
	}
}
