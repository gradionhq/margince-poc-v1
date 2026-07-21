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
