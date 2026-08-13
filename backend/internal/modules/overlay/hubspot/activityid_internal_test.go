// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// TestNamespacedClassesAreExactlyTheModulesEngagementList binds the list that
// MINTS a namespaced mirror id to the list that READS one back. engagementClasses
// (this file) decides at ingest whether a record's id becomes "<class>:<id>";
// overlay.IncumbentEngagementClasses is what the re-projection sweep builds its
// "<class>:" row filter from, and what the identity bridge reverses. The two are
// separate lists on either side of the module seam — overlay cannot import
// hubspot — so nothing but this holds them together.
//
// A class the module names but this file omits mints BARE ids while the sweep
// filters on the prefix: it selects zero rows, the class never re-projects, and
// the flip stays blocked with every other gate green. The mirror image mints
// namespaced ids no caller can attribute.
func TestNamespacedClassesAreExactlyTheModulesEngagementList(t *testing.T) {
	named := overlay.IncumbentEngagementClasses()
	for _, class := range named {
		if !engagementClasses[class] {
			t.Errorf("overlay names %q an engagement class but engagementClasses (hubspot/activityid.go) omits it, "+
				"so its rows get bare mirror ids while the sweep filters on %q and selects none of them: "+
				"add %q to engagementClasses — the list that mints the id and the list that reads it back must agree",
				class, class+":", class)
		}
	}
	for class := range engagementClasses {
		if !slices.Contains(named, class) {
			t.Errorf("engagementClasses (hubspot/activityid.go) namespaces %q's mirror ids but overlay does not name it an "+
				"engagement class, so no caller can attribute a row of it: add %q to incumbentEngagementClasses "+
				"(overlay/incumbent.go) — the list that mints the id and the list that reads it back must agree",
				class, class)
		}
	}
}

// TestMirrorActivityExternalIDNamespacesEngagementsOnly proves OVA-MAP-7's
// mirror-side rule: the five engagement classes get their incumbent id
// namespaced by source class, while every other class keeps its bare id, and
// the round-trip back to the raw id (for a HubSpot API call) is exact.
func TestMirrorActivityExternalIDNamespacesEngagementsOnly(t *testing.T) {
	for _, class := range []string{objectClassCalls, objectClassMeetings, objectClassEmails, objectClassNotes, objectClassTasks} {
		mirrorID := mirrorActivityExternalID(class, "123")
		if want := class + ":123"; mirrorID != want {
			t.Errorf("mirrorActivityExternalID(%q, 123) = %q, want %q", class, mirrorID, want)
		}
		if raw := incumbentActivityID(class, mirrorID); raw != "123" {
			t.Errorf("incumbentActivityID(%q, %q) = %q, want 123 (round-trip to the raw API id)", class, mirrorID, raw)
		}
	}
	for _, class := range []string{objectClassContacts, objectClassCompanies, objectClassDeals, objectClassLeads} {
		if mirrorID := mirrorActivityExternalID(class, "123"); mirrorID != "123" {
			t.Errorf("mirrorActivityExternalID(%q, 123) = %q, want the bare 123 (non-engagement class)", class, mirrorID)
		}
		if raw := incumbentActivityID(class, "123"); raw != "123" {
			t.Errorf("incumbentActivityID(%q, 123) = %q, want 123 unchanged", class, raw)
		}
	}
}
