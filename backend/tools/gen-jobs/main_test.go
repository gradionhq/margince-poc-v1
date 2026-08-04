// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// Each fixture below is COMPLETE but for the one rule under test, so a failure
// can only mean that rule fired. A fixture missing several required fields
// would fail whichever check happened to run first, and would keep passing
// after the rule it names was deleted.
const validQueues = `
queues:
  default: {max_workers: 5}
`

func mustFail(t *testing.T, src, want string) {
	t.Helper()
	_, err := parseContract([]byte(src))
	if err == nil {
		t.Fatalf("parseContract accepted the contract; want a failure mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("parseContract failed with %q; want a message mentioning %q", err, want)
	}
}

func TestParseRejectsAWorkspaceKindWithNoTimeout(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    opts_owner: fan_out
    max_attempts: 3
`, "declares no timeout")
}

func TestParseRejectsAQueueNoEntryDeclares(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: nonexistent
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: foo_workspace
    fan_out_unit: workspace
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "nonexistent")
}

func TestParseRejectsAFanOutEdgeToAnUndeclaredKind(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: nobody
    fan_out_unit: workspace
`, "nobody")
}

func TestParseRejectsADispatcherWithNoCadence(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    fans_out_to: foo_workspace
    fan_out_unit: workspace
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "declares no cadence")
}

func TestParseRejectsMaxAttemptsOnAKindTheFileDoesNotGovern(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    max_attempts: 3
`, "max_attempts")
}

func TestParseRejectsAFanOutUnitWithNoEdge(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fan_out_unit: workspace
`, "fan_out_unit")
}

func TestParseRejectsAnEdgeWithNoFanOutUnit(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: foo_workspace
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "fan_out_unit")
}

func TestParseRejectsADuplicateKindString(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
  foo_workspace:
    role: workspace
    go_type: OtherWorkspaceArgs
    queue: default
    timeout: 5m
    opts_owner: caller
`, "declared twice")
}

func TestParseRejectsTwoKindsSharingOneGoType(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
  bar_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "FooWorkspaceArgs")
}

func TestParseRejectsADerivedTimeoutWithNoValue(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: {derived: fooPassTimeout}
    opts_owner: caller
`, "value")
}

func TestParseRejectsADispatcherThatFansOutToNothing(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
`, "fans out to nothing")
}

func TestParseAcceptsTheFourTimeoutForms(t *testing.T) {
	c, err := parseContract([]byte(validQueues + `
kinds:
  a_workspace:
    role: workspace
    go_type: AWorkspaceArgs
    queue: default
    timeout: 90s
    opts_owner: caller
  b_workspace:
    role: workspace
    go_type: BWorkspaceArgs
    queue: default
    timeout: {derived: bPassTimeout, value: 26m20s}
    opts_owner: caller
  c_workspace:
    role: workspace
    go_type: CWorkspaceArgs
    queue: default
    timeout: {operator: SomeCaps}
    opts_owner: caller
  d_workspace:
    role: workspace
    go_type: DWorkspaceArgs
    queue: default
    timeout: {none: true, reason: bounded by a backlog, not a wall clock}
    opts_owner: caller
`))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got := c.Kinds["a_workspace"].Timeout.Fixed; got != 90_000_000_000 {
		t.Errorf("a_workspace fixed timeout = %v, want 90s", got)
	}
	if got := c.Kinds["b_workspace"].Timeout.Derived; got != "bPassTimeout" {
		t.Errorf("b_workspace derived-from = %q, want bPassTimeout", got)
	}
	if got := c.Kinds["b_workspace"].Timeout.Fixed; got != 1_580_000_000_000 {
		t.Errorf("b_workspace resolved value = %v, want 26m20s — Govern hands River a duration, not a constant name", got)
	}
	if got := c.Kinds["c_workspace"].Timeout.Operator; got != "SomeCaps" {
		t.Errorf("c_workspace operator field = %q, want SomeCaps", got)
	}
	if !c.Kinds["d_workspace"].Timeout.None {
		t.Error("d_workspace must parse as a deliberate absence")
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	src := []byte(validQueues + `
kinds:
  b_kind:
    role: dispatcher
    go_type: BKindArgs
    queue: default
    timeout: 1m
    opts_owner: caller
    cadence: 1h
    fans_out_to: a_kind
    fan_out_unit: workspace
  a_kind:
    role: workspace
    go_type: AKindArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`)
	c, err := parseContract(src)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	firstSpecs, err := emitSpecs(c, "hash")
	if err != nil {
		t.Fatalf("emitting specs: %v", err)
	}
	firstKinds, err := emitKinds(c, "hash")
	if err != nil {
		t.Fatalf("emitting kinds: %v", err)
	}
	for range 20 {
		againSpecs, err := emitSpecs(c, "hash")
		if err != nil {
			t.Fatalf("emitting specs: %v", err)
		}
		if againSpecs != firstSpecs {
			t.Fatal("emitSpecs is not deterministic; map iteration order will drift the gate in CI while passing locally")
		}
		againKinds, err := emitKinds(c, "hash")
		if err != nil {
			t.Fatalf("emitting kinds: %v", err)
		}
		if againKinds != firstKinds {
			t.Fatal("emitKinds is not deterministic; map iteration order will drift the gate in CI while passing locally")
		}
	}
}
