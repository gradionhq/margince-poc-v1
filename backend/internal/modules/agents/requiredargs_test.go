// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// requiringTool is a tool whose schema declares two non-uuid required
// arguments, one optional one, and a required uuid — enough to show what this
// chokepoint holds and what it leaves to its siblings.
func requiringTool() echoTool {
	spec := objectSpec("requires_things", principal.ScopeRead)
	spec.InputSchema = json.RawMessage(`{"type":"object","required":["q","kind","anchor_id"],"properties":{
		"q":{"type":"string"},
		"kind":{"type":"string"},
		"anchor_id":{"type":"string","format":"uuid"},
		"limit":{"type":"integer"}},
		"additionalProperties":false}`)
	return echoTool{spec: spec, out: json.RawMessage(`{"ok":true}`)}
}

func requiringRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	r.Register(requiringTool())
	return r
}

// The claim itself: a call omitting an argument its own tools/list entry says is
// required is refused AT THE REGISTRY, before any handler runs.
func TestACallMissingARequiredArgumentIsRefusedAtTheChokepoint(t *testing.T) {
	r := requiringRegistry(t)
	ctx := scopedAgentCtx(principal.ScopeRead)
	_, err := r.Invoke(ctx, "requires_things",
		json.RawMessage(`{"kind":"note","anchor_id":"0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e11"}`))
	var badArgs *BadArgsError
	if !asBadArgs(err, &badArgs) {
		t.Fatalf("err = %v, want *BadArgsError", err)
	}
	if !strings.Contains(err.Error(), "`q`") {
		t.Errorf("refusal %q does not name the missing argument", err)
	}
}

// Every missing argument in one answer. Reporting them one per round trip is
// accurate and still wasteful: an agent then spends a call per field to learn
// what one refusal could have told it.
func TestOneRefusalNamesEveryMissingRequiredArgument(t *testing.T) {
	r := requiringRegistry(t)
	_, err := r.Invoke(scopedAgentCtx(principal.ScopeRead), "requires_things",
		json.RawMessage(`{"anchor_id":"0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e11"}`))
	if err == nil {
		t.Fatal("a call missing two required arguments was admitted")
	}
	for _, want := range []string{"`kind`", "`q`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
}

// An explicit null is ABSENT. A caller spelling out `{"q": null}` has supplied
// no query, and answering "present" would hand the handler a zero value it
// would have to re-refuse in its own words — the arrangement this replaces.
func TestAnExplicitNullIsTreatedAsAMissingArgument(t *testing.T) {
	r := requiringRegistry(t)
	_, err := r.Invoke(scopedAgentCtx(principal.ScopeRead), "requires_things",
		json.RawMessage(`{"q":null,"kind":"note","anchor_id":"0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e11"}`))
	if err == nil || !strings.Contains(err.Error(), "`q`") {
		t.Fatalf("err = %v, want the null argument reported as missing", err)
	}
}

// And the other direction, so the check cannot pass by refusing everything: a
// complete call reaches its handler, and an OPTIONAL argument may be omitted.
func TestACompleteCallReachesTheHandlerWithoutItsOptionalArguments(t *testing.T) {
	r := requiringRegistry(t)
	out, err := r.Invoke(scopedAgentCtx(principal.ScopeRead), "requires_things",
		json.RawMessage(`{"q":"anything","kind":"note","anchor_id":"0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e11"}`))
	if err != nil {
		t.Fatalf("a complete call was refused: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("out = %s, want the handler's own answer", out)
	}
}

// A required UUID stays idargs.go's: both checks would otherwise refuse the same
// missing argument, and the caller would be told about it twice in two different
// sentences.
func TestARequiredUUIDIsLeftToTheIdCheck(t *testing.T) {
	if named := declaredRequired(requiringTool().spec.InputSchema); len(named) != 2 {
		t.Fatalf("declaredRequired = %v, want the two non-uuid arguments only", named)
	}
	r := requiringRegistry(t)
	_, err := r.Invoke(scopedAgentCtx(principal.ScopeRead), "requires_things",
		json.RawMessage(`{"q":"anything","kind":"note"}`))
	if err == nil {
		t.Fatal("a call missing its required uuid was admitted")
	}
	// One sentence about it, from the check that owns id-shaped arguments.
	if strings.Count(err.Error(), "anchor_id") != 1 {
		t.Errorf("refusal %q reports the same missing argument more than once", err)
	}
}

// A `required` entry naming a property the schema never declares would refuse
// every call to that tool for a field no caller could learn about — a refusal
// with no way out. It is dropped here and caught at boot by the fitness test
// over the registry, where it can name the tool instead of a caller.
func TestARequiredEntryNamingAnUndeclaredPropertyIsNotEnforced(t *testing.T) {
	named := declaredRequired(json.RawMessage(`{"type":"object","required":["ghost"],"properties":{}}`))
	if len(named) != 0 {
		t.Errorf("declaredRequired = %v, want nothing enforceable", named)
	}
}

// asBadArgs is errors.As with the one target this file cares about, named so
// the assertions above read as what they check rather than as plumbing.
func asBadArgs(err error, target **BadArgsError) bool {
	for err != nil {
		if bad, ok := err.(*BadArgsError); ok {
			*target = bad
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

var _ = mcp.ToolSpec{}

// A schema whose `required` is not a list of strings is a defect in whatever
// registered it — an extension unit, most likely — and it is named while cmd
// wiring boots rather than on a caller's first request.
func TestAnUnreadableRequiredListIsRefusedAtRegistration(t *testing.T) {
	mustPanic(t, "a required list that is not a list of strings cannot be enforced", func() {
		declaredRequired(json.RawMessage(`{"type":"object","required":"q","properties":{"q":{"type":"string"}}}`))
	})
}

// Arguments that are not an object at all carry no members to look for. The
// shape verdict belongs to the steps that own it — the argument split, then the
// handler's own decode — each of which names what it wanted; a second, vaguer
// answer to the same question is worse than none.
func TestNonObjectArgumentsAreLeftToTheStepsThatOwnTheirShape(t *testing.T) {
	r := requiringRegistry(t)
	if err := r.requireDeclaredPresence("requires_things", json.RawMessage(`"a bare string"`)); err != nil {
		t.Errorf("non-object arguments were refused here as %v, where the shape is not this check's to judge", err)
	}
}
