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

// The mirror, and the one that actually costs something: a fan-out child is
// the ONLY kind whose attempt cap the running system reads from this file, so
// omitting it does not fall back to a house default — it falls back to
// River's 25-rung ladder, which replaces the dispatcher's tick as the retry
// cadence without anything going red.
func TestParseRejectsAFanOutChildWithNoMaxAttempts(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: fan_out
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

// A child row carries no unit of its own: every consumer walks the fan-out edge
// backwards to learn what one stands for. Two dispatchers naming one child with
// DIFFERENT units make that answer depend on which edge the reader walked last,
// which is iteration order — so the sweep-unit gauges would report a grain
// nobody chose. Refused here, where a contract can still be edited.
func TestParseRejectsTwoDispatchersFanningOutToOneChildWithDifferentUnits(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: shared_child
    fan_out_unit: workspace
  bar:
    role: dispatcher
    go_type: BarArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: shared_child
    fan_out_unit: connection
  shared_child:
    role: workspace
    go_type: SharedChildArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "a child row stands for ONE unit")
}

// The same child claimed twice with the SAME unit is unambiguous, so it is not
// this rule's business — the rule is about disagreement, not about sharing.
func TestParseAcceptsTwoDispatchersFanningOutToOneChildWithOneUnit(t *testing.T) {
	mustParse(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: shared_child
    fan_out_unit: workspace
  bar:
    role: dispatcher
    go_type: BarArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
    fans_out_to: shared_child
    fan_out_unit: workspace
  shared_child:
    role: workspace
    go_type: SharedChildArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`)
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

// A typo inside a block with its own UnmarshalYAML is the case the outer
// decoder's KnownFields cannot see: yaml.Node.Decode makes its own decoder, so
// an unknown key there is dropped in silence and the field keeps its zero. That
// zero is not an absence but the OPPOSITE declaration — registers_nothing where
// registers_anyway was meant, an id where a scalar was argued for — so each
// block that takes the mapping form is pinned here.
//
// Each fixture is otherwise complete and carries the misspelling once, so a
// failure can only be the strict read refusing it.
func TestParseRejectsAnUnknownKeyInsideACustomDecodedBlock(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "registration",
			src: `
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    registration: {when: [Brain], absnet: registers_anyway}
`,
			want: "absnet",
		},
		{
			name: "timeout",
			src: `
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: {none: true, resaon: bounded by a backlog, not a wall clock}
    opts_owner: caller
`,
			want: "resaon",
		},
		{
			name: "args",
			src: `
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      Workspace: {scalar: true, resaon: not an id}
`,
			want: "resaon",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustFail(t, validQueues+tc.src, tc.want)
		})
	}
}

// A cadence mapping is the same hole, on a block whose valid keys are a
// different pair; it takes its own fixture because only a DISPATCHER may carry
// one.
func TestParseRejectsAnUnknownKeyInACadenceMapping(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: dispatcher
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: {operator: Interval, schedule_when_postive: Interval}
    fans_out_to: foo_workspace
    fan_out_unit: workspace
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "schedule_when_postive")
}

// Only the first document is decoded and only the first is walked for
// duplicates, but the fingerprint both generated tables carry is the sha256 of
// the whole FILE. Kinds after a separator would therefore be hashed as if they
// governed something while compiling into neither table.
func TestParseRejectsASecondDocument(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo_workspace:
    role: workspace
    go_type: FooWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
---
kinds:
  bar_workspace:
    role: workspace
    go_type: BarWorkspaceArgs
    queue: default
    timeout: 2m
    opts_owner: caller
`, "more than one YAML document")
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
    timeout: {none: true, reason: "bounded by a backlog, not a wall clock"}
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

	// The field path, not merely the posture: it is what a gate joins the
	// registration's computed expression back to, and a policy that rendered
	// only "this comes from an operator" would leave WHICH dial uncheckable.
	operator := specBlock(t, src, "c_workspace")
	if !strings.Contains(operator, `TimeoutPolicy{OperatorField: "SomeCaps"}`) {
		t.Errorf("c_workspace rendered as:\n%s\nwant TimeoutPolicy{OperatorField: \"SomeCaps\"}", operator)
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

// A fault block exists only to WAIVE returning a failure, so the rationale is
// the whole content of it: without one there is nothing to tell an unratified
// swallowed error from a ratified one.
func TestParseRejectsAFaultWaiverWithNoRationale(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    fault: {}
`, "no nil_after_logging rationale")
}

// A scalar args field is a value the erasure engine cannot reach through the
// row the job names, so it is admitted only with the argument for it.
func TestParseRejectsAScalarArgsFieldWithNoReason(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      Snippet: {scalar: true}
`, "declared a scalar with no reason")
}

// An empty mapping says nothing the `id` shorthand does not, and is the shape
// left behind when somebody deletes a rationale or forgets scalar: true.
func TestParseRejectsAnEmptyArgsFieldMapping(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      Workspace: {}
`, "says nothing")
}

// An ID may carry a rationale, and must be able to: a field whose NAME reads
// like content has something to argue even when it really is a reference, and
// jobargscontent_test.go refuses such a field without one. A rule that allowed
// the mapping form only for a scalar would leave that argument nowhere to go.
func TestParseAcceptsAReasonOnAnIdField(t *testing.T) {
	c, err := parseContract([]byte(validQueues + `
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      RecipientEmail:
        reason: the comms_outbound row id, whose name is historical
`))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	got := c.Kinds["foo"].Args["RecipientEmail"]
	if got.Scalar {
		t.Errorf("RecipientEmail parsed as %+v, want an id — a reason must not promote a field to a scalar", got)
	}
	if got.Reason == "" {
		t.Error("RecipientEmail parsed with no reason, so the argument for a content-looking name would be dropped at generation")
	}
}

// An args field names an EXPORTED Go field, because River marshals args to
// JSON and an unexported one never reaches the wire — so a lowercase entry
// declares a field no job can be carrying.
func TestParseRejectsAnUnexportedArgsFieldName(t *testing.T) {
	mustFail(t, validQueues+`
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      workspace: id
`, `args field "workspace"`)
}

// The shorthand is the ordinary case and has to survive: a rule that refused
// `id` would push every field into the mapping form and bury the four that
// genuinely need one.
func TestParseAcceptsTheIdShorthandAndAnArguedScalar(t *testing.T) {
	c, err := parseContract([]byte(validQueues + `
kinds:
  foo:
    role: workspace
    go_type: FooArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    args:
      Workspace: id
      Provider:
        scalar: true
        reason: it names a code path, not a row
`))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	args := c.Kinds["foo"].Args
	if got := args["Workspace"]; got.Scalar || got.Reason != "" {
		t.Errorf("Workspace parsed as %+v, want a bare id", got)
	}
	if got := args["Provider"]; !got.Scalar || got.Reason == "" {
		t.Errorf("Provider parsed as %+v, want an argued-for scalar", got)
	}
}
