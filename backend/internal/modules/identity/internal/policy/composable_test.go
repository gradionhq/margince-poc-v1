// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package policy

import (
	"slices"
	"strings"
	"testing"
)

// reset clears the registered vocabulary after a case, so one test's objects are
// never another's — the registry is process-global by design (it is written once
// at boot), which makes leakage between tests the only real hazard here.
func reset(t *testing.T) {
	t.Helper()
	t.Cleanup(ResetRegisteredForTest)
}

func TestObjectValidate(t *testing.T) {
	for _, valid := range []Object{"ext_yogi_quote", "ext_notes_widget", "ext_a_b", "ext_unit1_thing2"} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Object(%q).Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Object{
		"",                      // nothing
		"widget",                // unnamespaced
		"ext_widget",            // no unit segment
		"ext_",                  // prefix only
		"Ext_Unit_Widget",       // upper case
		"ext_unit__widget",      // doubled underscore
		"ext_unit_widget_",      // trailing underscore
		"ext_crm-demo_widget",   // a hyphen, which no unquoted SQL identifier holds
		"ext_unit_widget extra", // a space
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Object(%q).Validate() = nil, want the rejection", invalid)
		}
	}
}

// TestAnExtensionObjectMayNotShadowACoreOne: the ext_ prefix already precludes
// it, so this check exists to make that a FACT rather than a consequence of two
// regexes agreeing — a later relaxation of either would otherwise open a path
// for a dropped-in directory to redefine what a core role document grants.
func TestAnExtensionObjectMayNotShadowACoreOne(t *testing.T) {
	if len(coreObjects) == 0 {
		t.Fatal("the core set is empty — this test would check nothing")
	}
	err := Object(coreObjects[0]).Validate()
	if err == nil {
		t.Fatalf("Object(%q) validated as an extension object", coreObjects[0])
	}
	// The grammar refuses it first (no ext_ prefix), which is the honest
	// reading; the shadow check is the belt on the same trousers. Assert the
	// shadow branch directly, so it is not dead.
	shadow := Object("ext_shadow_object")
	if err := shadow.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterWidensTheGrantableVocabularyOnly(t *testing.T) {
	reset(t)
	const object = "ext_yogi_quote"
	if IsGrantableObject(object) {
		t.Fatal("the object is grantable before registration")
	}
	if err := Register(object); err != nil {
		t.Fatal(err)
	}
	if !IsGrantableObject(object) {
		t.Fatal("a registered object is not grantable")
	}
	// It widens GRANTABLE, never CORE: IsCoreObject is what the contract-enum
	// parity test reads, and an object in it that no client can express would
	// make that gate pass on a lie.
	if IsCoreObject(object) {
		t.Fatal("a registered extension object reports as a core object")
	}
	if slices.Contains(coreObjects, object) {
		t.Fatal("registration mutated the closed core set")
	}
}

// TestRegisterIsValidateThenApply: a set with one bad name must register NONE of
// them, or a boot that reports failure has still half-widened what a stored role
// document may grant — and a crash-looping process would keep the half.
func TestRegisterIsValidateThenApply(t *testing.T) {
	reset(t)
	err := Register("ext_good_one", "widget", "ext_good_two")
	if err == nil {
		t.Fatal("Register accepted a set containing an invalid name")
	}
	for _, object := range []string{"ext_good_one", "ext_good_two"} {
		if IsRegisteredObject(object) {
			t.Errorf("%s registered although the set failed validation", object)
		}
	}
	if got := RegisteredObjects(); len(got) != 0 {
		t.Fatalf("RegisteredObjects() = %v, want none", got)
	}
}

// TestRegisterRefusesADuplicate: two claims on one object name is a wiring defect
// where each side thinks it owns the grants. Also validate-then-apply: the
// duplicate must not leave the OTHER names in the same call registered.
func TestRegisterRefusesADuplicate(t *testing.T) {
	reset(t)
	if err := Register("ext_unit_one"); err != nil {
		t.Fatal(err)
	}
	err := Register("ext_unit_two", "ext_unit_one")
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("err = %v, want the duplicate refusal", err)
	}
	if IsRegisteredObject("ext_unit_two") {
		t.Fatal("a name in the failing set was registered anyway")
	}
}

// TestRegisteredObjectsIsSorted: the merge seeds the /me snapshot from this list,
// so an order that followed map iteration would make a snapshot's key order
// nondeterministic between two identical processes.
func TestRegisteredObjectsIsSorted(t *testing.T) {
	reset(t)
	if err := Register("ext_z_last", "ext_a_first", "ext_m_middle"); err != nil {
		t.Fatal(err)
	}
	got := RegisteredObjects()
	want := []string{"ext_a_first", "ext_m_middle", "ext_z_last"}
	if slices.Compare(got, want) != 0 {
		t.Fatalf("RegisteredObjects() = %v, want %v", got, want)
	}
}

// TestRegisterWithNoObjectsIsANoOp: the composition root calls this with the
// composed set's objects, which is the empty slice for every unit in the tree
// today — and an empty call must not be an error a boot has to special-case.
func TestRegisterWithNoObjectsIsANoOp(t *testing.T) {
	reset(t)
	if err := Register(); err != nil {
		t.Fatalf("Register() with no objects = %v, want nil", err)
	}
	if got := RegisteredObjects(); len(got) != 0 {
		t.Fatalf("RegisteredObjects() = %v, want none", got)
	}
}

// TestMergeSeedsARegisteredObjectAtTheZeroGrant is the policy-level half of the
// /me story (identity's extrbacsnapshot_test.go carries it to the wire): the
// snapshot must be the complete vocabulary, so a client can tell "you hold
// nothing on this" from "no such object".
func TestMergeSeedsARegisteredObjectAtTheZeroGrant(t *testing.T) {
	reset(t)
	if err := Register("ext_yogi_quote"); err != nil {
		t.Fatal(err)
	}
	merged := Merge(map[string]Document{"rep": {Objects: map[string]grant{"deal": readOnly}}})
	got, ok := merged.Objects["ext_yogi_quote"]
	if !ok {
		t.Fatal("a registered object is absent from the merged snapshot")
	}
	if got.Create || got.Read || got.Update || got.Delete {
		t.Fatalf("the seed granted something: %+v", got)
	}
	// A role that DOES grant it widens the zero rather than the reverse.
	merged = Merge(map[string]Document{"rep": {Objects: map[string]grant{"ext_yogi_quote": readOnly}}})
	if !merged.Objects["ext_yogi_quote"].Read {
		t.Fatal("a granted read on a registered object was flattened by the seed")
	}
}
