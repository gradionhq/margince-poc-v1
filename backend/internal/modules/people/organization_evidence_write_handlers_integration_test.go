// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The transport for the org-360 evidence writes: a correction and a
// confirmation each reach the right row, a malformed If-Match is refused
// before the store is touched, and a stale one surfaces as version skew.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestUpdateOrganizationProfileFieldHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	// A correction reaches the named field and comes back human-sourced.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/organizations/x/profile-fields/icp",
		strings.NewReader(`{"value":"Utilities"}`)).WithContext(ctx)
	h.UpdateOrganizationProfileField(rec, req, crmcontracts.Id(orgID.UUID), "icp",
		crmcontracts.UpdateOrganizationProfileFieldParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("profile-field correction status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out crmcontracts.CompanyProfileField
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode corrected profile field: %v", err)
	}
	if out.Value != "Utilities" {
		t.Fatalf("corrected value = %q, want %q", out.Value, "Utilities")
	}
	if string(out.Source) != "human" {
		t.Fatalf("corrected source = %q, want human — a correction is a human act", out.Source)
	}

	// The sibling field is untouched: a correction addresses one field, and
	// addressing it by org alone would rewrite the whole profile.
	fields, err := e.store.ListOrganizationProfileFields(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		if string(f.Field) == "legal_name" && f.Value != "Voltaq Systems GmbH" {
			t.Fatalf("legal_name = %q after correcting icp, want it untouched", f.Value)
		}
	}

	// A malformed If-Match is refused as a 422 before any write.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/organizations/x/profile-fields/icp",
		strings.NewReader(`{"value":"Nope"}`)).WithContext(ctx)
	req.Header.Set("If-Match", "not-a-version")
	h.UpdateOrganizationProfileField(rec, req, crmcontracts.Id(orgID.UUID), "icp",
		crmcontracts.UpdateOrganizationProfileFieldParams{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed If-Match status = %d, want 422", rec.Code)
	}
}

func TestConfirmOrganizationProfileFieldHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	// Confirming keeps the value and records the human agreement.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/organizations/x/profile-fields/icp/confirm", nil).WithContext(ctx)
	h.ConfirmOrganizationProfileField(rec, req, crmcontracts.Id(orgID.UUID), "icp",
		crmcontracts.ConfirmOrganizationProfileFieldParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("profile-field confirm status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out crmcontracts.CompanyProfileField
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode confirmed profile field: %v", err)
	}
	if out.Value != "Energy-intensive manufacturers" {
		t.Fatalf("confirmed value = %q, want the claim unchanged", out.Value)
	}
	if out.VerifiedAt == nil {
		t.Fatal("confirmed profile field has no verified_at — the confirmation left no trace")
	}

	// A foreign/absent org is existence-hidden rather than refused.
	rec = httptest.NewRecorder()
	h.ConfirmOrganizationProfileField(rec, req, crmcontracts.Id(ids.NewV7()), "icp",
		crmcontracts.ConfirmOrganizationProfileFieldParams{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent-org confirm status = %d, want 404", rec.Code)
	}
}

func TestUpdateOrganizationFactHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	// A fact is addressed by field AND value_key. The seeded company/phone
	// carries an empty value_key, so "phone:" must reach it and nothing else.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/organizations/x/facts/phone:",
		strings.NewReader(`{"value":"+49 30 9999"}`)).WithContext(ctx)
	h.UpdateOrganizationFact(rec, req, crmcontracts.Id(orgID.UUID), "phone:",
		crmcontracts.UpdateOrganizationFactParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("fact correction status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out crmcontracts.OrganizationFact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode corrected fact: %v", err)
	}
	if out.Value != "+49 30 9999" {
		t.Fatalf("corrected fact value = %q, want the new phone", out.Value)
	}

	// The certification fact shares neither field nor value_key and stays put.
	facts, err := e.store.ListOrganizationFacts(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if string(f.Field) == "certification" && f.Value != "ISO 9001" {
			t.Fatalf("certification = %q after correcting phone, want it untouched", f.Value)
		}
	}

	// A key with no separator names no row at all. That is a malformed
	// address, not a missing fact, and answering 404 would tell the caller
	// this fact once existed.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/organizations/x/facts/phone",
		strings.NewReader(`{"value":"+49 30 0000"}`)).WithContext(ctx)
	h.UpdateOrganizationFact(rec, req, crmcontracts.Id(orgID.UUID), "phone",
		crmcontracts.UpdateOrganizationFactParams{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("separator-less fact key status = %d, want 422", rec.Code)
	}
}

func TestConfirmOrganizationFactHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/organizations/x/facts/confirm", nil).WithContext(ctx)
	h.ConfirmOrganizationFact(rec, req, crmcontracts.Id(orgID.UUID), "certification:iso 9001",
		crmcontracts.ConfirmOrganizationFactParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("fact confirm status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out crmcontracts.OrganizationFact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode confirmed fact: %v", err)
	}
	if out.Value != "ISO 9001" {
		t.Fatalf("confirmed fact value = %q, want the claim unchanged", out.Value)
	}
	if out.VerifiedAt == nil {
		t.Fatal("confirmed fact has no verified_at — the confirmation left no trace")
	}

	// A stale If-Match is version skew, not a silent overwrite.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/organizations/x/facts/confirm", nil).WithContext(ctx)
	req.Header.Set("If-Match", "1")
	h.ConfirmOrganizationFact(rec, req, crmcontracts.Id(orgID.UUID), "certification:iso 9001",
		crmcontracts.ConfirmOrganizationFactParams{})
	if rec.Code != http.StatusPreconditionFailed && rec.Code != http.StatusConflict {
		t.Fatalf("stale If-Match status = %d, want 409/412 version skew", rec.Code)
	}
}
