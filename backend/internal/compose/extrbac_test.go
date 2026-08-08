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

	got := extensionRbacObjects([]extension.Verb{gated, alsoGated, ungated})
	if len(got) != 1 || got[0] != "ext_crm_demo_widget" {
		t.Fatalf("derived %v, want exactly [ext_crm_demo_widget]", got)
	}
	// A unit owning no records declares nothing, which is the live tree's state.
	if got := extensionRbacObjects([]extension.Verb{ungated}); len(got) != 0 {
		t.Fatalf("derived %v from an operation declaring no object", got)
	}
	if got := extensionRbacObjects(nil); len(got) != 0 {
		t.Fatalf("derived %v from no operations", got)
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
	if err := RegisterExtensions([]extension.Extension{{Name: "crm-demo", Version: "0.1.0"}}, []extension.Verb{gated}); err != nil {
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
	squatting := unitVerb("crm-demo", "demo_sync", extension.TierAutoExecute, extension.ScopeRead)
	squatting.RbacObject = "ext_crm_demo_widget"
	// Validate would refuse a cross-unit object, so the escape this checks is
	// the one Validate cannot see: a well-namespaced name the VOCABULARY refuses
	// (a doubled underscore is not a legal SQL identifier segment).
	squatting.RbacObject = "ext_crm_demo__widget"
	if err := RegisterExtensions([]extension.Extension{{Name: "crm-demo", Version: "0.1.0"}}, []extension.Verb{squatting}); err == nil {
		t.Fatal("the boot accepted an object the vocabulary refuses")
	}
	if len(ComposedVerbs()) != 0 {
		t.Fatal("the verb set was recorded although the boot failed")
	}
}
