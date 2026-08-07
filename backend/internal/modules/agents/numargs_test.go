// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// A bound a tool advertises is a bound the surface keeps.
//
// The claim under test is the one a client actually reads: `minimum` and
// `maximum` in a tools/list entry are what a caller sizes its call by, and a
// server that serves 999999 against `maximum: 50` has taught that client to
// re-discover every other declared claim by experiment.
//
// The subject set is derived from the registered surface and from each tool's OWN
// schema — every registered tool, every property carrying a bound. A walk that
// derives its field names but hand-lists its subjects certifies every subject it
// never looked at, and a new bounded argument must inherit this the moment it is
// registered.
//
// Probed through Registry.Invoke, which is where the enforcement lives and the
// only path to a Handle in this package: a walk over handlers would pass again
// the moment a new handler forgets its own schema, which is the whole failure
// being closed.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// declaredBound is one advertised range as the SCHEMA states it, read by the
// walk itself rather than taken from the registry's own reading — a gate that
// asks the code under test what it should enforce proves only that it is
// self-consistent.
type declaredBound struct {
	field string
	min   *float64
	max   *float64
}

// numProps returns the bounds a tool's schema advertises on its top-level
// integer/number arguments.
func numProps(t *testing.T, tool string, inputSchema json.RawMessage) []declaredBound {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type    string   `json:"type"`
			Minimum *float64 `json:"minimum"`
			Maximum *float64 `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		t.Fatalf("%s: inputSchema does not parse: %v", tool, err)
	}
	var bounds []declaredBound
	for field, def := range schema.Properties {
		if def.Type != "integer" && def.Type != "number" {
			continue
		}
		if def.Minimum == nil && def.Maximum == nil {
			continue
		}
		bounds = append(bounds, declaredBound{field: field, min: def.Minimum, max: def.Maximum})
	}
	return bounds
}

// boundProbeArgs renders an arguments object satisfying every required argument,
// with field set to value — so the refusal under test is about that argument's
// range and not about some other argument being absent.
func boundProbeArgs(t *testing.T, tool string, inputSchema json.RawMessage, field string, value float64) json.RawMessage {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(absentIDArgs(t, tool, inputSchema, ""), &args); err != nil {
		t.Fatalf("%s: probe arguments do not parse: %v", tool, err)
	}
	args[field] = value
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("%s: marshal probe arguments: %v", tool, err)
	}
	return encoded
}

// numText renders a number the way a refusal names it, derived here rather than
// borrowed from the production formatter so the walk asserts on the prose a
// caller reads instead of on our own rendering of it.
func numText(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func TestEveryDeclaredNumericBoundIsEnforcedAndItsEdgeIsLegal(t *testing.T) {
	registry := idProbeDispatcher(t).registry
	// Every scope the surface defines, so admission never stands between the walk
	// and the validation it is here to check: the gate refuses on authority before
	// arguments are looked at, and that refusal would read as a pass.
	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeDraft,
		principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich)

	probed := 0
	for name, tool := range registry.tools {
		for _, bound := range numProps(t, name, tool.Spec().InputSchema) {
			for _, probe := range boundProbes(bound) {
				probed++
				args := boundProbeArgs(t, name, tool.Spec().InputSchema, bound.field, probe.value)
				_, err := registry.Invoke(ctx, name, args)
				var badArgs *BadArgsError
				refused := errors.As(err, &badArgs)
				if probe.refusal == "" {
					// The tool may still refuse this call for its own reasons (the probe
					// fills its other arguments with placeholders); what it may not do is
					// refuse a value its own schema advertises as in range.
					if refused && strings.Contains(badArgs.Error(), "declared") {
						t.Errorf("%s refused %q=%s, which is %s of its own declared range: %q",
							name, bound.field, numText(probe.value), probe.what, badArgs.Error())
					}
					continue
				}
				if err == nil {
					t.Errorf("%s accepted %q=%s, %s of the range its schema advertises.\n"+
						"A bound nothing enforces is a promise the client sized its call by.",
						name, bound.field, numText(probe.value), probe.what)
					continue
				}
				if !refused {
					t.Errorf("%s answered %q=%s with %T (%v), want *BadArgsError — the caller chose that "+
						"number and is the only party that can fix it", name, bound.field, numText(probe.value), err, err)
					continue
				}
				if !strings.Contains(badArgs.Error(), bound.field) || !strings.Contains(badArgs.Error(), probe.refusal) {
					t.Errorf("%s refused %q=%s with %q, which does not say %q — a refusal that names neither "+
						"the argument nor its bound cannot be acted on", name, bound.field, numText(probe.value),
						badArgs.Error(), probe.refusal)
				}
			}
		}
	}
	// The walk is only as good as its reach: a surface that stopped declaring
	// bounds would pass every assertion above by probing nothing.
	if probed == 0 {
		t.Fatal("no integer/number argument declares a bound on any tool — the walk proved nothing")
	}
}

// boundProbe is one value put to a tool, with the refusal its schema owes for it
// — empty when the value is one the schema invites.
type boundProbe struct {
	what    string
	value   float64
	refusal string
}

// boundProbes derives what to try for one declared range: a step outside each
// end the schema states, and the end itself. The edges matter as much as the
// misses — without them an inverted comparison refuses everything, including
// what the schema invites, and still passes every out-of-range assertion.
func boundProbes(bound declaredBound) []boundProbe {
	var probes []boundProbe
	if bound.min != nil {
		floor := *bound.min
		probes = append(
			probes,
			boundProbe{what: "below the floor", value: floor - 1, refusal: "below its declared minimum of " + numText(floor)},
			boundProbe{what: "exactly the floor", value: floor},
		)
	}
	if bound.max != nil {
		ceiling := *bound.max
		probes = append(
			probes,
			boundProbe{what: "above the ceiling", value: ceiling + 1, refusal: "above its declared maximum of " + numText(ceiling)},
			boundProbe{what: "exactly the ceiling", value: ceiling},
		)
	}
	return probes
}

// boundProbeProvider is the composite provider seam under a call whose numbers
// its schema forbids: reaching it at all is the defect.
type boundProbeProvider struct {
	seamProbeProvider
	t *testing.T
}

func (p boundProbeProvider) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	p.t.Error("search_records reached the provider with a limit its schema forbids")
	return datasource.SearchResult{}, nil
}

func TestTheAdvertisedLimitsBindTheToolsThatAdvertiseThem(t *testing.T) {
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterCoreTools(registry, boundProbeProvider{t: t}, nil, nil, nil)
	RegisterSlippingTools(registry,
		func(context.Context) ([]SlippingDeal, error) {
			t.Error("the deal lister ran for a limit the schema forbids")
			return nil, nil
		},
		func(context.Context, SlippingDeal) (ids.UUID, string, error) {
			t.Error("a draft was written for a limit the schema forbids")
			return ids.Nil, "", nil
		})
	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeDraft)

	for _, tc := range []struct {
		name, tool, args, want string
	}{
		{"a zero cap asks for nothing", "whats_slipping_this_week", `{"limit":0}`, "`limit` is 0, below its declared minimum of 1"},
		{"a negative cap", "whats_slipping_this_week", `{"limit":-5}`, "`limit` is -5, below its declared minimum of 1"},
		{"a cap far above the ceiling", "whats_slipping_this_week", `{"limit":999999}`, "`limit` is 999999, above its declared maximum of 50"},
		{"one over the ceiling", "search_records", `{"limit":51}`, "`limit` is 51, above its declared maximum of 50"},
		{"the bulk writer's own ceiling", "draft_follow_ups_for", `{"segment":"slipping","limit":26}`, "`limit` is 26, above its declared maximum of 25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.Invoke(ctx, tc.tool, json.RawMessage(tc.args))
			var badArgs *BadArgsError
			if !errors.As(err, &badArgs) {
				t.Fatalf("%s%s → %v, want *BadArgsError", tc.tool, tc.args, err)
			}
			if !strings.Contains(badArgs.Error(), tc.want) {
				t.Errorf("%s%s refused with %q, want it to say %q", tc.tool, tc.args, badArgs.Error(), tc.want)
			}
		})
	}
}

func TestACapInsideItsDeclaredRangeReachesTheTool(t *testing.T) {
	reads := 0
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterSlippingTools(registry, func(context.Context) ([]SlippingDeal, error) {
		reads++
		return nil, nil
	}, nil)
	ctx := scopedAgentCtx(principal.ScopeRead)

	// An omitted optional argument is a complete call, an explicit null carries no
	// number to place inside or outside a range, and both edges are values the
	// schema invites.
	for _, args := range []string{`{}`, `{"limit":null}`, `{"limit":1}`, `{"limit":50}`} {
		if _, err := registry.Invoke(ctx, "whats_slipping_this_week", json.RawMessage(args)); err != nil {
			t.Errorf("Invoke with %s = %v, want the tool to run", args, err)
		}
	}
	if reads != 4 {
		t.Errorf("the tool ran %d times for 4 legal calls — a bound check that refuses what the schema "+
			"invites is worse than none", reads)
	}
}

func TestABoundCheckNeverAnswersAQuestionAboutShape(t *testing.T) {
	// Arguments that are not an object carry no member to bound, and the verdict on
	// the shape belongs to the steps that own it — the argument split, then the
	// handler's own decode, each of which names what it wanted. A second, vaguer
	// answer to the same question is worse than none.
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterSlippingTools(registry, func(context.Context) ([]SlippingDeal, error) {
		t.Error("the deal lister ran for arguments that are not an object")
		return nil, nil
	}, nil)

	if err := registry.requireDeclaredBounds("whats_slipping_this_week", json.RawMessage(`[0]`)); err != nil {
		t.Errorf("the bound check refused a shape with %v, which is not its verdict to give", err)
	}
	_, err := registry.Invoke(scopedAgentCtx(principal.ScopeRead), "whats_slipping_this_week", json.RawMessage(`[0]`))
	var badArgs *BadArgsError
	if !errors.As(err, &badArgs) {
		t.Fatalf("a non-object argument set → %v, want *BadArgsError", err)
	}
	if strings.Contains(badArgs.Error(), "declared") {
		t.Errorf("a caller sending the wrong shape was told about a bound instead: %q", badArgs.Error())
	}
}

func TestOneRefusalNamesEveryArgumentOutsideItsRange(t *testing.T) {
	// An agent that learns about one out-of-range argument per round trip spends a
	// call per field on what one refusal could have told it. No shipped tool
	// declares two bounds today, so the collection is proved on a registered tool
	// that does — the same door every tool comes through.
	tool := &fakeTool{spec: mcp.ToolSpec{
		Name: "two_bounds", Title: "Two bounds", Description: describedForRegistration, RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"count":{"type":"integer","minimum":1,"maximum":9},
			"weight":{"type":"number","minimum":0.5}},"additionalProperties":false}`),
	}}
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	registry.Register(tool)

	_, err := registry.Invoke(scopedAgentCtx(principal.ScopeRead), "two_bounds",
		json.RawMessage(`{"count":42,"weight":0.1}`))
	var badArgs *BadArgsError
	if !errors.As(err, &badArgs) {
		t.Fatalf("two out-of-range arguments → %v, want *BadArgsError", err)
	}
	for _, want := range []string{
		"`count` is 42, above its declared maximum of 9",
		"`weight` is 0.1, below its declared minimum of 0.5",
	} {
		if !strings.Contains(badArgs.Error(), want) {
			t.Errorf("the refusal %q does not say %q", badArgs.Error(), want)
		}
	}
	if tool.handled {
		t.Error("the tool ran on arguments its own schema forbids")
	}
}

func TestDeclaredNumBoundsCarriesEveryBoundedNumberAndNothingElse(t *testing.T) {
	bounds := declaredNumBounds(json.RawMessage(`{"type":"object","properties":{
		"capped":{"type":"integer","minimum":1,"maximum":50},
		"floored":{"type":"number","minimum":0.5},
		"ceilinged":{"type":"integer","maximum":7},
		"free":{"type":"integer"},
		"named":{"type":"string","minimum":3}}}`))

	// Sorted, so a call breaking two bounds is refused in the same words every run.
	got := make([]string, 0, len(bounds))
	for _, b := range bounds {
		got = append(got, b.name)
	}
	if want := "capped,ceilinged,floored"; strings.Join(got, ",") != want {
		// A bound on a string property is not a range this check can hold, and an
		// unbounded number has nothing to hold — carrying either would refuse or
		// admit on a claim the schema never made.
		t.Fatalf("bounded properties = %v, want [%s] in that order", got, want)
	}
	// And each end is read as declared, including the ones a schema leaves open: an
	// invented floor would refuse a value the caller was invited to send.
	want := map[string]string{"capped": "1..50", "ceilinged": "open..7", "floored": "0.5..open"}
	for _, b := range bounds {
		if read := boundText(b); read != want[b.name] {
			t.Errorf("%s reads as %s, want %s", b.name, read, want[b.name])
		}
	}
}

// boundText renders a read range so one comparison covers both ends and the ones
// left open.
func boundText(b numBound) string {
	end := func(v *float64) string {
		if v == nil {
			return "open"
		}
		return numText(*v)
	}
	return end(b.min) + ".." + end(b.max)
}

func TestRegisterRefusesASchemaWhoseBoundIsNotANumber(t *testing.T) {
	// A bound nobody can read is a schema defect in whatever registered it, and it
	// surfaces where it can still be fixed — while cmd wiring boots, not on a
	// caller's first request, where the tool would silently be unbounded.
	r := NewRegistry(nil, nil)
	mustPanic(t, "a bound that is not a number leaves the argument unenforceable", func() {
		r.Register(&fakeTool{spec: mcp.ToolSpec{
			Name: "unreadable_bound", Title: "Unreadable bound", Description: describedForRegistration, Tier: mcp.TierAutoExecute,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":"one"}}}`),
		}})
	})
}
