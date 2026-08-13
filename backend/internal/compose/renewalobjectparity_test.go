// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// automation.RenewalReminderObjects and customfields.FieldObjects are two
// hand-duplicated copies of the SAME closed object vocabulary — neither
// module may import the other (ADR-0054 §9), so each spells its own list
// from the same datasource.EntityType constants rather than one deriving
// from the other. Nothing inside either module's own package can catch the
// two drifting apart; this package is where both are in scope, so this is
// where the fitness test that binds them together lives (the same posture
// agenttoolparity_test.go and agentscopeparity_test.go already take for
// their own cross-module vocabularies).

import (
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
)

func TestRenewalReminderObjectsMatchesFieldObjectsExactly(t *testing.T) {
	renewal := append([]string(nil), automation.RenewalReminderObjects()...)
	fields := append([]string(nil), customfields.FieldObjects...)
	sort.Strings(renewal)
	sort.Strings(fields)

	if len(renewal) != len(fields) {
		t.Fatalf("automation.RenewalReminderObjects() has %d entries, customfields.FieldObjects has %d — they must name the identical closed set",
			len(renewal), len(fields))
	}
	for i := range renewal {
		if renewal[i] != fields[i] {
			t.Fatalf("automation.RenewalReminderObjects() and customfields.FieldObjects diverge: %v vs %v", renewal, fields)
		}
	}
}
