// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// resolvePreviewRecipe's renewal_reminder branch, provable as a pure unit
// test: the object/date_field validation it must apply before ever
// building a previewDef around a workspace-controlled table/column pair.
// The true end-to-end proof — that the resulting previewDef's predicate
// actually matches real seeded rows under storekit.CompilePredicate and
// RLS scope — needs a real Postgres and is Task 5's job (compose
// integration lane); this file only proves the refusal/acceptance logic
// resolvePreviewRecipe runs before ever reaching the database.

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResolvePreviewRecipeRenewalReminder(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	t.Run("stored instance with neither object nor date_field is refused", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName, Params: json.RawMessage(`{}`)}
		if _, _, err := resolvePreviewRecipe(stored, AutomationPreviewInput{}, now); err == nil {
			t.Fatal("want a refusal — a preview must never guess a table")
		}
	})

	t.Run("stored instance naming an object outside the closed vocabulary is refused", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName,
			Params: json.RawMessage(`{"object":"not_a_real_object","date_field":"cf_renewal"}`)}
		if _, _, err := resolvePreviewRecipe(stored, AutomationPreviewInput{}, now); err == nil {
			t.Fatal("want a refusal for an unknown object")
		}
	})

	t.Run("draft override missing date_field is refused", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName, Params: json.RawMessage(`{}`)}
		in := AutomationPreviewInput{Params: map[string]any{"object": "person"}}
		if _, _, err := resolvePreviewRecipe(stored, in, now); err == nil {
			t.Fatal("want a refusal — date_field is required to preview")
		}
	})

	t.Run("a fully configured instance resolves a previewDef over its own object/column", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_renewal","days_before":15}`)}
		def, window, err := resolvePreviewRecipe(stored, AutomationPreviewInput{}, now)
		if err != nil {
			t.Fatalf("resolvePreviewRecipe: %v", err)
		}
		if def.table != "person" {
			t.Errorf("table = %q, want person", def.table)
		}
		if window != previewDefaultWindowDays {
			t.Errorf("window = %d, want the default %d", window, previewDefaultWindowDays)
		}
		field, ok := def.fields["date_field"]
		if !ok {
			t.Fatal("previewDef has no date_field field entry")
		}
		if want := `t."cf_renewal"`; field.Expr != want {
			t.Errorf("field expr = %q, want %q", field.Expr, want)
		}
		if def.match.And == nil || len(def.match.And) != 2 {
			t.Fatalf("match = %+v, want a two-leg AND (gte from, lte to)", def.match)
		}
	})

	t.Run("a draft override supersedes the stored instance's params", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName, Params: json.RawMessage(`{}`)}
		in := AutomationPreviewInput{Params: map[string]any{"object": "deal", "date_field": "cf_contract_end"}}
		def, _, err := resolvePreviewRecipe(stored, in, now)
		if err != nil {
			t.Fatalf("resolvePreviewRecipe: %v", err)
		}
		if def.table != "deal" {
			t.Errorf("table = %q, want deal (the override, not the empty stored params)", def.table)
		}
	})
}
