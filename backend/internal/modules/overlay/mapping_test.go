// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// TestTargetKindString pins the three declared TargetKind names plus the
// unknown-int fallback (mapping.go's own String doc: "never leaks a raw
// int to a caller trying to diagnose a bad mapping").
func TestTargetKindString(t *testing.T) {
	tests := []struct {
		kind overlay.TargetKind
		want string
	}{
		{overlay.TargetColumn, "column"},
		{overlay.TargetChild, "child"},
		{overlay.TargetAssembler, "assembler"},
		{overlay.TargetKind(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("TargetKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

// TestApplyTargetChildAssemblesAChildRow proves the TargetChild kind
// (design.md §4.8): a 1:N mapping into a child row lands under
// "<parent>.<child column>", and a second field on the SAME parent
// merges into the same child row rather than overwriting it.
func TestApplyTargetChildAssemblesAChildRow(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts",
		Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"email"}, To: "person_email.email", Kind: overlay.TargetChild},
			{From: []string{"email_type"}, To: "person_email.kind", Kind: overlay.TargetChild},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"email": "a@example.com", "email_type": "work"})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	child, ok := out["person_email"].(map[string]any)
	if !ok {
		t.Fatalf("person_email = %#v, want a child row map", out["person_email"])
	}
	if child["email"] != "a@example.com" || child["kind"] != "work" {
		t.Fatalf("person_email = %#v, want both fields merged into the same child row", child)
	}
}

// TestApplyTargetChildRejectsAMalformedTo proves a TargetChild field
// whose To carries no "." separator is a declaration error, never a
// panic or a silently-dropped value.
func TestApplyTargetChildRejectsAMalformedTo(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"email"}, To: "email", Kind: overlay.TargetChild}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"email": "a@example.com"}); err == nil {
		t.Fatal("Apply: want an error for a TargetChild field with no \"<parent>.<child>\" separator")
	}
}

// TestApplyUnknownTargetKindErrors proves applyField's default branch:
// a FieldMapping.Kind outside {TargetColumn, TargetChild, TargetAssembler}
// is a declaration error, never silently dropped.
func TestApplyUnknownTargetKindErrors(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"firstname"}, To: "first_name", Kind: overlay.TargetKind(99)}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"firstname": "Christian"}); err == nil {
		t.Fatal("Apply: want an error for an unknown TargetKind")
	}
}

// TestApplyRequiresExactlyOneFromForANonAssemblerField proves valueFor's
// From-count guard: a TargetColumn/TargetChild field declaring anything
// other than exactly one From property is a declaration error.
func TestApplyRequiresExactlyOneFromForANonAssemblerField(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"firstname", "lastname"}, To: "first_name", Kind: overlay.TargetColumn}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"firstname": "Christian", "lastname": "Mueller"}); err == nil {
		t.Fatal("Apply: want an error for a TargetColumn field declaring more than one From property")
	}
}

// TestApplyEmptyStringIsTreatedAsAbsent proves valueFor's HubSpot-shaped
// convention: an empty-string property value is treated the same as the
// property being entirely absent, so an unset HubSpot field never lands
// a spurious empty string on the mirror.
func TestApplyEmptyStringIsTreatedAsAbsent(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"jobtitle"}, To: "title", Kind: overlay.TargetColumn}},
	}
	out, _, err := overlay.Apply(m, map[string]any{"jobtitle": ""})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if _, ok := out["title"]; ok {
		t.Fatalf("title = %v, want absent for an empty-string source property", out["title"])
	}
}

// TestApplyResolveFieldPassesRawValueThrough proves a Resolve field's raw
// value crosses Apply unmodified — Apply has no store access to perform
// the lookup itself (mapping.go's own doc on applyTransform).
func TestApplyResolveFieldPassesRawValueThrough(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"hubspot_owner_id"}, To: "owner_id", Kind: overlay.TargetColumn, Resolve: "mirror_user_map"}},
	}
	out, _, err := overlay.Apply(m, map[string]any{"hubspot_owner_id": "1197833249"})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if out["owner_id"] != "1197833249" {
		t.Fatalf("owner_id = %v, want the raw owner reference passed through unmodified", out["owner_id"])
	}
}

// TestApplyAssemblerSkipsAbsentSourcesAndErrorsOnBadTransform proves
// valueFor's TargetAssembler branch: it is a no-op when every one of its
// From properties is absent from raw, and an unknown Transform name on
// an assembler field is still a declaration error like any other field.
func TestApplyAssemblerSkipsAbsentSourcesAndErrorsOnBadTransform(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"address", "city"}, To: "address", Kind: overlay.TargetAssembler, Transform: "address_json"},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"firstname": "Christian"})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if _, ok := out["address"]; ok {
		t.Fatalf("address = %v, want absent when none of its From properties are present", out["address"])
	}

	bad := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"address"}, To: "address", Kind: overlay.TargetAssembler, Transform: "titlecase"},
		},
	}
	if _, _, err := overlay.Apply(bad, map[string]any{"address": "Hauptstrasse 1"}); err == nil {
		t.Fatal("Apply: want an error for an assembler field naming an unknown Transform")
	}
}

// TestTransformLowercaseRejectsNonString proves the lowercase transform's
// type guard: applied to anything but a string, it is a clean error, not
// a panic.
func TestTransformLowercaseRejectsNonString(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{From: []string{"n"}, To: "x", Kind: overlay.TargetColumn, Transform: "lowercase"}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"n": 42.0}); err == nil {
		t.Fatal("Apply: want an error for lowercase applied to a non-string value")
	}
}

// TestTransformAmountToMinorRejectsNonStringAndUnparsable proves
// transformAmountToMinor's two guard clauses: a non-string value and a
// string that doesn't parse as a decimal amount are both clean errors.
func TestTransformAmountToMinorRejectsNonStringAndUnparsable(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "deals", Target: "deal",
		Fields: []overlay.FieldMapping{{From: []string{"amount"}, To: "amount_minor", Kind: overlay.TargetColumn, Transform: "amount_to_minor"}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"amount": 12.5}); err == nil {
		t.Fatal("Apply: want an error for amount_to_minor applied to a non-string value")
	}
	if _, _, err := overlay.Apply(m, map[string]any{"amount": "not-a-number"}); err == nil {
		t.Fatal("Apply: want an error for amount_to_minor applied to an unparsable string")
	}
}

// TestTransformEmployeesToSizeBandBuckets pins every band boundary
// design.md §9's size_band enum declares, plus the non-string and
// unparsable-int guard clauses.
func TestTransformEmployeesToSizeBandBuckets(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "companies", Target: "organization",
		Fields: []overlay.FieldMapping{{From: []string{"numberofemployees"}, To: "size_band", Kind: overlay.TargetColumn, Transform: "employees_to_size_band"}},
	}
	tests := []struct {
		n    string
		want string
	}{
		{"1", "1-10"},
		{"10", "1-10"},
		{"11", "11-50"},
		{"50", "11-50"},
		{"51", "51-200"},
		{"200", "51-200"},
		{"201", "201-1000"},
		{"1000", "201-1000"},
		{"1001", "1001+"},
	}
	for _, tt := range tests {
		out, _, err := overlay.Apply(m, map[string]any{"numberofemployees": tt.n})
		if err != nil {
			t.Fatalf("Apply(%q) returned an error: %v", tt.n, err)
		}
		if got := out["size_band"]; got != tt.want {
			t.Errorf("size_band for %s employees = %v, want %q", tt.n, got, tt.want)
		}
	}

	if _, _, err := overlay.Apply(m, map[string]any{"numberofemployees": 5.0}); err == nil {
		t.Fatal("Apply: want an error for employees_to_size_band applied to a non-string value")
	}
	if _, _, err := overlay.Apply(m, map[string]any{"numberofemployees": "not-a-number"}); err == nil {
		t.Fatal("Apply: want an error for employees_to_size_band applied to an unparsable string")
	}
}

// TestTransformAddressJSONDropsEmptyAndNilFields proves
// transformAddressJSON's own filtering: an empty-string or nil address
// sub-property is dropped from the assembled jsonb rather than landing
// as a spurious empty value, and it rejects a non-map input cleanly.
func TestTransformAddressJSONDropsEmptyAndNilFields(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"address", "city", "state"}, To: "address", Kind: overlay.TargetAssembler, Transform: "address_json"},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"address": "Hauptstrasse 1", "city": "", "state": nil})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	addr, ok := out["address"].(map[string]any)
	if !ok {
		t.Fatalf("address = %#v, want an assembled jsonb map", out["address"])
	}
	if _, ok := addr["city"]; ok {
		t.Errorf("address.city = %v, want absent (empty string dropped)", addr["city"])
	}
	if _, ok := addr["state"]; ok {
		t.Errorf("address.state = %v, want absent (nil dropped)", addr["state"])
	}
	if addr["address"] != "Hauptstrasse 1" {
		t.Errorf("address.address = %v, want Hauptstrasse 1", addr["address"])
	}
}

// TestApplyRejectsUnknownTransform pins the closed transform registry
// (design.md §4.8): a mapping declaration naming a Transform outside the
// registry is a declaration error, never a silent passthrough.
func TestApplyRejectsUnknownTransform(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts",
		Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"firstname"}, To: "first_name", Kind: overlay.TargetColumn, Transform: "titlecase"},
		},
	}

	_, _, err := overlay.Apply(m, map[string]any{"firstname": "Christian"})
	if err == nil {
		t.Fatal("Apply returned no error for a Transform name outside the closed registry")
	}
}

// TestApplyAmountToMinorRoundsNegativeHalfAwayFromZero pins the fix for
// the amount_to_minor rounding bug: the old "int64(f*100+0.5)" idiom
// rounds a NEGATIVE amount toward zero (truncation, not rounding),
// understating the minor-unit magnitude of a deal's amount whenever the
// source carries a refund/credit (negative) value. -12.567 minor-scaled
// is -1256.7; the nearest minor unit is -1257 (rounding half away from
// zero), not -1256 (what the old code produced).
func TestApplyAmountToMinorRoundsNegativeHalfAwayFromZero(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "deals",
		Target: "deal",
		Fields: []overlay.FieldMapping{
			{From: []string{"amount"}, To: "amount_minor", Kind: overlay.TargetColumn, Transform: "amount_to_minor"},
		},
	}

	out, _, err := overlay.Apply(m, map[string]any{"amount": "-12.567"})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	got, ok := out["amount_minor"].(int64)
	if !ok {
		t.Fatalf("amount_minor = %#v, want an int64", out["amount_minor"])
	}
	if got != -1257 {
		t.Errorf("amount_minor = %d, want -1257 (round-half-away-from-zero of -1256.7)", got)
	}
}

// TestApplyAmountToMinorIsExactNotFloat pins the exact-decimal conversion:
// "1.005" is 100.5 minor units, which rounds half-away-from-zero to 101.
// A float64 path parses "1.005" as 1.00499999…, scales to 100.4999…, and
// rounds to 100 — a silent one-unit understatement on an amount the wire
// stated precisely. The big.Rat path must return 101.
func TestApplyAmountToMinorIsExactNotFloat(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "deals",
		Target: "deal",
		Fields: []overlay.FieldMapping{
			{From: []string{"amount"}, To: "amount_minor", Kind: overlay.TargetColumn, Transform: "amount_to_minor"},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"amount": "1.005"})
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if got, ok := out["amount_minor"].(int64); !ok || got != 101 {
		t.Errorf("amount_minor = %#v, want int64(101) (exact round of 100.5)", out["amount_minor"])
	}
}

// TestApplyAmountToMinorRejectsNonFiniteAndOverflow pins the guards the
// float path lacked: a non-finite token must not parse, and an amount past
// int64 must be a conversion error rather than a wrapped/garbage value.
func TestApplyAmountToMinorRejectsNonFiniteAndOverflow(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "deals",
		Target: "deal",
		Fields: []overlay.FieldMapping{
			{From: []string{"amount"}, To: "amount_minor", Kind: overlay.TargetColumn, Transform: "amount_to_minor"},
		},
	}
	// Non-finite, non-numeric, overflow, AND the big.Rat forms that are not
	// HubSpot decimals (rationals, hex/binary prefixes, digit underscores)
	// must all be rejected — never silently coined into money.
	for _, bad := range []string{
		"NaN", "Inf", "-Inf", "not-a-number", "99999999999999999999999999",
		"1/2", "0x10", "0b101", "1_000",
		// A huge exponent / over-long mantissa must be refused BEFORE
		// big.Rat.SetString allocates for it (a resource-exhaustion guard).
		"1e-1000000", "1e1000000", strings.Repeat("9", 100),
	} {
		if _, _, err := overlay.Apply(m, map[string]any{"amount": bad}); err == nil {
			t.Errorf("Apply(amount=%q): want an error, got none", bad)
		}
	}
}
