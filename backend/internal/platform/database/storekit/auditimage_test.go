// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The audit seam's absent-image rule. `before`/`after` are `any`, so the
// question "did the caller supply an image" is a Go typed-nil question before
// it is a SQL one, and getting it wrong writes a JSON `null` that reads as
// present to every `WHERE before IS NULL` query in the tree.

import "testing"

// An image is absent or it is not, and how the caller spelled the absence is
// not the column's business. Every writer here supplies one — an untyped nil,
// a nil map (the shape a store builds an image in), a nil slice, a nil
// pointer — and each must reach the column as SQL NULL.
//
// The nil-map row is the one that has cost this tree twice: capture's
// own-domain registry stored JSON `null` for a first registration until it was
// found, and identity's record-grant upsert reproduced it from scratch a
// module away. Both are call sites of this one function, which is why the rule
// lives here rather than in either of them.
func TestAnAbsentImageIsSQLNullWhateverKindOfNilCarriesIt(t *testing.T) {
	var (
		nilMap   map[string]any
		nilSlice []string
		nilPtr   *struct{ A int }
	)
	for _, tc := range []struct {
		name  string
		image any
	}{
		{"untyped nil", nil},
		{"nil map", nilMap},
		{"nil slice", nilSlice},
		{"nil pointer", nilPtr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalOrNil(tc.image)
			if err != nil {
				t.Fatalf("marshalOrNil: %v", err)
			}
			if got != nil {
				t.Errorf("marshalOrNil(%s) = %q, want nil bytes so the column stores SQL NULL", tc.name, got)
			}
		})
	}
}

// The mirror, and the reason the rule is a nil check rather than an emptiness
// one: a caller that supplies an EMPTY image is saying something different
// from one that supplies none, and only the second is absent. Without this
// half the rule above is satisfied by a function that returns nil for
// everything falsy.
func TestAnEmptyImageIsStillAnImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		// craft:ignore naked-any it is the audit seam's own parameter type
		image any
	}{
		{"empty map", `{}`, map[string]any{}},
		{"empty slice", `[]`, []string{}},
		{"zero struct", `{"A":0}`, struct{ A int }{}},
		{"false", `false`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalOrNil(tc.image)
			if err != nil {
				t.Fatalf("marshalOrNil: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("marshalOrNil(%s) = %q, want %q — an empty image is not an absent one", tc.name, got, tc.want)
			}
		})
	}
}
