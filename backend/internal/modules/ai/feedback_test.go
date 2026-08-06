// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// The key is the claim's PATH, never its value. Keyed on the value, a verdict
// would evaporate the moment the evidence shifted — which is precisely when
// the human's answer matters most.
func TestClaimKeyDependsOnThePathAndNotTheValue(t *testing.T) {
	a := ClaimKey("profile_field:title")
	b := ClaimKey("profile_field:title")
	if a != b {
		t.Fatal("the same claim path hashed twice gave two keys; no verdict could survive a re-derivation")
	}
	if a == ClaimKey("profile_field:phone") {
		t.Error("two different claims share a key; suppressing one would suppress the other")
	}
	if len(a) != 64 {
		t.Errorf("key length = %d, want a 64-char sha256 hex digest", len(a))
	}
}

// Normalization happens in ONE place so two surfaces cannot spell the same
// claim differently and lose each other's verdicts.
func TestClaimKeyNormalizesSpacingAndCase(t *testing.T) {
	canonical := ClaimKey("moment:went_quiet")
	for _, variant := range []string{
		"  moment:went_quiet  ",
		"MOMENT:WENT_QUIET",
		"Moment:Went_Quiet\n",
	} {
		if ClaimKey(variant) != canonical {
			t.Errorf("ClaimKey(%q) differs from the canonical spelling", variant)
		}
	}
}

// The lookup key is composite: the same claim path under two different kinds
// is two claims, and a consulting surface must not read one as the other.
func TestVerdictLookupKeySeparatesKinds(t *testing.T) {
	path := ClaimKey("profile_field:title")
	if VerdictLookupKey(ClaimProfileField, path) == VerdictLookupKey(ClaimSignal, path) {
		t.Error("the same path under two claim kinds produced one lookup key")
	}
	if !strings.HasPrefix(VerdictLookupKey(ClaimSignal, path), ClaimSignal+":") {
		t.Error("the lookup key does not lead with its claim kind, so the two halves cannot be told apart")
	}
}

// Every subject the ledger accepts is also an RBAC object the write is gated
// on. A value here with no object would gate on nothing.
func TestEverySubjectTypeIsAnRBACObject(t *testing.T) {
	for subject := range feedbackSubjects {
		if strings.TrimSpace(subject) == "" {
			t.Error("an empty subject type would gate auth.Require on nothing")
		}
	}
	for _, want := range []string{"organization", "person", "deal", "lead"} {
		if !feedbackSubjects[want] {
			t.Errorf("%q is in the column's CHECK but not accepted here", want)
		}
	}
	if feedbackSubjects["activity"] {
		t.Error("a subject outside the column's CHECK would fail at the constraint rather than at the door")
	}
}
