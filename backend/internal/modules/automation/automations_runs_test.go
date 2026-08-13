// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The run-history vocabulary as specs: the wire outcome set, the
// workflow_run.status set (migration 0061's CHECK), and the two maps
// between them stay one closed system — a status without an outcome
// would render an invalid enum on the wire, an outcome without a status
// would make the filter silently empty.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
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
//
// renewal_reminder is not one of previewDefs()'s static entries — its
// table/column are per-instance, so resolvePreviewRecipe builds its
// previewDef dynamically instead (renewalPreviewDef's own doc) — but it
// must still answer the SAME positive guarantee every static entry does:
// a configured instance resolves a complete, SUPPORTED previewDef, not an
// exemption. Asserting that positively here (rather than skipping the key
// entirely) is what stops a SECOND future dynamic-preview catalog entry
// from silently riding the same free pass this key used to.
func TestEveryCatalogKeyHasAPreviewDefinition(t *testing.T) {
	defs := previewDefs()
	renewalCatalog := fakeFieldCatalog{columns: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_renewal_date", Type: fieldcatalog.TypeDate}},
	}}
	for _, entry := range Catalog() {
		if entry.Key == renewalReminderName {
			stored := Automation{
				Key:    renewalReminderName,
				Params: json.RawMessage(`{"object":"person","date_field":"cf_renewal_date","days_before":30}`),
			}
			def, _, err := resolvePreviewRecipe(context.Background(), renewalCatalog, stored, AutomationPreviewInput{}, time.Now())
			if err != nil {
				t.Errorf("resolvePreviewRecipe for a configured renewal_reminder instance: %v", err)
				continue
			}
			if def.unsupported != "" {
				t.Errorf("renewal_reminder's dynamic previewDef is unsupported (%q) for a fully configured instance", def.unsupported)
			}
			if def.table == "" || def.firedCount == nil || len(def.fields) == 0 {
				t.Errorf("renewal_reminder's dynamic previewDef is incomplete: table=%q fields=%d firedCount set=%v",
					def.table, len(def.fields), def.firedCount != nil)
			}
			continue
		}
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
