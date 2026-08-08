// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// listInput stands in for a store's list input: one field per operand kind the
// bindings cover, so a test says what a binding WROTE rather than that it
// returned no error.
type listInput struct {
	Owner  *ids.UserID
	Status *string
	Flag   *bool
}

var probeFilters = FilterSet[listInput]{
	"owner_id": FilterID(func(in *listInput, id *ids.UserID) { in.Owner = id }),
	"status":   FilterWord(func(in *listInput, v *string) { in.Status = v }),
	"stalled":  FilterFlag(func(in *listInput, v *bool) { in.Flag = v }),
}

// Each binding writes the field it names. A binding that parsed its operand and
// then wrote nowhere would leave the list unnarrowed while reporting success,
// which is the failure the whole shape exists to prevent.
func TestEachBindingNarrowsTheFieldItNames(t *testing.T) {
	owner := ids.NewV7()
	var in listInput

	if err := probeFilters.Apply(&in, map[string]string{
		"owner_id": owner.String(), "status": "open", "stalled": "true",
	}); err != nil {
		t.Fatalf("applying three filters: %v", err)
	}

	if in.Owner == nil || in.Owner.UUID != owner {
		t.Errorf("owner_id bound %v, want %v", in.Owner, owner)
	}
	if in.Status == nil || *in.Status != "open" {
		t.Errorf("status bound %v, want open", in.Status)
	}
	if in.Flag == nil || !*in.Flag {
		t.Errorf("stalled bound %v, want true", in.Flag)
	}
}

// A name the set does not carry is REFUSED. Ignoring it would run the
// enumeration unnarrowed and answer a wider question than the caller asked, in
// a shape indistinguishable from the right answer.
func TestAnUnknownFilterIsRefusedRatherThanIgnored(t *testing.T) {
	var in listInput

	// A KNOWN filter travels with it, and sorts first — so a set that bound
	// what it could and then bailed would leave a half-narrowed input behind,
	// which is the state the assertion below exists to catch. With the unknown
	// name alone, nothing binds before the refusal and that check cannot fail.
	err := probeFilters.Apply(&in, map[string]string{"status": "open", "tag": "vip"})

	if err == nil {
		t.Fatal("an unknown filter was accepted, so the list ran unnarrowed")
	}
	if !strings.Contains(err.Error(), "tag") {
		t.Errorf("the refusal does not name the filter it refused: %v", err)
	}
	if in.Status != nil || in.Owner != nil || in.Flag != nil {
		t.Errorf("a refused filter set left a partially narrowed input: %+v", in)
	}
}

// An operand the field cannot take is refused, and the refusal names the filter
// without echoing the value — the value is caller text traveling back out.
func TestAMalformedOperandIsRefusedWithoutEchoingIt(t *testing.T) {
	for _, tc := range []struct{ name, filter, value string }{
		{"a reference that is not a uuid", "owner_id", "not-a-uuid"},
		{"a flag that is not a boolean", "stalled", "sometimes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in listInput

			err := probeFilters.Apply(&in, map[string]string{tc.filter: tc.value})

			if err == nil {
				t.Fatalf("%s=%s was accepted", tc.filter, tc.value)
			}
			if !strings.Contains(err.Error(), tc.filter) {
				t.Errorf("the refusal does not say which filter was wrong: %v", err)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("the refusal echoes the caller's own operand back at them: %v", err)
			}
		})
	}
}

// The published vocabulary is sorted. A client that caches a tool schema reads
// a reshuffled list as a contract change, so map order must not reach it.
func TestTheVocabularyIsOrderedRatherThanMapOrdered(t *testing.T) {
	names := probeFilters.Names()
	want := []string{"owner_id", "stalled", "status"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	}
}
