// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The accept-write's pure validation half: field_keys must name grounded
// fields inside the closed deal-writable allowlist, edits flip provenance
// to human, and every value coerces to its column's shape — any refusal
// refuses the WHOLE request (no partial acceptance). The transactional
// half (deal write + audit notes) lives in the integration suite.

import (
	"errors"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// acceptPatchFixture grounds the four deal-writable fields plus one
// grounded-but-unwritable key; payment_terms is honestly omitted, so it
// must never be acceptable.
func acceptPatchFixture() map[string]extraction.ExtractedField {
	return groundedExtractionFields([]extraction.ExtractedField{
		{Field: "name", Value: "Acme Renewal Q3", SourceQuote: "Subject: Acme Renewal Q3", PageOrSection: "p.1", Confidence: "high"},
		{Field: "amount_minor", Value: "150000", SourceQuote: "Total: EUR 1,500.00", PageOrSection: "p.2", Confidence: "high"},
		{Field: "currency", Value: "EUR", SourceQuote: "all amounts in EUR", PageOrSection: "p.2", Confidence: "medium"},
		{Field: "expected_close_date", Value: "2030-12-31", SourceQuote: "offer valid until 2030-12-31", PageOrSection: "p.3", Confidence: "medium"},
		{Field: "owner_id", Value: "3e0f5a9c-0000-0000-0000-000000000001", SourceQuote: "account executive", PageOrSection: "p.1", Confidence: "medium"},
		{Field: "payment_terms", Omitted: true, OmittedReason: "not_stated_in_file"},
	})
}

// requireAcceptRefusal asserts err is the accept flow's typed whole-request
// refusal naming the given field and machine code.
func requireAcceptRefusal(t *testing.T, err error, field, code string) {
	t.Helper()
	var refused *ExtractionAcceptError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want an ExtractionAcceptError (%s/%s)", err, field, code)
	}
	if refused.Field != field || refused.Code != code {
		t.Errorf("refusal = %s/%s, want %s/%s", refused.Field, refused.Code, field, code)
	}
}

func TestGroundedExtractionFieldsExcludesOmitted(t *testing.T) {
	grounded := acceptPatchFixture()
	if _, ok := grounded["payment_terms"]; ok {
		t.Error("an omitted field carries no value; it must not index as grounded")
	}
	if _, ok := grounded["amount_minor"]; !ok {
		t.Error("a grounded field went missing from the index")
	}
}

func TestBuildExtractionAcceptPatchAcceptsAndCoercesGroundedFields(t *testing.T) {
	accepted, patch, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"name", "amount_minor", "currency", "expected_close_date"},
	}, acceptPatchFixture())
	if err != nil {
		t.Fatalf("accept of four grounded writable fields refused: %v", err)
	}
	if len(accepted) != 4 {
		t.Fatalf("accepted %d fields, want 4", len(accepted))
	}
	for _, f := range accepted {
		if f.Provenance != crmcontracts.AcceptedExtractionFieldProvenanceAiExtracted {
			t.Errorf("%s provenance = %s, want ai-extracted (nothing was edited)", f.Field, f.Provenance)
		}
		if f.SourceQuote == "" {
			t.Errorf("%s lost its source quote — the audit note body would be empty", f.Field)
		}
	}
	if patch.Name == nil || *patch.Name != "Acme Renewal Q3" {
		t.Errorf("patch.Name = %v, want Acme Renewal Q3", patch.Name)
	}
	if patch.AmountMinor == nil || *patch.AmountMinor != 150000 {
		t.Errorf("patch.AmountMinor = %v, want the coerced int64 150000", patch.AmountMinor)
	}
	if patch.Currency == nil || *patch.Currency != "EUR" {
		t.Errorf("patch.Currency = %v, want EUR", patch.Currency)
	}
	want := time.Date(2030, 12, 31, 0, 0, 0, 0, time.UTC)
	if patch.ExpectedClose == nil || !patch.ExpectedClose.Equal(want) {
		t.Errorf("patch.ExpectedClose = %v, want %v", patch.ExpectedClose, want)
	}
}

func TestBuildExtractionAcceptPatchRequiresAtLeastOneFieldKey(t *testing.T) {
	_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{FieldKeys: []string{}}, acceptPatchFixture())
	requireAcceptRefusal(t, err, "field_keys", "required")
}

func TestBuildExtractionAcceptPatchRefusesUngroundedKeys(t *testing.T) {
	// A key the extractor never produced and a key it honestly omitted are
	// the same refusal: neither carries evidence to accept.
	for name, key := range map[string]string{"never extracted": "probability", "omitted": "payment_terms"} {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
				FieldKeys: []string{"amount_minor", key},
			}, acceptPatchFixture())
			requireAcceptRefusal(t, err, "field_keys[1]", "not_grounded")
		})
	}
}

func TestBuildExtractionAcceptPatchRefusesGroundedFieldOutsideAllowlist(t *testing.T) {
	// owner_id IS grounded by the fixture, but a row reference is not a
	// document fact — the closed allowlist refuses it whole-request.
	_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"owner_id"},
	}, acceptPatchFixture())
	requireAcceptRefusal(t, err, "field_keys[0]", "not_deal_writable")
}

func TestBuildExtractionAcceptPatchDedupesRepeatedKeys(t *testing.T) {
	accepted, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor", "amount_minor"},
	}, acceptPatchFixture())
	if err != nil {
		t.Fatalf("repeated key refused: %v", err)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted %d fields for a repeated key, want 1 (field_keys is a set)", len(accepted))
	}
}

func TestBuildExtractionAcceptPatchEditFlipsProvenanceToHuman(t *testing.T) {
	edits := map[string]interface{}{"amount_minor": "200000", "currency": float64(0)}
	accepted, patch, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor", "name"},
		Edits:     &edits,
	}, acceptPatchFixture())
	if err != nil {
		t.Fatalf("edited accept refused: %v", err)
	}
	if accepted[0].Provenance != crmcontracts.AcceptedExtractionFieldProvenanceHuman || !accepted[0].Edited {
		t.Errorf("edited field = %+v, want provenance human", accepted[0])
	}
	if accepted[0].Value != "200000" || patch.AmountMinor == nil || *patch.AmountMinor != 200000 {
		t.Errorf("edited value = %q / patch %v, want the edit 200000 over the extracted 150000", accepted[0].Value, patch.AmountMinor)
	}
	// The currency edit targets a field the request never accepts: it must
	// not leak into the patch or flip anything.
	if patch.Currency != nil {
		t.Errorf("patch.Currency = %v, want unset (currency was not in field_keys)", patch.Currency)
	}
	if accepted[1].Provenance != crmcontracts.AcceptedExtractionFieldProvenanceAiExtracted {
		t.Errorf("unedited field provenance = %s, want ai-extracted", accepted[1].Provenance)
	}
}

func TestBuildExtractionAcceptPatchCoercesNumericEdit(t *testing.T) {
	edits := map[string]interface{}{"amount_minor": float64(200000)}
	accepted, patch, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
		Edits:     &edits,
	}, acceptPatchFixture())
	if err != nil {
		t.Fatalf("numeric edit refused: %v", err)
	}
	if accepted[0].Value != "200000" || patch.AmountMinor == nil || *patch.AmountMinor != 200000 {
		t.Errorf("numeric edit landed as %q / %v, want 200000", accepted[0].Value, patch.AmountMinor)
	}
}

func TestBuildExtractionAcceptPatchRefusesNonScalarEdit(t *testing.T) {
	edits := map[string]interface{}{"amount_minor": true}
	_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
		Edits:     &edits,
	}, acceptPatchFixture())
	requireAcceptRefusal(t, err, "edits.amount_minor", "invalid_edit_value")
}

func TestBuildExtractionAcceptPatchRefusesMalformedCoercions(t *testing.T) {
	t.Run("amount_minor must parse as int64", func(t *testing.T) {
		edits := map[string]interface{}{"amount_minor": "12,500.00"}
		_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
			FieldKeys: []string{"amount_minor"},
			Edits:     &edits,
		}, acceptPatchFixture())
		requireAcceptRefusal(t, err, "field_keys[0]", "invalid_integer")
	})
	t.Run("expected_close_date must be a calendar date", func(t *testing.T) {
		edits := map[string]interface{}{"expected_close_date": "end of Q3"}
		_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
			FieldKeys: []string{"expected_close_date"},
			Edits:     &edits,
		}, acceptPatchFixture())
		requireAcceptRefusal(t, err, "field_keys[0]", "invalid_date")
	})
	t.Run("currency must be ISO 4217", func(t *testing.T) {
		edits := map[string]interface{}{"currency": "EURO"}
		_, _, err := buildExtractionAcceptPatch(crmcontracts.AcceptExtractionRequest{
			FieldKeys: []string{"currency"},
			Edits:     &edits,
		}, acceptPatchFixture())
		var parse *values.ParseError
		if !errors.As(err, &parse) {
			t.Fatalf("err = %v, want the money value object's ParseError (the one ISO-4217 spelling)", err)
		}
		if parse.Field != "currency" {
			t.Errorf("ParseError.Field = %q, want currency", parse.Field)
		}
	})
}
