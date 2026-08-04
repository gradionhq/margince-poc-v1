// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
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
