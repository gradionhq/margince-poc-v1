// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// resolvePreviewRecipe's renewal_reminder branch, provable as a pure unit
// test: the object/date_field validation it must apply before ever
// building a previewDef around a workspace-controlled table/column pair,
// including the live-catalog check (validateRenewalPreviewDateField) via
// a fake fieldcatalog.Reader — no database needed since fieldcatalog.Reader
// is exactly the seam that lets this run without one. The end-to-end
// proof — that the resulting previewDef's predicate actually matches real
// seeded rows under storekit.CompilePredicate and the workspace scope — is
// TestRenewalReminderPreviewMatchesTheRealSeededRows
// (compose/integration/renewal_reminder_integration_test.go); this file
// only proves the refusal/acceptance logic resolvePreviewRecipe runs
// before ever reaching the database.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// fakeFieldCatalog is a DB-free stand-in for fieldcatalog.Reader: it
// answers a fixed set of active columns per object, so a unit test can
// prove renewalPreviewParams' live-column validation without a workspace
// transaction.
type fakeFieldCatalog struct {
	columns map[string][]fieldcatalog.Column
}

func (f fakeFieldCatalog) ActiveColumns(_ context.Context, object string) ([]fieldcatalog.Column, error) {
	return f.columns[object], nil
}

func TestResolvePreviewRecipeRenewalReminder(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	catalog := fakeFieldCatalog{columns: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_renewal", Type: fieldcatalog.TypeDate}},
		"deal":   {{Name: "cf_contract_end", Type: fieldcatalog.TypeDate}},
	}}

	t.Run("stored instance with neither object nor date_field is refused", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName, Params: json.RawMessage(`{}`)}
		if _, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, AutomationPreviewInput{}, now); err == nil {
			t.Fatal("want a refusal — a preview must never guess a table")
		}
	})

	t.Run("stored instance naming an object outside the closed vocabulary is refused", func(t *testing.T) {
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"not_a_real_object","date_field":"cf_renewal"}`),
		}
		if _, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, AutomationPreviewInput{}, now); err == nil {
			t.Fatal("want a refusal for an unknown object")
		}
	})

	t.Run("draft override missing date_field is refused", func(t *testing.T) {
		stored := Automation{Key: renewalReminderName, Params: json.RawMessage(`{}`)}
		in := AutomationPreviewInput{Params: map[string]any{"object": "person"}}
		if _, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, in, now); err == nil {
			t.Fatal("want a refusal — date_field is required to preview")
		}
	})

	t.Run("date_field naming a column absent from the live catalog is refused with a ParamError, not a database error", func(t *testing.T) {
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_does_not_exist","days_before":15}`),
		}
		_, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, AutomationPreviewInput{}, now)
		var paramErr *ParamError
		if err == nil || !errors.As(err, &paramErr) {
			t.Fatalf("resolvePreviewRecipe = %v, want a *ParamError for an unknown date_field", err)
		}
		if paramErr.Field != "params."+paramKeyDateField {
			t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params."+paramKeyDateField)
		}
	})

	t.Run("date_field naming a real but non-date column is refused", func(t *testing.T) {
		wrongType := fakeFieldCatalog{columns: map[string][]fieldcatalog.Column{
			"person": {{Name: "cf_renewal", Type: fieldcatalog.TypeText}},
		}}
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_renewal","days_before":15}`),
		}
		_, _, err := resolvePreviewRecipe(context.Background(), wrongType, stored, AutomationPreviewInput{}, now)
		var paramErr *ParamError
		if err == nil || !errors.As(err, &paramErr) {
			t.Fatalf("resolvePreviewRecipe = %v, want a *ParamError for a wrong-typed date_field", err)
		}
	})

	t.Run("a recurs_yearly instance refuses preview honestly instead of answering a misleading zero", func(t *testing.T) {
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_renewal","days_before":15,"recurs_yearly":true}`),
		}
		_, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, AutomationPreviewInput{}, now)
		var paramErr *ParamError
		if err == nil || !errors.As(err, &paramErr) {
			t.Fatalf("resolvePreviewRecipe(recurs_yearly=true) = %v, want a *ParamError refusing the preview", err)
		}
		if paramErr.Reason != recurringPreviewUnsupportedReason {
			t.Errorf("ParamError.Reason = %q, want %q", paramErr.Reason, recurringPreviewUnsupportedReason)
		}
	})

	t.Run("a fully configured instance resolves a previewDef over its own object/column", func(t *testing.T) {
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_renewal","days_before":15}`),
		}
		def, window, err := resolvePreviewRecipe(context.Background(), catalog, stored, AutomationPreviewInput{}, now)
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
		def, _, err := resolvePreviewRecipe(context.Background(), catalog, stored, in, now)
		if err != nil {
			t.Fatalf("resolvePreviewRecipe: %v", err)
		}
		if def.table != "deal" {
			t.Errorf("table = %q, want deal (the override, not the empty stored params)", def.table)
		}
	})

	t.Run("a nil catalog skips the live-column check (the seam not wired)", func(t *testing.T) {
		stored := Automation{
			Key:    renewalReminderName,
			Params: json.RawMessage(`{"object":"person","date_field":"cf_whatever","days_before":15}`),
		}
		if _, _, err := resolvePreviewRecipe(context.Background(), nil, stored, AutomationPreviewInput{}, now); err != nil {
			t.Fatalf("resolvePreviewRecipe with a nil catalog: %v, want nil (the check is skipped, not failed closed)", err)
		}
	})
}
