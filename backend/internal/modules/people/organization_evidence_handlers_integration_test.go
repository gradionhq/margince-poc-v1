// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The transport for the org-360 evidence reads: the handlers wrap the
// gated store reads onto the wire shape, answer [] (never null) for an org
// with no evidence, and existence-hide a foreign/absent org as 404.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestListOrganizationFactsHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	// Populated org → 200 with its facts.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/organizations/x/facts", nil).WithContext(ctx)
	h.ListOrganizationFacts(rec, req, crmcontracts.Id(orgID.UUID))
	if rec.Code != http.StatusOK {
		t.Fatalf("facts handler status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var facts crmcontracts.OrganizationFactListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &facts); err != nil {
		t.Fatalf("decode facts response: %v", err)
	}
	if len(facts.Data) != 2 {
		t.Fatalf("facts handler returned %d facts, want 2", len(facts.Data))
	}

	// Org with no facts → 200 with an empty array, never null.
	empty, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{DisplayName: "Bare GmbH", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ListOrganizationFacts(rec, req, empty.Id)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty facts status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != `{"data":[]}`+"\n" && body != `{"data":[]}` {
		t.Fatalf("empty facts body = %q, want an empty data array (never null)", body)
	}

	// A foreign/absent org is existence-hidden as 404.
	rec = httptest.NewRecorder()
	h.ListOrganizationFacts(rec, req, crmcontracts.Id(ids.NewV7()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent-org facts status = %d, want 404", rec.Code)
	}
}

func TestListOrganizationProfileFieldsHandler(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)
	h := Handlers{store: e.store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/organizations/x/profile-fields", nil).WithContext(ctx)
	h.ListOrganizationProfileFields(rec, req, crmcontracts.Id(orgID.UUID))
	if rec.Code != http.StatusOK {
		t.Fatalf("profile-fields handler status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var fields crmcontracts.OrganizationProfileFieldListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode profile-fields response: %v", err)
	}
	if len(fields.Data) != 2 {
		t.Fatalf("profile-fields handler returned %d fields, want 2", len(fields.Data))
	}

	rec = httptest.NewRecorder()
	h.ListOrganizationProfileFields(rec, req, crmcontracts.Id(ids.NewV7()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent-org profile-fields status = %d, want 404", rec.Code)
	}
}
