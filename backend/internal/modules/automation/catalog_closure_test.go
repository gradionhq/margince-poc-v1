// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// The RC-11 vocabulary, pinned verbatim from the spec. The registry is the
// sole membership authority; this asserts code and spec agree in BOTH
// directions, so neither a silent addition nor a silent drop can land.
var (
	pinnedTriggers = []TriggerKind{
		"record_created_updated", "field_reaches_value", "deal_enters_leaves_stage",
		"no_activity_for_n_days", "date_field_approaching", "inbound_reply", "task_overdue",
	}
	pinnedActions = []ActionType{
		"create_task", "notify", "assign_owner", "add_to_list",
		"set_field", "draft_email", "request_approval",
	}
)

func TestTriggerCatalogMatchesTheSpecBothDirections(t *testing.T) {
	assertSetsEqual(t, "trigger", toStrings(AllTriggerKinds()), toStrings(pinnedTriggers))
}

func TestActionCatalogMatchesTheSpecBothDirections(t *testing.T) {
	assertSetsEqual(t, "action", toStrings(AllActionTypes()), toStrings(pinnedActions))
}

// Every action must resolve to a definition, or an automation could carry an
// action with no tier and no permission — the escalation the ceiling exists
// to prevent. The map key and Type must agree, or a copy-paste into the
// wrong map slot would silently label one action as another. Every action
// is exactly one of {pinned-with-object, target-scoped-with-no-object} —
// never silently neither, which is the ambiguity a blank Object hides
// (assign_owner and set_field both act on whatever entity the trigger
// fired on; a pinned object for either would gate the wrong entity type).
func TestEveryActionHasADefinition(t *testing.T) {
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			t.Errorf("action %q has no definition", a)
			continue
		}
		if def.Type != a {
			t.Errorf("action %q resolves to def.Type %q — the map key and Type must agree", a, def.Type)
		}
		if def.Tier != "green" && def.Tier != "yellow" && def.Tier != "dynamic" {
			t.Errorf("action %q has tier %q, want green|yellow|dynamic", a, def.Tier)
		}
		assertPermissionIsExactlyOneShape(t, a, def.RequiredPermission)
	}
}

func assertPermissionIsExactlyOneShape(t *testing.T, a ActionType, p Permission) {
	t.Helper()
	if p.Action == "" {
		t.Errorf("action %q declares no verb — the author ceiling cannot gate it", a)
	}
	switch p.Shape {
	case PermissionPinned:
		if p.Object == "" {
			t.Errorf("action %q claims PermissionPinned but has no Object", a)
		}
	case PermissionTargetScoped:
		if p.Object != "" {
			t.Errorf("action %q is target-scoped but pins Object %q — the object must come from the fired target, not a guess", a, p.Object)
		}
	default:
		t.Errorf("action %q has PermissionShape %q — must be exactly pinned or target-scoped, never neither", a, p.Shape)
	}
}

// pinnedTargetScopedActions names the actions whose object cannot be
// pinned: assign_owner and set_field both route to
// provider.Update{Ref: action.Target} in workflows.go's ApplyActions, and
// Target's type is read off the firing event, so neither can pin a single
// object. Pinned by name, not derived, so a future change that flips an
// action's Shape — widening or narrowing the author-time ceiling — must
// touch this list and be seen in review, never slip through as a
// one-line change to actionDefs.
var pinnedTargetScopedActions = map[ActionType]bool{
	ActionTypeAssignOwner: true,
	ActionTypeSetField:    true,
}

func TestOnlyEntityAgnosticActionsAreTargetScoped(t *testing.T) {
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			continue // reported by TestEveryActionHasADefinition
		}
		want := pinnedTargetScopedActions[a]
		got := def.RequiredPermission.Shape == PermissionTargetScoped
		if got != want {
			t.Errorf("action %q is target-scoped=%v, want %v — a Shape change here is a security-relevant decision, not a drive-by edit", a, got, want)
		}
	}
}

// Every ActionDef's Executor must name a real workflow.ActionKind, or a
// typo or a kind dropped from the ports layer would silently strand an
// action with an executor nothing can run.
func TestEveryActionExecutorIsAKnownWorkflowKind(t *testing.T) {
	known := map[workflow.ActionKind]bool{}
	for _, k := range workflow.AllActionKinds() {
		known[k] = true
	}
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			continue // reported by TestEveryActionHasADefinition
		}
		if !known[def.Executor] {
			t.Errorf("action %q executor %q is not a member of workflow.AllActionKinds", a, def.Executor)
		}
	}
}

// TriggerDefFor must resolve for every closed TriggerKind, or a trigger
// kind with no definition would silently strand any automation that
// picks it. Entry is closed to event|clock, and a clock trigger consumes
// no event by design (AUTO-EV-7), so EventType must be empty whenever
// Entry is "clock".
func TestEveryTriggerHasADefinition(t *testing.T) {
	for _, k := range AllTriggerKinds() {
		def, ok := TriggerDefFor(k)
		if !ok {
			t.Errorf("trigger %q has no definition — TriggerDefFor must resolve every AllTriggerKinds member", k)
			continue
		}
		if def.Entry != "event" && def.Entry != "clock" {
			t.Errorf("trigger %q has Entry %q, want event|clock", k, def.Entry)
		}
		if def.Entry == "clock" && def.EventType != "" {
			t.Errorf("trigger %q is a clock trigger but declares EventType %q — clock triggers consume no event", k, def.EventType)
		}
	}
}

// TestRequiredPermissionForKindReverseMapIsUnambiguous is the soundness
// proof gate.go's match-time gate depends on: the gate sees a planned
// workflow.Action, which names only the executor (ActionKind), so it
// reverse-looks-up the permission through RequiredPermissionForKind. That
// lookup is sound only if no two catalog ActionTypes share an executor
// while disagreeing on the required permission — this asserts exactly
// that, over the full registry, rather than trusting it by inspection.
func TestRequiredPermissionForKindReverseMapIsUnambiguous(t *testing.T) {
	seen := map[workflow.ActionKind]Permission{}
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			continue // reported by TestEveryActionHasADefinition
		}
		if existing, already := seen[def.Executor]; already {
			if existing != def.RequiredPermission {
				t.Errorf("executor %q maps to two different permissions across action types (%+v vs %+v) — "+
					"RequiredPermissionForKind's reverse lookup would be ambiguous", def.Executor, existing, def.RequiredPermission)
			}
			continue
		}
		seen[def.Executor] = def.RequiredPermission
	}
}

// TestRequiredPermissionForKindResolvesEveryRegisteredExecutor proves the
// reverse lookup actually returns the SAME permission the forward
// registry (actionDefs) carries for every executor a catalog action uses
// today — not just that it doesn't collide.
func TestRequiredPermissionForKindResolvesEveryRegisteredExecutor(t *testing.T) {
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			continue
		}
		perm, ok := RequiredPermissionForKind(def.Executor)
		if !ok {
			t.Errorf("RequiredPermissionForKind(%q) ok=false, want the permission action %q registers", def.Executor, a)
			continue
		}
		if perm != def.RequiredPermission {
			t.Errorf("RequiredPermissionForKind(%q) = %+v, want %+v", def.Executor, perm, def.RequiredPermission)
		}
	}
}

// TestRequiredPermissionForKindFailsForAnUnregisteredExecutor proves the
// reverse lookup fails honestly (ok=false) for an executor kind no
// catalog action names, rather than resolving to a fabricated
// zero-value permission the gate could mistake for a real pinned object.
func TestRequiredPermissionForKindFailsForAnUnregisteredExecutor(t *testing.T) {
	if _, ok := RequiredPermissionForKind(workflow.ActionEnqueueJob); ok {
		t.Error("RequiredPermissionForKind(ActionEnqueueJob) ok=true, want false — no catalog action names this executor")
	}
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func assertSetsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	inWant := map[string]bool{}
	for _, w := range want {
		inWant[w] = true
	}
	inGot := map[string]bool{}
	for _, g := range got {
		inGot[g] = true
	}
	for _, g := range got {
		if !inWant[g] {
			t.Errorf("registry has %s %q that the spec does not list", label, g)
		}
	}
	for _, w := range want {
		if !inGot[w] {
			t.Errorf("spec lists %s %q that the registry does not have", label, w)
		}
	}
}
