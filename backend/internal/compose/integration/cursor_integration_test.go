// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The invariant: a keyset cursor is client input, so a token that fails
// to decode answers a 4xx problem+json, never a 500 — on EVERY
// cursor-paginated list operation the contract exposes. The endpoint
// set below enumerates the contract's Cursor-parameter operations whose
// implementations parse the token (crm.yaml components.parameters.Cursor
// refs); /approvals also declares the parameter but its implementation
// does not paginate yet, so a garbage token there is ignored rather
// than parsed and has nothing to misclassify.

import (
	"net/http"
	"testing"
)

func TestMalformedCursorAnswers4xxEverywhere(t *testing.T) {
	e := setup(t)

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Cursor Probe",
		"admin_email":        "admin@cursor.test",
		"admin_display_name": "Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap = %d", status)
	}
	e.slug = "cursor-probe"

	// /lists/{id}/members needs a real list to point at.
	var list anyMap
	if status := e.call(t, "POST", "/v1/lists", anyMap{
		"name": "Probe", "entity_type": "person",
	}, nil, &list); status != http.StatusCreated {
		t.Fatalf("create list = %d %v", status, list)
	}

	// Not valid base64url, so every decoder rejects it.
	const garbage = "cursor=%21%21garbage%21%21"
	endpoints := []string{
		"/v1/people?" + garbage,
		"/v1/organizations?" + garbage,
		"/v1/partners?" + garbage,
		"/v1/deals?" + garbage,
		"/v1/activities?" + garbage,
		"/v1/leads?" + garbage,
		"/v1/relationships?" + garbage,
		"/v1/lists/" + list["id"].(string) + "/members?" + garbage,
		"/v1/search?q=probe&" + garbage,
	}
	for _, path := range endpoints {
		var problem anyMap
		status := e.call(t, "GET", path, nil, nil, &problem)
		if status < 400 || status > 499 {
			t.Errorf("GET %s with a malformed cursor = %d %v, want a 4xx problem", path, status, problem)
		}
	}
}
