// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The mode refusal, at the edge. A workspace reading from the incumbent
// mirror gets ONE honest 422 rather than a page that quietly omits most of
// itself — and a mode lookup that FAILS refuses too, because serving native
// data when the lookup broke is the silent fallback the overlay module
// exists to prevent.
//
// Both cases return before the service is reached, which is what lets these
// run with a nil service: if either ever started touching the database, this
// test would panic rather than pass.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func overlayModeReturning(overlay bool, err error) OverlayMode {
	return func(context.Context) (bool, error) { return overlay, err }
}

func TestGetOrganization360RefusesAnOverlayWorkspace(t *testing.T) {
	h := NewHandlers(nil, overlayModeReturning(true, nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/x/360", nil)

	h.GetOrganization360(rec, req, crmcontracts.Id(ids.NewV7()))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decoding the problem body: %v", err)
	}
	if problem.Code != "validation_error" {
		t.Errorf("problem code = %q, want validation_error", problem.Code)
	}
	errs := problem.Details.Errors
	if len(errs) != 1 || errs[0].Code != "unsupported_in_overlay_mode" {
		t.Errorf("problem details.errors = %+v, want one unsupported_in_overlay_mode", errs)
	}
}

func TestAcknowledgeOrganizationViewRefusesAnOverlayWorkspace(t *testing.T) {
	h := NewHandlers(nil, overlayModeReturning(true, nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/x/view-ack", nil)

	h.AcknowledgeOrganizationView(rec, req, crmcontracts.Id(ids.NewV7()))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 — the mirror holds no visit marks either", rec.Code)
	}
}

// The nil service is the point: it proves the overlay gate runs BEFORE anything
// touches the database, so a mirror-backed workspace cannot write a dismissal
// against records this system of record does not own.
func TestDismissOrganizationSuggestionRefusesAnOverlayWorkspace(t *testing.T) {
	h := NewHandlers(nil, overlayModeReturning(true, nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/x/suggestions/dismiss",
		strings.NewReader(`{"fingerprint":"`+strings.Repeat("a", 64)+`"}`))
	req.Header.Set("Content-Type", "application/json")

	h.DismissOrganizationSuggestion(rec, req, crmcontracts.Id(ids.NewV7()))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 — the mirror raises no suggestions to dismiss", rec.Code)
	}
}

func TestGetOrganization360RefusesWhenTheModeCannotBeResolved(t *testing.T) {
	h := NewHandlers(nil, overlayModeReturning(false, errors.New("mode lookup failed")))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/x/360", nil)

	h.GetOrganization360(rec, req, crmcontracts.Id(ids.NewV7()))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d after a failed mode lookup, want 500 — the mode is unknown, so neither native data nor a tidy refusal is an honest answer",
			rec.Code)
	}
}
