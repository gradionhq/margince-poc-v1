// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What a human approves must bind to the record they were shown.
//
// The version pin is taken server-side for every version-pinnable record
// type (approvals.versionTables), never from the caller's own If-Match
// header — a header the contract declares optional, so an agent staging
// without one must still pin to the row's current state rather than opt
// out of the binding. `offer` is the sharp case: DELETE /v1/offers/{id}
// (the archive_record 🟡 tool's REST twin) carries no body, so its
// diff_hash is a CONSTANT for that offer id, while the line-item routes
// underneath it are auto_execute — the agent could rewrite the priced
// terms between the human's approval and the redemption and archive (or,
// with a different verb, ship) the offer at its own number instead of the
// one the human was shown.
//
// This drives that exact sequence over real HTTP: stage, approve, rewrite
// underneath, redeem — the redemption must refuse on the version mismatch.

import (
	"net/http"
	"testing"
)

func TestApprovedOfferArchiveRefusesAfterTheAgentRewritesTheTerms(t *testing.T) {
	e := setup(t)
	e.slug = "offers-pin"
	bootstrapWorkspaceSession(t, e, "Offers Pin", "pin@fable.test", "Admin")
	dealID := offerFixture(t, e)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		// The version-pin race this test proves happens inside archive_record's
		// approval, so the passport must be able to spend `write` to get there.
		"label": "pin agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	var offer struct {
		ID        string `json:"id"`
		Version   int64  `json:"version"`
		LineItems []struct {
			ID string `json:"id"`
		} `json:"line_items"`
	}
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "mcp",
		"line_items": []anyMap{{"description": "Pilot", "quantity": 1, "unit_price_minor": 250000, "tax_rate": 19.0}},
	}, bearer, &offer); status != http.StatusCreated {
		t.Fatalf("agent offer draft → %d", status)
	}
	if len(offer.LineItems) != 1 {
		t.Fatalf("draft carries %d line items, want 1", len(offer.LineItems))
	}

	// Stage the archive WITHOUT If-Match — the omission that would defeat
	// the pin entirely if it were taken from the caller instead of the row.
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.call(t, "DELETE", "/v1/offers/"+offer.ID, nil, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent archive → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The staged row carries a pin the agent never supplied.
	var pin *int64
	if err := e.owner.QueryRow(t.Context(),
		`SELECT target_version FROM approval WHERE id = $1`, approvalID).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin == nil {
		t.Fatal("the staged approval carries no target_version — omitting If-Match must not opt out of the binding")
	}

	// The human approves the offer they were shown: Pilot at 250000 minor.
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d", status)
	}

	// The agent then rewrites the priced terms through the auto_execute
	// child route. The offer's version moves; the approval's does not.
	if status := e.call(t, "PATCH", "/v1/offers/"+offer.ID+"/line-items/"+offer.LineItems[0].ID, anyMap{
		"unit_price_minor": 100,
	}, bearer, nil); status != http.StatusOK {
		t.Fatalf("agent line-item rewrite → %d", status)
	}

	// The byte-identical retry now refuses: same path, same empty body, same
	// diff_hash — and a row that is no longer the one the human approved.
	withToken := map[string]string{
		"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID,
	}
	var refusal struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "DELETE", "/v1/offers/"+offer.ID, nil, withToken, &refusal); status != http.StatusConflict ||
		refusal.Code != "version_skew" {
		t.Fatalf("redeeming against rewritten terms → %d %q, want 409 version_skew", status, refusal.Code)
	}

	// And nothing was archived.
	var after offerBody
	if status := e.call(t, "GET", "/v1/offers/"+offer.ID, nil, bearer, &after); status != http.StatusOK || after.Status != "draft" {
		t.Fatalf("offer status = %q after a refused redemption, want draft", after.Status)
	}
}
