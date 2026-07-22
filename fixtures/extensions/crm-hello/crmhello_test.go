// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmhello

import "testing"

func TestNewDeclaresTheFixtureUnit(t *testing.T) {
	e := New()
	if e.Name != "crm-hello" {
		t.Fatalf("Name = %q, want the unit's directory name crm-hello", e.Name)
	}
	if e.Version == "" {
		t.Fatal("Version is empty — the boot inventory records it")
	}
	if len(e.Jurisdictions) != 1 {
		t.Fatalf("declaration carries %d jurisdiction packs, want 1", len(e.Jurisdictions))
	}
	if got := e.Jurisdictions[0].Code(); got != "zz" {
		t.Fatalf("pack code = %q, want the user-assigned fixture code zz", got)
	}
	if ret := e.Jurisdictions[0].Retention(); ret != nil {
		t.Fatalf("fixture pack carries retention classes %v, want none", ret.Classes())
	}
}
