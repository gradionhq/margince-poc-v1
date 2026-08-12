// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The authorable vocabulary, proven against the selector table rather than
// against a second list. A hard-coded list of scopes here would be exactly the
// drift retentionscope.go exists to prevent: it would keep passing after
// somebody added a selector the authoring surface then silently refused.

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// TestParseRetentionScopeResolvesEveryAuthorableScope walks the whole
// vocabulary: every scope an admin may author must parse, and the pair it
// resolves to must be the one the evaluator keys its selector by.
func TestParseRetentionScopeResolvesEveryAuthorableScope(t *testing.T) {
	authorable := AuthorableScopes()
	if len(authorable) != len(retentionSelectors) {
		t.Fatalf("AuthorableScopes() has %d entries, want one per selector (%d)",
			len(authorable), len(retentionSelectors))
	}

	for _, wire := range authorable {
		t.Run(wire, func(t *testing.T) {
			scope, err := ParseRetentionScope(wire)
			if err != nil {
				t.Fatalf("ParseRetentionScope(%q) refused a scope it advertises as authorable: %v", wire, err)
			}
			if _, known := retentionSelectors[scope.selectorKey()]; !known {
				t.Errorf("scope %q resolved to selector key %q, which the evaluator has no selector for",
					wire, scope.selectorKey())
			}
			// The wire spelling has to survive the round trip, or the screen and
			// the stored row would name the same rule differently.
			if got := scope.String(); got != wire {
				t.Errorf("round trip lost the wire spelling: parsed %q, formatted %q", wire, got)
			}
		})
	}
}

// TestParseRetentionScopeSplitsObjectTypeFromCategory pins the pair each wire
// scope resolves to, including the bare `activity` scope whose category is the
// SQL NULL the seeded DM-SEED-2 row carries — an empty string there would be a
// second, silently different way to say "no finer scope".
func TestParseRetentionScopeSplitsObjectTypeFromCategory(t *testing.T) {
	cases := map[string]RetentionScope{
		"lead/unconverted":          {ObjectType: "lead", Category: "unconverted"},
		"activity":                  {ObjectType: "activity"},
		"activity/transcript":       {ObjectType: "activity", Category: "transcript"},
		"person/no_consent_no_deal": {ObjectType: "person", Category: "no_consent_no_deal"},
		"deal/lost":                 {ObjectType: "deal", Category: "lost"},
		"deal/won":                  {ObjectType: "deal", Category: "won"},
		"ai_call_payload/content":   {ObjectType: "ai_call_payload", Category: "content"},
	}
	if len(cases) != len(retentionSelectors) {
		t.Fatalf("this table covers %d scopes, the selector table has %d — a new selector needs its pair pinned here",
			len(cases), len(retentionSelectors))
	}

	for wire, want := range cases {
		scope, err := ParseRetentionScope(wire)
		if err != nil {
			t.Fatalf("ParseRetentionScope(%q): %v", wire, err)
		}
		if scope != want {
			t.Errorf("ParseRetentionScope(%q) = %+v, want %+v", wire, scope, want)
		}
	}

	bare, err := ParseRetentionScope("activity")
	if err != nil {
		t.Fatalf("ParseRetentionScope(activity): %v", err)
	}
	if bare.Category != "" {
		t.Errorf("bare activity scope carries category %q, want empty", bare.Category)
	}
	if bare.CategoryPtr() != nil {
		t.Errorf("bare activity scope renders category %q for the database, want NULL", *bare.CategoryPtr())
	}
}

// TestParseRetentionScopeNormalizesTheSelectorSpelling records what the parser
// does with the evaluator's own trailing-slash key: it resolves to the SAME
// single scope as the wire spelling. Two spellings that both stored a row would
// be two rows for one selector, which is the bound MaxPassDuration rests on.
func TestParseRetentionScopeNormalizesTheSelectorSpelling(t *testing.T) {
	fromKey, err := ParseRetentionScope("activity/")
	if err != nil {
		t.Fatalf("ParseRetentionScope(activity/): %v", err)
	}
	fromWire, err := ParseRetentionScope("activity")
	if err != nil {
		t.Fatalf("ParseRetentionScope(activity): %v", err)
	}
	if fromKey != fromWire {
		t.Errorf("the two spellings of the bare activity scope resolved differently: %+v vs %+v", fromKey, fromWire)
	}
	if got := fromKey.String(); got != "activity" {
		t.Errorf("the selector spelling formatted back as %q, want the wire spelling %q", got, "activity")
	}
}

// TestParseRetentionScopeRefusesAnUnservableScope is the refusal an admin
// actually meets. A stored policy for an unknown scope would be a rule that
// provably never runs, so the refusal has to name what IS authorable — one
// round trip is all the admin gets.
func TestParseRetentionScopeRefusesAnUnservableScope(t *testing.T) {
	for _, wire := range []string{"deal/abandoned", "person", "", "activity/nonsense", "lead"} {
		t.Run(wire, func(t *testing.T) {
			scope, err := ParseRetentionScope(wire)
			if err == nil {
				t.Fatalf("ParseRetentionScope(%q) accepted a scope with no selector, resolving to %+v", wire, scope)
			}
			if scope != (RetentionScope{}) {
				t.Errorf("a refused parse returned %+v, want the zero scope", scope)
			}
			var unknown UnknownScopeError
			if !errors.As(err, &unknown) {
				t.Fatalf("refusal is not an UnknownScopeError, so it would report as an internal fault: %v", err)
			}
			if unknown.Scope != wire {
				t.Errorf("refusal names scope %q, want the value the caller sent (%q)", unknown.Scope, wire)
			}

			var fault apperrors.FieldFault
			if !errors.As(err, &fault) {
				t.Fatalf("refusal does not implement FieldFault, so it would not classify as a 422 on the field: %v", err)
			}
			field, code, message := fault.FieldFault()
			if field != "scope" {
				t.Errorf("refusal names field %q, want scope", field)
			}
			if code != "unknown_retention_scope" {
				t.Errorf("refusal code = %q, want unknown_retention_scope", code)
			}
			for _, authorable := range AuthorableScopes() {
				if !strings.Contains(message, authorable) {
					t.Errorf("refusal message omits the authorable scope %q, leaving the admin guessing at a closed vocabulary: %s",
						authorable, message)
				}
			}
		})
	}
}

// TestAuthorableScopesIsTheSelectorTableInWireSpelling derives the expectation
// from the selector table itself: adding a selector widens the vocabulary with
// no second list to remember, and this test keeps passing only because it
// re-derives rather than restates.
func TestAuthorableScopesIsTheSelectorTableInWireSpelling(t *testing.T) {
	want := make([]string, 0, len(retentionSelectors))
	for key := range retentionSelectors {
		want = append(want, strings.TrimSuffix(key, "/"))
	}
	sort.Strings(want)

	got := AuthorableScopes()
	if len(got) != len(want) {
		t.Fatalf("AuthorableScopes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AuthorableScopes() = %v, want %v (sorted, so the refusal message is stable)", got, want)
		}
	}

	// Stable across calls: the refusal message and the contract enum are both
	// read from it, and a map-order-dependent list would reorder per process.
	if second := AuthorableScopes(); !slices.Equal(got, second) {
		t.Errorf("two calls disagreed: %v then %v", got, second)
	}
}

// TestScopeOfInvertsCategoryPtr closes the loop between the stored columns and
// the wire: a row read back from the database has to name itself the way the
// request that authored it did, for the NULL category as much as for a set one.
func TestScopeOfInvertsCategoryPtr(t *testing.T) {
	for _, wire := range AuthorableScopes() {
		t.Run(wire, func(t *testing.T) {
			scope, err := ParseRetentionScope(wire)
			if err != nil {
				t.Fatalf("ParseRetentionScope(%q): %v", wire, err)
			}
			back := ScopeOf(scope.ObjectType, scope.CategoryPtr())
			if back != scope {
				t.Errorf("ScopeOf(CategoryPtr()) = %+v, want %+v", back, scope)
			}
			if back.String() != wire {
				t.Errorf("a stored row names itself %q, want %q", back.String(), wire)
			}
		})
	}
}
