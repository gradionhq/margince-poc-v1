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

// TestApplyTargetChildRejectsAMalformedTo proves a TargetChild field
// whose To carries no "." separator is a declaration error, never a
// panic or a silently-dropped value. The field carries a well-formed
// ChildRow so the separator is the only thing left to reject it.
func TestApplyTargetChildRejectsAMalformedTo(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{{
			From: []string{"email"}, To: "email", Kind: overlay.TargetChild,
			Child: &overlay.ChildRow{Position: 0},
		}},
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
		Fields: []overlay.FieldMapping{{From: []string{"amount", "deal_currency_code"}, To: "amount_minor", Kind: overlay.TargetAssembler, Transform: "amount_minor_by_currency"}},
	}
	if _, _, err := overlay.Apply(m, map[string]any{"amount": 12.5, "deal_currency_code": "EUR"}); err == nil {
		t.Fatal("Apply: want an error for amount_minor_by_currency applied to a non-string amount")
	}
	if _, _, err := overlay.Apply(m, map[string]any{"amount": "not-a-number", "deal_currency_code": "EUR"}); err == nil {
		t.Fatal("Apply: want an error for amount_minor_by_currency applied to an unparsable amount")
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
		{"201", "201-500"},
		{"500", "201-500"},
		{"501", "501-1000"},
		{"1000", "501-1000"},
		{"1001", "1001-5000"},
		{"5000", "1001-5000"},
		{"5001", "5000+"},
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
	for _, nonPositive := range []string{"0", "-5"} {
		if _, _, err := overlay.Apply(m, map[string]any{"numberofemployees": nonPositive}); err == nil {
			t.Fatalf("Apply: want an error for a non-positive headcount %q (never labeled 1-10)", nonPositive)
		}
	}
}

// TestTransformAddressJSONDropsEmptyAndNilFields proves
// transformAddressJSON's own filtering: an empty-string or nil address
// sub-property is dropped from the assembled jsonb rather than landing
// as a spurious empty value. The surviving properties land under the
// contract's Address member names, and a property the rename does not
// know rides through under its own name rather than being dropped.
func TestTransformAddressJSONDropsEmptyAndNilFields(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		Fields: []overlay.FieldMapping{
			{From: []string{"address", "city", "state", "floor"}, To: "address", Kind: overlay.TargetAssembler, Transform: "address_json"},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"address": "Hauptstrasse 1", "city": "", "state": nil, "floor": "3"})
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
	if _, ok := addr["region"]; ok {
		t.Errorf("address.region = %v, want absent (nil dropped)", addr["region"])
	}
	if addr["line1"] != "Hauptstrasse 1" {
		t.Errorf("address.line1 = %v, want Hauptstrasse 1", addr["line1"])
	}
	if addr["floor"] != "3" {
		t.Errorf("address.floor = %v, want the unrenamed property carried through", addr["floor"])
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
// the round-half-away-from-zero conversion (decimalStringToMinor): the old "int64(f*100+0.5)" idiom
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
			{From: []string{"amount", "deal_currency_code"}, To: "amount_minor", Kind: overlay.TargetAssembler, Transform: "amount_minor_by_currency"},
		},
	}

	out, _, err := overlay.Apply(m, map[string]any{"amount": "-12.567", "deal_currency_code": "EUR"})
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
			{From: []string{"amount", "deal_currency_code"}, To: "amount_minor", Kind: overlay.TargetAssembler, Transform: "amount_minor_by_currency"},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"amount": "1.005", "deal_currency_code": "EUR"})
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
			{From: []string{"amount", "deal_currency_code"}, To: "amount_minor", Kind: overlay.TargetAssembler, Transform: "amount_minor_by_currency"},
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
		if _, _, err := overlay.Apply(m, map[string]any{"amount": bad, "deal_currency_code": "EUR"}); err == nil {
			t.Errorf("Apply(amount=%q): want an error, got none", bad)
		}
	}
}

// A contact's work email and mobile number are different rows of one
// collection, distinguished by the type the mapping declares — not by
// anything present in the incumbent property itself. A child target that
// held a single row could express only one of them.
func TestChildTargetsLandAsACollection(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
		Fields: []overlay.FieldMapping{
			{
				From: []string{"phone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "work", "is_primary": true}, Position: 0},
			},
			{
				From: []string{"mobilephone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "mobile", "is_primary": false}, Position: 1},
			},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{
		"hs_object_id": "1", "phone": "+4930111", "mobilephone": "+4917622",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rows, ok := out["person_phone"].([]map[string]any)
	if !ok {
		t.Fatalf("person_phone = %T, want a []map[string]any collection", out["person_phone"])
	}
	if len(rows) != 2 {
		t.Fatalf("person_phone has %d rows, want 2 — work and mobile are separate rows", len(rows))
	}
	if rows[0]["phone"] != "+4930111" || rows[0]["phone_type"] != "work" || rows[0]["is_primary"] != true {
		t.Errorf("row 0 = %v, want the work number carrying its declared attributes", rows[0])
	}
	if rows[1]["phone"] != "+4917622" || rows[1]["phone_type"] != "mobile" {
		t.Errorf("row 1 = %v, want the mobile number carrying its declared attributes", rows[1])
	}
	if rows[0]["position"] != 0 || rows[1]["position"] != 1 {
		t.Errorf("positions = %v/%v, want each row to carry the order the mapping declared", rows[0]["position"], rows[1]["position"])
	}
}

// A row the incumbent sent nothing for is absent, not an empty row: a
// contact with no mobile number must not publish a blank mobile.
func TestChildCollectionOmitsRowsTheIncumbentDidNotSend(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
		Fields: []overlay.FieldMapping{
			{
				From: []string{"phone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "work"}, Position: 0},
			},
			{
				From: []string{"mobilephone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "mobile"}, Position: 1},
			},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1", "phone": "+4930111"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rows, ok := out["person_phone"].([]map[string]any)
	if !ok {
		t.Fatalf("person_phone = %T, want a []map[string]any collection", out["person_phone"])
	}
	if len(rows) != 1 || rows[0]["phone_type"] != "work" {
		t.Errorf("person_phone = %v, want only the work row the incumbent actually sent", rows)
	}
}

// Two rows of one parent claiming the same position is a declaration
// defect, and it makes the collection's order arbitrary. It fires on the
// first record mapped rather than on some unlucky later one.
func TestChildRowsCollidingOnPositionAreADeclarationError(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
		Fields: []overlay.FieldMapping{
			{
				From: []string{"phone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "work"}, Position: 0},
			},
			{
				From: []string{"mobilephone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "mobile"}, Position: 0},
			},
		},
	}
	// The record carries NEITHER colliding property, so only a check made on
	// the declaration itself can catch it.
	_, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1"})
	if err == nil {
		t.Fatal("Apply accepted two child rows at position 0; a collection with an arbitrary order is a declaration defect")
	}
	if !strings.Contains(err.Error(), "person_phone") {
		t.Errorf("error %q does not name the colliding parent, so it does not say where to look", err)
	}
}

// A child field with no ChildRow cannot say which row it belongs to.
func TestChildTargetWithoutARowDeclarationIsAnError(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
		Fields: []overlay.FieldMapping{
			{From: []string{"email"}, To: "person_email.email", Kind: overlay.TargetChild},
		},
	}
	// The record carries none of the mapped properties, so only a check made
	// on the declaration itself can catch it.
	_, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1"})
	if err == nil {
		t.Fatal("Apply accepted a child target with no ChildRow; the row it lands on is then undeclared")
	}
	if !strings.Contains(err.Error(), "person_email.email") {
		t.Errorf("error %q does not name the offending target, so it does not say where to look", err)
	}
}

// A declared attribute may not restate the row's mapped column or its
// position: the row would then carry two answers for one member and the
// winner would depend on map iteration order.
func TestChildRowAttributesMayNotShadowTheRowsOwnMembers(t *testing.T) {
	shadowing := []struct {
		name string
		attr string
	}{
		{name: "the mapped column", attr: "email"},
		{name: "the declared position", attr: "position"},
	}
	for _, s := range shadowing {
		m := overlay.ObjectMapping{
			Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
			Fields: []overlay.FieldMapping{{
				From: []string{"email"}, To: "person_email.email", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{s.attr: "shadow"}, Position: 0},
			}},
		}
		// The record carries none of the mapped properties, so only a check
		// made on the declaration itself can catch it.
		if _, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1"}); err == nil {
			t.Errorf("Apply accepted an attribute shadowing %s", s.name)
		}
	}
}

// A mapping may declare its rows in any order; the collection it yields is
// ordered by the positions it declared, because every consumer reads the rows
// in slice order.
func TestChildCollectionIsOrderedByDeclaredPosition(t *testing.T) {
	m := overlay.ObjectMapping{
		Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
		Fields: []overlay.FieldMapping{
			{
				From: []string{"mobilephone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "mobile"}, Position: 1},
			},
			{
				From: []string{"phone"}, To: "person_phone.phone", Kind: overlay.TargetChild,
				Child: &overlay.ChildRow{Attrs: map[string]any{"phone_type": "work"}, Position: 0},
			},
		},
	}
	out, _, err := overlay.Apply(m, map[string]any{
		"hs_object_id": "1", "phone": "+4930111", "mobilephone": "+4917622",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rows, ok := out["person_phone"].([]map[string]any)
	if !ok {
		t.Fatalf("person_phone = %T, want a []map[string]any collection", out["person_phone"])
	}
	if len(rows) != 2 {
		t.Fatalf("person_phone has %d rows, want 2", len(rows))
	}
	if rows[0]["phone_type"] != "work" || rows[1]["phone_type"] != "mobile" {
		t.Errorf("rows read back %v/%v, want position 0 before position 1 regardless of declaration order",
			rows[0]["phone_type"], rows[1]["phone_type"])
	}
}
