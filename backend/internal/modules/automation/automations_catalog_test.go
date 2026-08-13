// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"errors"
	"testing"
)

// TestValidateRenewalReminderParamsRejectsUnknownObject proves object is
// checked against the closed renewalReminderObjectSet — a typo or a
// resource this vocabulary deliberately excludes (activity, relationship;
// customfields.FieldObjects' own doc explains why those two are excluded)
// is a named ParamError, never a save that silently draws zero candidates
// forever.
func TestValidateRenewalReminderParamsRejectsUnknownObject(t *testing.T) {
	err := validateRenewalReminderParams(map[string]any{"object": "activity", "date_field": "renewal_date"})
	var paramErr *ParamError
	if err == nil {
		t.Fatal("validateRenewalReminderParams accepted an unknown object, want a ParamError")
	}
	if !errors.As(err, &paramErr) {
		t.Fatalf("validateRenewalReminderParams returned %v (%T), want *ParamError", err, err)
	}
	if paramErr.Field != "params.object" {
		t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params.object")
	}
}

// TestValidateRenewalReminderParamsAcceptsEveryKnownObject proves the
// positive side of the same check: every member of renewalReminderObjects
// is accepted, not just rejected on the negative case above.
func TestValidateRenewalReminderParamsAcceptsEveryKnownObject(t *testing.T) {
	for _, object := range renewalReminderObjects {
		if err := validateRenewalReminderParams(map[string]any{"object": object, "date_field": "renewal_date"}); err != nil {
			t.Errorf("validateRenewalReminderParams rejected known object %q: %v", object, err)
		}
	}
}

// TestValidateRenewalReminderParamsRejectsEmptyDateField proves date_field
// must be non-empty — an instance with an empty field name has nothing to
// watch and would never surface a candidate.
func TestValidateRenewalReminderParamsRejectsEmptyDateField(t *testing.T) {
	err := validateRenewalReminderParams(map[string]any{"object": "person", "date_field": ""})
	var paramErr *ParamError
	if err == nil || !errors.As(err, &paramErr) {
		t.Fatalf("validateRenewalReminderParams(empty date_field) = %v, want a *ParamError", err)
	}
	if paramErr.Field != "params.date_field" {
		t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params.date_field")
	}
}

// TestValidateRenewalReminderParamsRecursYearly proves recurs_yearly is
// accepted true, false, or absent (defaulting false at read time via
// timescan.go's renewalDateFieldScanParams) — and rejected when present
// but not a boolean.
func TestValidateRenewalReminderParamsRecursYearly(t *testing.T) {
	base := map[string]any{"object": "person", "date_field": "renewal_date"}

	for _, recurs := range []any{true, false, nil} {
		params := map[string]any{"object": base["object"], "date_field": base["date_field"]}
		if recurs != nil {
			params["recurs_yearly"] = recurs
		}
		if err := validateRenewalReminderParams(params); err != nil {
			t.Errorf("validateRenewalReminderParams(recurs_yearly=%v) = %v, want nil", recurs, err)
		}
	}

	badParams := map[string]any{"object": base["object"], "date_field": base["date_field"], "recurs_yearly": "yes"}
	err := validateRenewalReminderParams(badParams)
	var paramErr *ParamError
	if err == nil || !errors.As(err, &paramErr) {
		t.Fatalf("validateRenewalReminderParams(recurs_yearly=%q) = %v, want a *ParamError", "yes", err)
	}
	if paramErr.Field != "params.recurs_yearly" {
		t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params.recurs_yearly")
	}
}

// TestValidateRenewalReminderParamsKeepsDaysBeforeValidation proves
// days_before's existing bounded-integer check is untouched by the three
// new keys: an out-of-range value is still rejected the same way
// validateSingleIntParam already rejected it before #706.
func TestValidateRenewalReminderParamsKeepsDaysBeforeValidation(t *testing.T) {
	err := validateRenewalReminderParams(map[string]any{"days_before": float64(400)})
	var paramErr *ParamError
	if err == nil || !errors.As(err, &paramErr) {
		t.Fatalf("validateRenewalReminderParams(days_before=400) = %v, want a *ParamError", err)
	}
	if paramErr.Field != "params.days_before" {
		t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params.days_before")
	}
}

// TestValidateRenewalReminderParamsRejectsUnknownKey proves the schema
// stays closed: a params key outside the four named ones is refused, the
// same additionalProperties:false posture every other catalog schema here
// enforces by hand.
func TestValidateRenewalReminderParamsRejectsUnknownKey(t *testing.T) {
	err := validateRenewalReminderParams(map[string]any{"not_a_real_key": true})
	var paramErr *ParamError
	if err == nil || !errors.As(err, &paramErr) {
		t.Fatalf("validateRenewalReminderParams(unknown key) = %v, want a *ParamError", err)
	}
	if paramErr.Field != "params.not_a_real_key" {
		t.Errorf("ParamError.Field = %q, want %q", paramErr.Field, "params.not_a_real_key")
	}
}
