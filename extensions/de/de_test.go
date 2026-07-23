// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package de

import (
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// TestNewDeclaresTheGoBDFloors pins the statutory content: the §147
// AO/HGB retention floors as CALENDAR years. A changed span or class
// name here is a legal-content change and must be deliberate.
func TestNewDeclaresTheGoBDFloors(t *testing.T) {
	e := New()
	if e.Name != "de" {
		t.Fatalf("Name = %q, want the unit's directory name de", e.Name)
	}
	if len(e.Jurisdictions) != 1 {
		t.Fatalf("declaration carries %d jurisdiction packs, want 1", len(e.Jurisdictions))
	}
	p := e.Jurisdictions[0]
	if got := p.Code(); got != "de" {
		t.Fatalf("pack code = %q, want de", got)
	}
	want := map[jurisdiction.RetentionClassName]jurisdiction.Period{
		jurisdiction.CommercialCorrespondence: {Years: 6},
		jurisdiction.AccountingRecords:        {Years: 8},
	}
	classes := p.Retention().Classes()
	if len(classes) != len(want) {
		t.Fatalf("pack declares %d retention classes, want %d", len(classes), len(want))
	}
	for _, c := range classes {
		keep, known := want[c.Name]
		if !known {
			t.Errorf("unexpected retention class %q", c.Name)
			continue
		}
		if c.Keep != keep {
			t.Errorf("class %s keeps %s, want %s (statutory floor, calendar years)", c.Name, c.Keep, keep)
		}
	}
}
