// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import "testing"

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
// to prevent.
func TestEveryActionHasADefinition(t *testing.T) {
	for _, a := range AllActionTypes() {
		def, ok := ActionDefFor(a)
		if !ok {
			t.Errorf("action %q has no definition", a)
			continue
		}
		if def.Tier != "green" && def.Tier != "yellow" && def.Tier != "dynamic" {
			t.Errorf("action %q has tier %q, want green|yellow|dynamic", a, def.Tier)
		}
		if def.RequiredPermission.Object == "" || def.RequiredPermission.Action == "" {
			t.Errorf("action %q declares no RequiredPermission — the author ceiling cannot gate it", a)
		}
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
