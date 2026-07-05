// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The human-edit-precedence gate (interfaces.md §2.1, B-EP06.14) on the
// governed REST twin: an agent's field patch is 🟢 while it keeps clear
// of human-typed values, resolves 🟡 the moment it would overwrite one,
// and only a human decision releases the overwrite.

import (
	"testing"
)

func TestEndToEnd_humanEditPrecedenceOnAgentUpdate(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A HUMAN types the person's name — full_name is now human-owned per
	// the audit trail.
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Greta Human"}, nil, &person); status != 201 {
		t.Fatalf("human create → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "precedence agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// A field no human ever wrote updates 🟢 — the reversible-and-logged
	// path stays open.
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"title": "CTO"}, bearer, nil); status != 200 {
		t.Fatalf("agent patch of a never-human field → %d, want 200 (🟢)", status)
	}
	// A field the AGENT last wrote stays 🟢 too — precedence protects
	// people, not machines.
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"title": "VP Engineering"}, bearer, nil); status != 200 {
		t.Fatalf("agent re-patch of its own field → %d, want 200 (🟢)", status)
	}

	// Overwriting the human-typed name resolves 🟡: staged, not applied.
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"full_name": "Greta Machine"}, bearer, &problem)
	if status != 403 || problem.Code != "approval_required" {
		t.Fatalf("agent overwrite of a human field → %d %q, want 403 approval_required", status, problem.Code)
	}
	var current struct {
		FullName string `json:"full_name"`
	}
	if status := e.call(t, "GET", "/v1/people/"+person.ID, nil, bearer, &current); status != 200 || current.FullName != "Greta Human" {
		t.Fatalf("staged overwrite must not have executed: %d %q", status, current.FullName)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// A human decision releases it; the identical retry lands the patch.
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != 200 {
		t.Fatalf("human approve → %d", status)
	}
	withToken := map[string]string{"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID}
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"full_name": "Greta Machine"}, withToken, nil); status != 200 {
		t.Fatalf("approved retry → %d, want the patch to execute", status)
	}
	if status := e.call(t, "GET", "/v1/people/"+person.ID, nil, bearer, &current); status != 200 || current.FullName != "Greta Machine" {
		t.Fatalf("approved overwrite did not land: %d %q", status, current.FullName)
	}
}
