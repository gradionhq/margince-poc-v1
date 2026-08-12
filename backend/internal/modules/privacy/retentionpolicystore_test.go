// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The authoring store's decisions that are made before any SQL runs: the sparse
// merge, the two field refusals, and the audit image. The transactional half —
// the RBAC gate, the unique-constraint conflict, the audit row — is proven in
// internal/compose/integration against a real Postgres.

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// storedPolicy is the row a patch is folded onto: the seeded transcript rung,
// which is a destructive policy with a lawful basis, so every field a patch can
// touch starts non-zero and an accidental reset is visible.
func storedPolicy() Policy {
	basis := "legal_obligation"
	return Policy{
		ID:                  ids.NewV7(),
		Scope:               RetentionScope{ObjectType: "activity", Category: "transcript"},
		RetainDays:          365,
		Action:              actionErase,
		LawfulBasis:         &basis,
		Enabled:             true,
		SuppressedByPosture: true,
	}
}

func TestApplyPatchLeavesAnOmittedFieldAlone(t *testing.T) {
	before := storedPolicy()

	after := applyPatch(before, PolicyPatch{})

	if after != before {
		t.Errorf("an all-nil patch changed the row: %+v, want %+v", after, before)
	}
}

// TestApplyPatchSetsOneFieldAtATime is the sparse-edit contract: the screen
// sends only what the admin touched, so a patch of one field must not carry the
// zero value of the other three into the row.
func TestApplyPatchSetsOneFieldAtATime(t *testing.T) {
	days := 2555
	action := actionArchive
	basis := "consent"
	enabled := false

	cases := map[string]struct {
		patch PolicyPatch
		want  func(Policy) Policy
	}{
		"retain_days": {
			patch: PolicyPatch{RetainDays: &days},
			want:  func(p Policy) Policy { p.RetainDays = days; return p },
		},
		"action": {
			patch: PolicyPatch{Action: &action},
			want:  func(p Policy) Policy { p.Action = action; return p },
		},
		"lawful_basis": {
			patch: PolicyPatch{LawfulBasis: &basis},
			want:  func(p Policy) Policy { p.LawfulBasis = &basis; return p },
		},
		"enabled": {
			patch: PolicyPatch{Enabled: &enabled},
			want:  func(p Policy) Policy { p.Enabled = enabled; return p },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := storedPolicy()
			want := tc.want(before)

			after := applyPatch(before, tc.patch)

			if after.RetainDays != want.RetainDays || after.Action != want.Action ||
				after.Enabled != want.Enabled || *after.LawfulBasis != *want.LawfulBasis {
				t.Errorf("applyPatch(%s) = %+v, want %+v", name, after, want)
			}
			// The scope is absent from the patch by design — re-targeting a row
			// would re-attribute its audit history — so it survives every merge.
			if after.Scope != before.Scope || after.ID != before.ID {
				t.Errorf("patch moved the row's identity: %+v → %+v", before, after)
			}
		})
	}
}

// TestApplyPatchCannotClearTheLawfulBasis records what the merge DOES rather
// than what the sparse shape might suggest: a nil LawfulBasis means "unchanged",
// so there is no patch that sets the column back to NULL. An admin who must
// remove the basis re-authors the policy.
func TestApplyPatchCannotClearTheLawfulBasis(t *testing.T) {
	before := storedPolicy()

	after := applyPatch(before, PolicyPatch{LawfulBasis: nil})

	if after.LawfulBasis == nil {
		t.Fatal("a nil LawfulBasis in the patch cleared the stored basis; nil means unchanged on every other field too")
	}
	if *after.LawfulBasis != *before.LawfulBasis {
		t.Errorf("lawful basis changed to %q, want the stored %q", *after.LawfulBasis, *before.LawfulBasis)
	}

	// Set from absent is the direction that does work.
	basis := "legitimate_interest"
	fresh := before
	fresh.LawfulBasis = nil
	if got := applyPatch(fresh, PolicyPatch{LawfulBasis: &basis}); got.LawfulBasis == nil || *got.LawfulBasis != basis {
		t.Errorf("patching a basis onto a row that had none gave %v, want %q", got.LawfulBasis, basis)
	}
}

// TestApplyPatchDoesNotDeriveSuppression pins that the merge is about STORED
// fields. Suppression is a live reading of the posture, stamped by the caller
// after the write, so applyPatch must not invent one from the new action.
func TestApplyPatchDoesNotDeriveSuppression(t *testing.T) {
	before := storedPolicy() // erase, suppressed
	archive := actionArchive

	after := applyPatch(before, PolicyPatch{Action: &archive})

	if !after.SuppressedByPosture {
		t.Error("applyPatch recomputed SuppressedByPosture; the posture is read once per transaction by the caller, not derived here")
	}
}

func TestValidateRetentionActionAcceptsOnlyTheExecutableActions(t *testing.T) {
	for _, action := range []string{actionArchive, actionAnonymize, actionErase} {
		if err := validateRetentionAction(action); err != nil {
			t.Errorf("validateRetentionAction(%q) refused an action the engine executes: %v", action, err)
		}
	}
	for _, action := range []string{"", "purge", "delete", "Erase", "anonymise"} {
		err := validateRetentionAction(action)
		if err == nil {
			t.Fatalf("validateRetentionAction(%q) accepted an action the engine has no executor for", action)
		}
		assertFieldFault(t, err, "action", "invalid_retention_action")
	}
}

// TestValidateRetainDaysRefusesAWindowThatActsImmediately covers the value the
// comment calls the dangerous one: zero is not a harmless edge, it is a policy
// that empties its scope on the next pass.
func TestValidateRetainDaysRefusesAWindowThatActsImmediately(t *testing.T) {
	for _, days := range []int{1, 30, 365, 2555} {
		if err := validateRetainDays(days); err != nil {
			t.Errorf("validateRetainDays(%d) refused a legitimate window: %v", days, err)
		}
	}
	for _, days := range []int{0, -1, -365} {
		err := validateRetainDays(days)
		if err == nil {
			t.Fatalf("validateRetainDays(%d) accepted a window that acts on a record as soon as it exists", days)
		}
		assertFieldFault(t, err, fieldRetainDays, "invalid_retain_days")
	}
}

// assertFieldFault checks a refusal classifies as a 422 against the named
// field on every surface, not only on REST.
func assertFieldFault(t *testing.T, err error, wantField, wantCode string) {
	t.Helper()
	var fault apperrors.FieldFault
	if !errors.As(err, &fault) {
		t.Fatalf("refusal does not implement FieldFault, so it would report as an internal fault: %v", err)
	}
	field, code, message := fault.FieldFault()
	if field != wantField {
		t.Errorf("refusal names field %q, want %q", field, wantField)
	}
	if code != wantCode {
		t.Errorf("refusal code = %q, want %q", code, wantCode)
	}
	if message == "" {
		t.Error("refusal carries no message; the caller has nothing to act on")
	}
	// The log line has to name the field too, not only the wire fault.
	if !strings.Contains(err.Error(), wantField) {
		t.Errorf("Error() = %q, want it to name the field %q", err.Error(), wantField)
	}
}

// TestAuditImageCarriesTheStoredFieldsOnly is the field-history guard. Folding
// the live posture reading into the image would make the projection report a
// policy edit every time the posture moved — a change to a row nobody touched.
func TestAuditImageCarriesTheStoredFieldsOnly(t *testing.T) {
	policy := storedPolicy() // SuppressedByPosture: true, so the leak would show

	image := auditImage(policy)

	if _, present := image["suppressed_by_posture"]; present {
		t.Error("audit image carries suppressed_by_posture, a live posture reading; the field history would report an edit whenever the posture moved")
	}
	want := map[string]any{
		"scope":         "activity/transcript",
		fieldRetainDays: policy.RetainDays,
		"action":        policy.Action,
		"lawful_basis":  policy.LawfulBasis,
		"enabled":       policy.Enabled,
	}
	if len(image) != len(want) {
		t.Fatalf("audit image has keys %v, want exactly %v", keysOf(image), keysOf(want))
	}
	for key, wantValue := range want {
		got, present := image[key]
		if !present {
			t.Errorf("audit image is missing %q; the field history would never show it changing", key)
			continue
		}
		if got != wantValue {
			t.Errorf("audit image[%q] = %v, want %v", key, got, wantValue)
		}
	}
}

// TestAuditImageNamesTheBareScopeWithoutATrailingSlash keeps the audit ledger
// reading in the wire vocabulary: the NULL-category row is `activity`, the
// spelling the screen and the contract enum use.
func TestAuditImageNamesTheBareScopeWithoutATrailingSlash(t *testing.T) {
	policy := storedPolicy()
	policy.Scope = RetentionScope{ObjectType: "activity"}

	if got := auditImage(policy)["scope"]; got != "activity" {
		t.Errorf("audit image names the bare scope %v, want %q", got, "activity")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
