// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestMustBeTotalNamesEveryUndeclaredKind is what stands behind the runner's
// refusal to boot. A check that returned a bare "not total" would send an
// operator diffing two lists by hand, and one that stopped at the first
// missing kind would send them round the loop once per kind.
func TestMustBeTotalNamesEveryUndeclaredKind(t *testing.T) {
	err := MustBeTotal([]string{"privacy_retention_workspace", "zeta_kind", "alpha_kind"})
	if err == nil {
		t.Fatal("two undeclared kinds passed the totality check — a kind with no Spec runs at River's one-minute default")
	}
	// Sorted, so a report is the same on every process and diffable run to run.
	if want := "alpha_kind, zeta_kind"; !strings.Contains(err.Error(), want) {
		t.Errorf("error is %q, want it to name %q", err, want)
	}
	if strings.Contains(err.Error(), "privacy_retention_workspace") {
		t.Errorf("error names a DECLARED kind: %q", err)
	}
}

// TestMustBeTotalAcceptsTheDeclaredTable pins the other half: the check must
// pass for the kinds the contract actually carries, or the runner would refuse
// every boot and the fix would be to delete the check.
func TestMustBeTotalAcceptsTheDeclaredTable(t *testing.T) {
	var kinds []string
	for kind := range Declared() {
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		t.Fatal("the declared table is empty — this test would pass vacuously")
	}
	if err := MustBeTotal(kinds); err != nil {
		t.Errorf("the declared table is not total against itself: %v", err)
	}
}

// TestADeclarationHandedOutIsNoRouteBackIntoTheTable — the compiled table is
// the contract, and a caller that edited a slice inside a Spec it was given
// would edit it for every later reader in the process. The whole claim of this
// package is that what the file says is what the fleet does; a table one
// consumer can reach into makes that true only until somebody sorts a slice in
// place.
func TestADeclarationHandedOutIsNoRouteBackIntoTheTable(t *testing.T) {
	kind, spec := aDeclarationWithBothSlices(t)
	// Read out as STRINGS before anything is touched. Holding the Spec would
	// hold the same backing arrays the mutation below writes through, and the
	// comparison would then be a value against itself — passing precisely when
	// the copy is missing.
	wantField, wantWhen := spec.Args[0].Name, spec.Registration.When[0]

	handed, ok := SpecFor(kind)
	if !ok {
		t.Fatalf("%s is declared but SpecFor does not answer for it", kind)
	}
	handed.Args[0].Name = "mutated"
	handed.Registration.When[0] = "Mutated"

	after, _ := SpecFor(kind)
	if after.Args[0].Name != wantField {
		t.Errorf("%s now declares an args field named %q, want %q — a caller's edit reached the compiled table", kind, after.Args[0].Name, wantField)
	}
	if after.Registration.When[0] != wantWhen {
		t.Errorf("%s now registers on %q, want %q — a caller's edit reached the compiled table", kind, after.Registration.When[0], wantWhen)
	}

	for declared, iterated := range Declared() {
		if declared != kind {
			continue
		}
		iterated.Args[0].Name = "mutated by the iterator's caller"
		iterated.Registration.When[0] = "Mutated by the iterator's caller"
	}
	last, _ := SpecFor(kind)
	if last.Args[0].Name != wantField || last.Registration.When[0] != wantWhen {
		t.Errorf("%s was edited through Declared(); the iterator hands out the same table SpecFor does", kind)
	}
}

// aDeclarationWithBothSlices finds a kind that actually exercises both slices,
// so the test above cannot pass by editing nothing.
func aDeclarationWithBothSlices(t *testing.T) (string, Spec) {
	t.Helper()
	for kind, spec := range Declared() {
		if len(spec.Args) > 0 && len(spec.Registration.When) > 0 {
			return kind, spec
		}
	}
	t.Fatal("no declared kind carries both args and a registration condition, so nothing here would be mutated and this test would prove nothing")
	return "", Spec{}
}

// TestCloneCoversEveryReferenceASpecCarries derives the obligation from the
// TYPE rather than restating clone's body. Every field below is a place where
// copying a Spec by value copies a reference INTO the compiled table, so a Spec
// that grows one — a new slice of conditions, a map of anything — has to be
// added to clone or the handing-out above silently stops being a copy.
func TestCloneCoversEveryReferenceASpecCarries(t *testing.T) {
	deepCopied := []string{"Args", "Registration.When"}
	got := sharedReferences(reflect.TypeFor[Spec](), "")
	slices.Sort(got)

	if !slices.Equal(got, deepCopied) {
		t.Errorf("Spec shares %v with the compiled table, but clone deep-copies %v — every difference is a field a caller can edit the declaration through, or a copy clone makes for nothing",
			got, deepCopied)
	}
}

// sharedReferences reports every path within a type at which a copy would share
// memory with its original. A slice is reported AND walked into: cloning the
// outer slice of a []*T copies the pointers, not what they point at.
//
// An ARRAY is walked into but never reported. Its elements live in the struct
// itself, so copying the Spec copies them; only what those elements in turn
// point at is shared. Reporting one would demand a clone for a field no caller
// can reach the table through.
func sharedReferences(t reflect.Type, path string) []string {
	switch t.Kind() {
	case reflect.Slice:
		return append([]string{path}, sharedReferences(t.Elem(), path+"[]")...)
	case reflect.Array:
		return sharedReferences(t.Elem(), path+"[]")
	case reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func, reflect.Interface:
		return []string{path}
	case reflect.Struct:
		var found []string
		for i := range t.NumField() {
			field := t.Field(i)
			within := field.Name
			if path != "" {
				within = path + "." + field.Name
			}
			found = append(found, sharedReferences(field.Type, within)...)
		}
		return found
	default:
		return nil
	}
}
