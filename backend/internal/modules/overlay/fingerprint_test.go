// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// baseMapping is the shape every mutation below perturbs by exactly one
// declaration detail. It is deliberately not a real HubSpot mapping: the
// question is whether the fingerprint notices a change, not whether this
// particular projection is one the product ships.
//
// Const and Attrs carry several keys each, and values of several types, because
// a one-key map makes the ordering the digest depends on unobservable —
// sorting one key is indistinguishable from not sorting at all, so the
// stability test below could only pass. Their mixed types are what makes a
// type-blind rendering of a value observable too.
func baseMapping() overlay.ObjectMapping {
	return overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		ExternalKey: "hs_object_id", Baseline: "lastmodifieddate",
		UnmappedPolicy: "flag",
		Const: map[string]any{
			"kind": "note", "origin": "hubspot", "archived": false, "revision": 2,
		},
		Fields: []overlay.FieldMapping{
			{From: []string{"firstname"}, To: "first_name", Kind: overlay.TargetColumn},
			{
				From: []string{"email"}, To: "person_email.email", Kind: overlay.TargetChild,
				Transform: "lowercase",
				Child: &overlay.ChildRow{
					Attrs: map[string]any{
						"email_type": "work", "is_primary": true, "verified": false, "rank": 1,
					},
					Position: 0,
				},
			},
		},
	}
}

// A projection is only as good as the declaration that produced it, so any
// change to that declaration has to change the fingerprint — otherwise a
// mapping edit leaves already-mirrored rows claiming to be current when the
// projection they hold is one this code would never produce again.
func TestFingerprintChangesWithEveryDeclarationDetail(t *testing.T) {
	base := overlay.Fingerprint(baseMapping())
	for _, tc := range []struct {
		name   string
		mutate func(*overlay.ObjectMapping)
	}{
		{"source", func(m *overlay.ObjectMapping) { m.Source = "companies" }},
		{"target", func(m *overlay.ObjectMapping) { m.Target = "organization" }},
		{"external key", func(m *overlay.ObjectMapping) { m.ExternalKey = "id" }},
		{"baseline", func(m *overlay.ObjectMapping) { m.Baseline = "hs_lastmodifieddate" }},
		{"unmapped policy", func(m *overlay.ObjectMapping) { m.UnmappedPolicy = "drop" }},
		{"const value", func(m *overlay.ObjectMapping) { m.Const["kind"] = "call" }},
		{"const key", func(m *overlay.ObjectMapping) { m.Const = map[string]any{"other": "note"} }},
		{"field from", func(m *overlay.ObjectMapping) { m.Fields[0].From = []string{"lastname"} }},
		{"field to", func(m *overlay.ObjectMapping) { m.Fields[0].To = "last_name" }},
		{"field kind", func(m *overlay.ObjectMapping) { m.Fields[0].Kind = overlay.TargetAssembler }},
		{"field transform", func(m *overlay.ObjectMapping) { m.Fields[1].Transform = "uppercase" }},
		{"field resolve", func(m *overlay.ObjectMapping) { m.Fields[0].Resolve = "mirror_user_map" }},
		{"field always-emit", func(m *overlay.ObjectMapping) { m.Fields[0].AlwaysEmit = true }},
		{"const value type", func(m *overlay.ObjectMapping) { m.Const["revision"] = "2" }},
		{"child attrs", func(m *overlay.ObjectMapping) { m.Fields[1].Child.Attrs["email_type"] = "personal" }},
		{"child attr value type", func(m *overlay.ObjectMapping) { m.Fields[1].Child.Attrs["is_primary"] = "true" }},
		{"child position", func(m *overlay.ObjectMapping) { m.Fields[1].Child.Position = 1 }},
		{"field added", func(m *overlay.ObjectMapping) {
			m.Fields = append(m.Fields, overlay.FieldMapping{From: []string{"jobtitle"}, To: "title"})
		}},
		{"field removed", func(m *overlay.ObjectMapping) { m.Fields = m.Fields[:1] }},
		{"field order", func(m *overlay.ObjectMapping) { m.Fields[0], m.Fields[1] = m.Fields[1], m.Fields[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseMapping()
			tc.mutate(&m)
			if got := overlay.Fingerprint(m); got == base {
				t.Errorf("changing the %s left the fingerprint at %s, so rows projected by the old "+
					"declaration would read as current. Include it in Fingerprint.", tc.name, got)
			}
		})
	}
}

// The mutation table above is only exhaustive while the structs it perturbs
// are. A new field on either has to be decided about — hashed, or explicitly
// not — and this pin is what forces that decision instead of letting the
// field default into being ignored.
//
// The pin is on the field NAMES, not their count: a change that swaps one field
// for another in a single edit keeps the count identical, and that is exactly
// the case where the replacement silently misses the digest.
func TestFingerprintCoversEveryDeclarationField(t *testing.T) {
	for _, tc := range []struct {
		name string
		//craft:ignore naked-any the pinned shapes are heterogeneous struct types read through reflection — the any is the reflection boundary itself
		shape any
		want  []string
	}{
		{"ObjectMapping", overlay.ObjectMapping{}, []string{
			"Baseline", "Const", "ExternalKey", "Fields", "Source", "Target", "UnmappedPolicy",
		}},
		{"FieldMapping", overlay.FieldMapping{}, []string{
			"AlwaysEmit", "Child", "From", "Kind", "Resolve", "To", "Transform",
		}},
		{"ChildRow", overlay.ChildRow{}, []string{"Attrs", "Position"}},
	} {
		if got := sortedFieldNames(tc.shape); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s declares %v, pinned at %v. A field was added, removed or renamed: decide whether it "+
				"belongs in Fingerprint, add or remove its case in TestFingerprintChangesWithEveryDeclarationDetail, "+
				"then move this pin.", tc.name, got, tc.want)
		}
	}
}

// sortedFieldNames answers a struct's field names in a stable order, so the pin
// above reads as the set it is and a reordering of the declaration alone does
// not fail it.
//
//craft:ignore naked-any the argument is whichever declaration struct is being pinned — the any is the reflection boundary itself
func sortedFieldNames(shape any) []string {
	shapeType := reflect.TypeOf(shape)
	names := make([]string, 0, shapeType.NumField())
	for i := range shapeType.NumField() {
		names = append(names, shapeType.Field(i).Name)
	}
	sort.Strings(names)
	return names
}

// A rendering that flattens a value before it reaches the digest collides on
// declarations that project differently. Each pair below is one such collision:
// the two sides are distinct declarations, so the mapping edit between them has
// to re-project the estate rather than leave it claiming to be current.
func TestFingerprintSeparatesDeclarationsThatFlattenAlike(t *testing.T) {
	for _, tc := range []struct {
		name    string
		left    func(*overlay.ObjectMapping)
		right   func(*overlay.ObjectMapping)
		because string
	}{
		{
			name: "a two-property From against a one-property From naming a space",
			left: func(m *overlay.ObjectMapping) {
				m.Fields[0].Kind = overlay.TargetAssembler
				m.Fields[0].From = []string{"a", "b"}
			},
			right: func(m *overlay.ObjectMapping) {
				m.Fields[0].Kind = overlay.TargetAssembler
				m.Fields[0].From = []string{"a b"}
			},
			because: "an assembler gathering two raw properties projects a different payload from one " +
				"gathering a single property whose name contains a space",
		},
		{
			name:  "a bool const against the string spelling it",
			left:  func(m *overlay.ObjectMapping) { m.Const["archived"] = true },
			right: func(m *overlay.ObjectMapping) { m.Const["archived"] = "true" },
			because: "Const values are copied verbatim into the mirrored payload, where the JSON true " +
				"and the JSON \"true\" are different values",
		},
		{
			name:  "a bool child attribute against the string spelling it",
			left:  func(m *overlay.ObjectMapping) { m.Fields[1].Child.Attrs["is_primary"] = true },
			right: func(m *overlay.ObjectMapping) { m.Fields[1].Child.Attrs["is_primary"] = "true" },
			because: "Attrs are copied verbatim into the mirrored child row, where the JSON true and " +
				"the JSON \"true\" are different values",
		},
		{
			name:  "a numeric const against the string spelling it",
			left:  func(m *overlay.ObjectMapping) { m.Const["revision"] = 2 },
			right: func(m *overlay.ObjectMapping) { m.Const["revision"] = "2" },
			because: "a mirrored number and a mirrored string are different JSON values, and a consumer " +
				"reading the column gets a different type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right := baseMapping(), baseMapping()
			tc.left(&left)
			tc.right(&right)
			if overlay.Fingerprint(left) == overlay.Fingerprint(right) {
				t.Errorf("both declarations fingerprint to %s, so switching between them would leave every "+
					"mirrored row reading as current — but %s. Feed the difference into Fingerprint.",
					overlay.Fingerprint(left), tc.because)
			}
		})
	}
}

// The other half of the same obligation: a difference that CANNOT change a
// projected payload must not move the digest, or the estate re-projects for
// nothing. A nil From and an empty one both gather no raw property.
func TestFingerprintTreatsAnAbsentAndAnEmptyFromAlike(t *testing.T) {
	nilFrom, emptyFrom := baseMapping(), baseMapping()
	nilFrom.Fields[0].From = nil
	emptyFrom.Fields[0].From = []string{}

	got, want := overlay.Fingerprint(nilFrom), overlay.Fingerprint(emptyFrom)
	if got != want {
		t.Errorf("a nil From fingerprints to %s and an empty one to %s, but neither gathers a raw property, "+
			"so the two project identically; re-projecting the estate to move between them buys nothing. "+
			"Hash the length, not the container.", got, want)
	}
}

// A fingerprint that varied between processes would mark every row stale
// forever and block the flip permanently, so map iteration order must not
// reach the digest.
func TestFingerprintIsStableAcrossRuns(t *testing.T) {
	first := overlay.Fingerprint(baseMapping())
	for i := 0; i < 50; i++ {
		if got := overlay.Fingerprint(baseMapping()); got != first {
			t.Fatalf("run %d produced %s, want %s — an unstable fingerprint marks every row stale forever", i, got, first)
		}
	}
	if first == "" {
		t.Fatal("Fingerprint answered the empty string; a row stamped with it could never be compared")
	}
}
