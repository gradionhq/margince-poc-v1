// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

import "testing"

// The directory name is the canonical unit name: it keys SQL identifiers and the
// #/ext/<name> route, so a drift between it and Name is a broken route and a
// broken table prefix at once.
func TestNameMatchesTheDirectory(t *testing.T) {
	unit := New()
	if unit.Name != "relay-probe" {
		t.Fatalf("Name = %q, want relay-probe", unit.Name)
	}
}

// This unit exists to be the reference for a PROVIDER-FACING unit, so the
// shapes the documentation points at must actually be present. A reference that
// teaches by absence is worse than no reference.
//
// len() rather than a nil check, and on Channels rather than Channel: the
// surface declares Ingress []IngressSource and Channels []Channel, so a nil
// check would also pass on an empty non-nil slice — a unit that declares the
// field and populates nothing.
func TestDeclaresTheProviderFacingShapes(t *testing.T) {
	unit := New()
	if len(unit.Ingress) == 0 {
		t.Error("Ingress is empty — a provider-facing unit captures")
	}
	if len(unit.Channels) == 0 {
		t.Error("Channels is empty — a provider-facing unit is a transport replies leave on")
	}
}

// The declaration's literals and this unit's own constants are the same
// strings. They are spelled twice on purpose — the operator manifest is derived
// from New()'s AST WITHOUT compiling the unit, so a constant in the declaration
// would be a name the generator cannot resolve — and this is what stops the two
// spellings from becoming two names.
func TestTheDeclaredNamesMatchTheConstants(t *testing.T) {
	unit := New()
	if len(unit.Ingress) == 0 || len(unit.Channels) == 0 {
		t.Fatal("the declaration is missing the shapes TestDeclaresTheProviderFacingShapes covers")
	}
	if got := unit.Ingress[0].System; got != ingressSystem {
		t.Errorf("declared ingress system %q, constant is %q — a landed record's provenance would not match what this unit writes", got, ingressSystem)
	}
	if got := unit.Channels[0].Provider; got != provider {
		t.Errorf("declared channel provider %q, constant is %q — a carried message would land under a provider this unit does not answer to", got, provider)
	}
	if len(unit.Secrets) == 0 || unit.Secrets[0].Key != tokenKey {
		t.Errorf("declared secret key does not match tokenKey %q — the poll would read back a key nothing deposited", tokenKey)
	}
}
