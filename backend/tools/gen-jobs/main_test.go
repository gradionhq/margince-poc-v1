// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"regexp"
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

// The two pairing tests below assert on "are one declaration", the phrase only
// the pairing rule uses. The bare substring "fan_out_unit" would NOT do: the
// closed-set rule beside it ("fan_out_unit must be …") contains that substring
// too, so deleting the pairing rule would leave both tests still failing — on
// the wrong rule, and green forever after.
const fanOutPairingRule = "are one declaration"

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
`, fanOutPairingRule)
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
`, fanOutPairingRule)
}

func TestParseRejectsAnUnknownFanOutUnit(t *testing.T) {
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
    fan_out_unit: tenant
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "fan_out_unit must be")
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

// fourForms carries one kind per timeout form, plus an on-demand dispatcher, so
// the parse assertions and the emitted-source assertions below are made against
// the same document. The derived value is 26m20s deliberately: it is not a whole
// number of minutes, which is what exercises goDuration's fall-through to
// seconds — the rung a rounder fixture would never reach.
const fourForms = validQueues + `
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
  e_dispatcher:
    role: dispatcher
    go_type: EDispatcherArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: on_demand
    fans_out_to: a_workspace
    fan_out_unit: workspace
`

func mustParse(t *testing.T, src string) contract {
	t.Helper()
	c, err := parseContract([]byte(src))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	return c
}

// specBlock returns the emitted map entry for one kind, so an assertion is
// about THAT kind's rendering rather than about the file containing a string
// somewhere.
func specBlock(t *testing.T, src, kind string) string {
	t.Helper()
	start := strings.Index(src, "\t\""+kind+"\": {")
	if start < 0 {
		t.Fatalf("the emitted table has no entry for %q", kind)
	}
	end := strings.Index(src[start:], "\n\t},")
	if end < 0 {
		t.Fatalf("the emitted entry for %q is unterminated", kind)
	}
	return src[start : start+end]
}

func TestParseAcceptsTheFourTimeoutForms(t *testing.T) {
	c := mustParse(t, fourForms)
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

// TestEmitRendersEachTimeoutForm pins the two places a silently wrong number
// reaches the compiled table: timeoutLiteral's precedence ladder, and
// goDuration's unit ladder. Determinism alone does not cover either — an
// emitter that renders the same wrong duration every time is perfectly stable.
func TestEmitRendersEachTimeoutForm(t *testing.T) {
	src, err := emitSpecs(mustParse(t, fourForms), "hash")
	if err != nil {
		t.Fatalf("emitting specs: %v", err)
	}
	for _, tc := range []struct {
		kind, want, why string
	}{
		{"a_workspace", "TimeoutPolicy{Fixed: 90 * time.Second}",
			"a literal under a minute renders in seconds"},
		{"b_workspace", `TimeoutPolicy{Fixed: 1580 * time.Second, DerivedFrom: "bPassTimeout"}`,
			"a derived timeout carries BOTH the duration Govern hands River and the constant the census compares"},
		{"d_workspace", "TimeoutPolicy{None: true}",
			"a deliberate absence renders as None, which Duration turns into River's -1"},
	} {
		if block := specBlock(t, src, tc.kind); !strings.Contains(block, tc.want) {
			t.Errorf("%s rendered as:\n%s\nwant it to contain %s — %s", tc.kind, block, tc.want, tc.why)
		}
	}

	operator := specBlock(t, src, "c_workspace")
	if !strings.Contains(operator, "TimeoutPolicy{FromOperator: true}") {
		t.Errorf("c_workspace rendered as:\n%s\nwant TimeoutPolicy{FromOperator: true}", operator)
	}
	if strings.Contains(operator, "Fixed") {
		t.Errorf("c_workspace rendered as:\n%s\nan {operator: …} policy must not carry a Fixed: Duration returns the SUPPLIED value, so a leaked one would be silently unreachable", operator)
	}
}

// TestEmitRendersTheDeclarationsAConsumerReads pins the fields whose absence
// from the compiled table would be invisible: an on-demand dispatcher must not
// render as a kind with no schedule, and the kind-to-type pairing must survive
// into Go so a gate can assert it without re-parsing the contract.
func TestEmitRendersTheDeclarationsAConsumerReads(t *testing.T) {
	src, err := emitSpecs(mustParse(t, fourForms), "hash")
	if err != nil {
		t.Fatalf("emitting specs: %v", err)
	}

	dispatcher := specBlock(t, src, "e_dispatcher")
	if !strings.Contains(dispatcher, "Cadence{OnDemand: true}") {
		t.Errorf("e_dispatcher rendered as:\n%s\nwant Cadence{OnDemand: true} — dropping the field makes a dispatcher that deliberately has no clock identical to one whose schedule went missing", dispatcher)
	}
	// gofmt pads a struct literal's field values into a column, so the field
	// and its value are matched across whatever alignment it chose.
	if !regexp.MustCompile(`GoType:\s+"EDispatcherArgs"`).MatchString(dispatcher) {
		t.Errorf("e_dispatcher rendered as:\n%s\nwant its GoType — the kind-to-type pairing is what a renamed args struct silently breaks", dispatcher)
	}

	if block := specBlock(t, src, "a_workspace"); strings.Contains(block, "Cadence{") {
		t.Errorf("a_workspace rendered as:\n%s\na workspace kind is enqueued by its dispatcher and never ticked, so it must carry no cadence at all", block)
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
