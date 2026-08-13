// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// baseMapping is the shape every mutation below perturbs by exactly one
// declaration detail. It is deliberately not a real HubSpot mapping: the
// question is whether the fingerprint notices a change, not whether this
// particular projection is one the product ships.
func baseMapping() overlay.ObjectMapping {
	return overlay.ObjectMapping{
		Source: "contacts", Target: "person",
		ExternalKey: "hs_object_id", Baseline: "lastmodifieddate",
		UnmappedPolicy: "flag",
		Const:          map[string]any{"kind": "note"},
		Fields: []overlay.FieldMapping{
			{From: []string{"firstname"}, To: "first_name", Kind: overlay.TargetColumn},
			{
				From: []string{"email"}, To: "person_email.email", Kind: overlay.TargetChild,
				Transform: "lowercase",
				Child:     &overlay.ChildRow{Attrs: map[string]any{"email_type": "work"}, Position: 0},
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
		{"child attrs", func(m *overlay.ObjectMapping) { m.Fields[1].Child.Attrs["email_type"] = "personal" }},
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
func TestFingerprintCoversEveryDeclarationField(t *testing.T) {
	for _, tc := range []struct {
		name string
		//craft:ignore naked-any the pinned shapes are heterogeneous struct types read through reflection — the any is the reflection boundary itself
		shape any
		want  int
	}{
		{"ObjectMapping", overlay.ObjectMapping{}, 7},
		{"FieldMapping", overlay.FieldMapping{}, 7},
		{"ChildRow", overlay.ChildRow{}, 2},
	} {
		if got := reflect.TypeOf(tc.shape).NumField(); got != tc.want {
			t.Errorf("%s has %d fields, pinned at %d. A field was added or removed: decide whether it "+
				"belongs in Fingerprint, add or remove its case in TestFingerprintChangesWithEveryDeclarationDetail, "+
				"then move this pin.", tc.name, got, tc.want)
		}
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
