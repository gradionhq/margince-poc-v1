// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// Task 6's own fitness tests (UAT.md:72 — "exactly the six seeded
// templates"): the catalog seeds EXACTLY six, each pinned key resolves
// to a REGISTERED handler (the orphan-key trap — a seeded key with no
// handler is a silent no-op: "Active", never fires, logs nothing), and
// every seeded entry's default params pass its own Validate.

import (
	"testing"
)

// pinnedSeededNames is UAT.md:72's six, name-for-name — this test fails
// the moment a seeded entry's Name drifts from the pinned copy, same as
// a key drift.
var pinnedSeededNames = map[string]string{
	noActivityReminderName: "No-activity reminder",
	renewalReminderName:    "Renewal reminder",
	stageChangeNotifyName:  "Stage-change notify",
	routeLeadName:          "Route new lead to a task",
	checkInCadenceName:     "Check-in cadence",
	postMeetingRecapName:   "Post-meeting recap draft",
}

func TestExactlySixSeededTemplatesWithPinnedNames(t *testing.T) {
	var seeded []CatalogEntry
	for _, entry := range Catalog() {
		if entry.Seeded {
			seeded = append(seeded, entry)
		}
	}
	if len(seeded) != 6 {
		t.Fatalf("Catalog() seeds %d entries, want exactly 6 (UAT.md:72)", len(seeded))
	}
	seenKeys := map[string]bool{}
	for _, entry := range seeded {
		seenKeys[entry.Key] = true
		wantName, pinned := pinnedSeededNames[entry.Key]
		if !pinned {
			t.Errorf("seeded key %q is not one of the six pinned templates", entry.Key)
			continue
		}
		if entry.Name != wantName {
			t.Errorf("seeded %q carries Name %q, want the pinned %q", entry.Key, entry.Name, wantName)
		}
	}
	for key := range pinnedSeededNames {
		if !seenKeys[key] {
			t.Errorf("pinned template %q is missing from the seeded set", key)
		}
	}
}

// registeredHandlerNames derives the SAME-package handler-name set this
// module registers (StarterWorkflows) — a fitness function over the
// real registry, never a maintained parallel list (CLAUDE.md rule #2).
// assign_lead_owner's handler lives in the people module, which this
// package cannot import (ADR-0054 §9: a module never imports a
// sibling), so it is named explicitly below rather than derived —
// compose's own leadrouting_config_test.go proves that exact name
// resolves against the REAL people.LeadRoutingWorkflow handler, closing
// the loop this package alone cannot.
func registeredHandlerNames() map[string]bool {
	names := map[string]bool{}
	for _, h := range StarterWorkflows(Executors{}) {
		names[h.Spec().Name] = true
	}
	// externalHandlerNames: registered by compose (compose/workflows.go)
	// from a sibling module, under the exact name its own Spec()
	// declares — see this function's own doc.
	names[assignLeadOwnerName] = true
	return names
}

// TestEverySeededKeyResolvesToARegisteredHandler is the orphan-key trap
// for the seeded six specifically: a seeded key with no backing handler
// would enroll into every fresh workspace as an "Active" automation that
// never fires and never logs anything — the worst kind of silent no-op,
// because nothing about it looks broken.
func TestEverySeededKeyResolvesToARegisteredHandler(t *testing.T) {
	handlers := registeredHandlerNames()
	for _, entry := range Catalog() {
		if !entry.Seeded {
			continue
		}
		if !handlers[entry.Key] {
			t.Errorf("seeded catalog key %q resolves to no registered handler — every fresh workspace would enroll a silent no-op", entry.Key)
		}
	}
}

// TestEveryCatalogKeyResolvesToARegisteredHandler widens the same trap
// to the full authorable set (incl. assign_lead_owner and
// stage_change_create_task, neither seeded but both instantiable
// through the API) — an unresolvable key here is reachable by a human
// author, not just the bootstrap seed.
func TestEveryCatalogKeyResolvesToARegisteredHandler(t *testing.T) {
	handlers := registeredHandlerNames()
	for _, entry := range Catalog() {
		if !handlers[entry.Key] {
			t.Errorf("catalog key %q resolves to no registered handler", entry.Key)
		}
	}
}

// TestSeededEntriesDefaultParamsPassTheirOwnValidate proves
// SeedStarterAutomationsTx's own seed shape (nil/empty params) survives
// every seeded entry's Validate — the exact check that function now
// runs before every INSERT, so a future seeded entry that required a
// non-empty default would fail loudly here, not as a bootstrap-time SQL
// CHECK violation.
func TestSeededEntriesDefaultParamsPassTheirOwnValidate(t *testing.T) {
	for _, entry := range Catalog() {
		if !entry.Seeded {
			continue
		}
		if err := entry.Validate(nil); err != nil {
			t.Errorf("seeded entry %q: default (nil) params fail its own Validate: %v", entry.Key, err)
		}
	}
}

// TestNonSeededCatalogEntriesStayOutOfTheSeed pins the negative space:
// assign_lead_owner and stage_change_create_task are real, instantiable
// catalog entries but must never be Seeded — a regression here would
// silently grow the bootstrap set past six.
func TestNonSeededCatalogEntriesStayOutOfTheSeed(t *testing.T) {
	for _, key := range []string{assignLeadOwnerName, stageChangeCreateTaskName} {
		entry, ok := CatalogEntryByKey(key)
		if !ok {
			t.Fatalf("catalog key %q left the closed catalog", key)
		}
		if entry.Seeded {
			t.Errorf("catalog entry %q is Seeded=true, want false (it is authorable, not one of the six)", key)
		}
	}
}
