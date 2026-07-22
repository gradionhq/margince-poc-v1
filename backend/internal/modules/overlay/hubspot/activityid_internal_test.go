// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import "testing"

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
