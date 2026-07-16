// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The run-history vocabulary as specs: the wire outcome set, the
// workflow_run.status set (migration 0061's CHECK), and the two maps
// between them stay one closed system — a status without an outcome
// would render an invalid enum on the wire, an outcome without a status
// would make the filter silently empty.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestRunOutcomeAndStatusMapsAreTotalInverses(t *testing.T) {
	if len(runStatusByOutcome) != len(runOutcomeByStatus) {
		t.Fatalf("outcome→status has %d entries, status→outcome has %d — the maps must be bijective",
			len(runStatusByOutcome), len(runOutcomeByStatus))
	}
	for outcome, status := range runStatusByOutcome {
		if !crmcontracts.AutomationRunOutcome(outcome).Valid() {
			t.Errorf("outcome %q is not a contract AutomationRunOutcome member", outcome)
		}
		if back := runOutcomeByStatus[status]; back != outcome {
			t.Errorf("outcome %q → status %q → outcome %q: the maps disagree", outcome, status, back)
		}
	}
	// Every status of the 0061 CHECK constraint renders on the wire.
	for _, status := range []string{"applied", "skipped", "failed", "requires_approval", "blocked"} {
		outcome, ok := runOutcomeByStatus[status]
		if !ok {
			t.Errorf("workflow_run status %q has no wire outcome — a stored run would render an empty enum", status)
			continue
		}
		if !crmcontracts.AutomationRunOutcome(outcome).Valid() {
			t.Errorf("status %q maps to %q, not a contract outcome", status, outcome)
		}
	}
}

// Every instantiable catalog type must be previewable: the designer's
// dry-run is part of the A72 surface, so a new catalog entry without a
// preview definition is a defect this fitness check catches at unit time.
func TestEveryCatalogKeyHasAPreviewDefinition(t *testing.T) {
	defs := previewDefs()
	for _, entry := range Catalog() {
		def, ok := defs[entry.Key]
		if !ok {
			t.Errorf("catalog key %q has no preview definition — POST /automations/{id}/preview would 500", entry.Key)
			continue
		}
		if def.unsupported != "" {
			continue // a documented gap (previewNotYetSupported's own reason), not a missing definition
		}
		if def.table == "" || def.firedCount == nil || len(def.fields) == 0 {
			t.Errorf("preview definition for %q is incomplete: table=%q fields=%d firedCount set=%v",
				entry.Key, def.table, len(def.fields), def.firedCount != nil)
		}
	}
	for key := range defs {
		if _, ok := CatalogEntryByKey(key); !ok {
			t.Errorf("preview definition %q names no catalog entry — dead definition or a renamed key", key)
		}
	}
}
