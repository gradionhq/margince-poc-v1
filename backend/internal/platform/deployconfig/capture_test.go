// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"strings"
	"testing"
)

func TestCaptureWarnsOnlyAboutSettingsItNoLongerActsOn(t *testing.T) {
	// Silence for a file that says nothing stale — an operator who never set
	// these must not be told to remove them.
	if w := (Capture{}).Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %v on an empty block, want none", w)
	}
	if w := (Capture{TransactionalExtra: []string{"esp.example"}}).Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %v for a setting that still works, want none", w)
	}

	// The moved keys still PARSE — deleting them would turn an upgrade into a
	// refusal to boot, for a list the operator could then no longer reach —
	// so the warning is the only thing that says they no longer act.
	for _, c := range []Capture{
		{FreemailExtra: []string{"provider.example"}},
		{FreemailNever: []string{"customer.example"}},
		{FreemailExtra: []string{"a.example"}, FreemailNever: []string{"b.example"}},
	} {
		w := c.Warnings()
		if len(w) != 1 {
			t.Fatalf("Warnings() = %v for %+v, want exactly one", w, c)
		}
		// It has to name where the list moved, or it tells an operator their
		// config is ignored without telling them what to do instead.
		if !strings.Contains(w[0], "consumer-mail-domains") || !strings.Contains(w[0], "margince.yaml") {
			t.Errorf("warning %q names neither the new surface nor the file to edit", w[0])
		}
	}
}
