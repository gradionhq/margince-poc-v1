// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestExtensionRbacObjectsComeFromTheDeclaredOperations: the objects a boot
// registers are derived from the composed verb set, never declared in Go — so a
// unit gains a vocabulary entry by declaring x-rbac-object in its fragment and
// no other way.
func TestExtensionRbacObjectsComeFromTheDeclaredOperations(t *testing.T) {
	gated := unitVerb("crm-demo", "demo_sync", extension.TierAutoExecute, extension.ScopeRead)
	gated.RbacObject = "ext_crm_demo_widget"
	// A second operation on the SAME object: one unit's screens share it, so the
	// registration must be de-duplicated or the boot refuses itself.
	alsoGated := unitVerb("crm-demo", "demo_read", extension.TierAutoExecute, extension.ScopeRead)
	alsoGated.RbacObject = "ext_crm_demo_widget"
	ungated := unitVerb("yogi", "yogi_quote", extension.TierAutoExecute, extension.ScopeRead)

	got, err := extensionRbacObjects([]extension.Verb{gated, alsoGated, ungated})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "ext_crm_demo_widget" {
		t.Fatalf("derived %v, want exactly [ext_crm_demo_widget]", got)
	}
	// A unit owning no records declares nothing, which is the live tree's state.
	if got, err := extensionRbacObjects([]extension.Verb{ungated}); err != nil || len(got) != 0 {
		t.Fatalf("derived %v (err %v) from an operation declaring no object", got, err)
	}
	if got, err := extensionRbacObjects(nil); err != nil || len(got) != 0 {
		t.Fatalf("derived %v (err %v) from no operations", got, err)
	}
}

// TestTwoUnitsDerivingOneRbacObjectAreRefusedByName: `ext_` + the unit name with
// hyphens underscored is NOT injective — unit `crm` object `demo_widget` and
// unit `crm-demo` object `widget` both derive `ext_crm_demo_widget`, and both
// clear Verb.Validate because each really is inside its own unit's namespace.
// The vocabulary would refuse the second registration naming neither unit, which
// reads as one unit declaring an object twice and sends an operator to the wrong
// file.
func TestTwoUnitsDerivingOneRbacObjectAreRefusedByName(t *testing.T) {
	crm := unitVerb("crm", "crm_sync", extension.TierAutoExecute, extension.ScopeRead)
	crm.RbacObject = "ext_crm_demo_widget"
	crmDemo := unitVerb("crm-demo", "demo_sync", extension.TierAutoExecute, extension.ScopeRead)
	crmDemo.RbacObject = "ext_crm_demo_widget"
	// Both are individually valid; that is the whole difficulty.
	for _, v := range []extension.Verb{crm, crmDemo} {
		if err := v.Validate(); err != nil {
			t.Fatalf("%s's declaration must be individually valid: %v", v.Unit, err)
		}
	}

	_, err := extensionRbacObjects([]extension.Verb{crm, crmDemo})
	if err == nil {
		t.Fatal("two units deriving one object name were accepted")
	}
	for _, want := range []string{`"crm"`, `"crm-demo"`, "ext_crm_demo_widget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	// And it reaches the boot rather than being a local nicety.
	t.Cleanup(identity.ResetRbacObjectsForTest)
	t.Cleanup(func() { setComposedTools(nil); setComposedVerbs(nil) })
	bootErr := RegisterExtensions([]extension.Extension{
		{Name: "crm", Version: "0.1.0"}, {Name: "crm-demo", Version: "0.1.0"},
	}, []extension.Verb{crm, crmDemo}, nil)
	if bootErr == nil || !strings.Contains(bootErr.Error(), "both derive RBAC object") {
		t.Fatalf("boot err = %v, want the derived-name collision", bootErr)
	}
}

func TestRegisterRbacObjectsWidensTheVocabulary(t *testing.T) {
	t.Cleanup(identity.ResetRbacObjectsForTest)
	const object = "ext_crm_demo_widget"
	if identity.RBACObjectGrantable(object) {
		t.Fatal("the object is grantable before registration")
	}
	if err := RegisterRbacObjects([]identity.RbacObject{object}); err != nil {
		t.Fatal(err)
	}
	if !identity.RBACObjectGrantable(object) {
		t.Fatal("a registered object is not grantable, so an authority requirement naming it can never be satisfied")
	}
	// Empty is a no-op rather than an error: it is what every unit in the tree
	// composes to today, and a boot must not have to special-case it.
	if err := RegisterRbacObjects(nil); err != nil {
		t.Fatalf("RegisterRbacObjects(nil) = %v, want nil", err)
	}
}

// TestRegisterRbacObjectsRefusesANameOutsideTheNamespace: the refusal is the
// module's own (identity's internal policy grammar), and it has to arrive here
// wrapped rather than swallowed — a boot that registered "deal" would let a
// dropped-in directory widen what a core role document grants.
func TestRegisterRbacObjectsRefusesANameOutsideTheNamespace(t *testing.T) {
	t.Cleanup(identity.ResetRbacObjectsForTest)
	err := RegisterRbacObjects([]identity.RbacObject{"ext_crm_demo_widget", "deal"})
	if err == nil || !strings.Contains(err.Error(), "compose:") {
		t.Fatalf("err = %v, want a compose-prefixed refusal", err)
	}
	if identity.RBACObjectGrantable("ext_crm_demo_widget") {
		t.Fatal("a name in the failing set registered anyway — the registration is not validate-then-apply")
	}
}

// TestRegisterExtensionsRegistersTheDeclaredObjects is the boot path end to end:
// nothing else in the process calls RegisterRbacObjects, so if this reconciliation
// stopped doing it the vocabulary would silently be core-only again.
func TestRegisterExtensionsRegistersTheDeclaredObjects(t *testing.T) {
	t.Cleanup(identity.ResetRbacObjectsForTest)
	t.Cleanup(func() { setComposedTools(nil); setComposedVerbs(nil) })
	gated := unitVerb("crm-demo", "demo_sync", extension.TierAutoExecute, extension.ScopeRead)
	gated.RbacObject = "ext_crm_demo_widget"
	if err := RegisterExtensions([]extension.Extension{{Name: "crm-demo", Version: "0.1.0"}}, []extension.Verb{gated}, nil); err != nil {
		t.Fatal(err)
	}
	if !identity.RBACObjectGrantable("ext_crm_demo_widget") {
		t.Fatal("the boot did not register the object its composed set declares")
	}
	// And the verb set is recorded, because the route mounting reads it later —
	// after the Server is assembled, which is after this ran.
	if got := ComposedVerbs(); len(got) != 1 || got[0].Tool != "demo_sync" {
		t.Fatalf("ComposedVerbs() = %v, want the registered set", got)
	}
}

// TestRegisterExtensionsRefusesAnObjectOutsideTheNamespace: the boot aborts
// rather than serving a set whose vocabulary it could not apply.
func TestRegisterExtensionsRefusesAnObjectOutsideTheNamespace(t *testing.T) {
	t.Cleanup(identity.ResetRbacObjectsForTest)
	t.Cleanup(func() { setComposedTools(nil); setComposedVerbs(nil) })
	// Verb.Validate refuses a CROSS-UNIT object, so the escape this checks is
	// the one it cannot see: a name correctly inside the unit's namespace that
	// the VOCABULARY still refuses (a doubled underscore is not a legal SQL
	// identifier segment). The two rules are owned by different packages on
	// purpose, and this is the case that proves the second one still runs.
	squatting := unitVerb("crm-demo", "demo_sync", extension.TierAutoExecute, extension.ScopeRead)
	squatting.RbacObject = "ext_crm_demo__widget"
	if err := RegisterExtensions([]extension.Extension{{Name: "crm-demo", Version: "0.1.0"}}, []extension.Verb{squatting}, nil); err == nil {
		t.Fatal("the boot accepted an object the vocabulary refuses")
	}
	if len(ComposedVerbs()) != 0 {
		t.Fatal("the verb set was recorded although the boot failed")
	}
}
