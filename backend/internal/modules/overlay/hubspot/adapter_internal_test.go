// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import "testing"

// TestWatermarkPropertyPerObject pins design.md §7's per-object watermark
// property split, spike-confirmed: contacts use lastmodifieddate; every
// other object class (companies/deals/… — not yet mapped, but the
// watermark split is independent of the mapping work landing) uses
// hs_lastmodifieddate.
func TestWatermarkPropertyPerObject(t *testing.T) {
	tests := []struct {
		objectClass string
		want        string
	}{
		{"contacts", "lastmodifieddate"},
		{"companies", "hs_lastmodifieddate"},
		{"deals", "hs_lastmodifieddate"},
	}
	for _, tt := range tests {
		if got := watermarkProperty(tt.objectClass); got != tt.want {
			t.Errorf("watermarkProperty(%q) = %q, want %q", tt.objectClass, got, tt.want)
		}
	}
}

// TestEveryMappingProjectsOwnerVisibility is the OVA-MAP fitness function
// behind ownerIDField's own doc: every mirrored object class MUST map
// hubspot_owner_id → owner_id, because ProjectOwnerVisibility grants the
// owner's seats their visibility from that field — a class that omits it
// ingests rows no one can ever see. Derived from objectMappings so a
// newly-added class is covered without editing this test.
func TestEveryMappingProjectsOwnerVisibility(t *testing.T) {
	for _, m := range objectMappings {
		var found bool
		for _, f := range m.Fields {
			if f.To == "owner_id" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("object mapping %q (target %q) does not map to owner_id; every mirrored class must, or its rows are invisible (see ownerIDField's doc)", m.Source, m.Target)
		}
	}
}
