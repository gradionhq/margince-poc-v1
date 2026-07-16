// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The RC-2 exclusion CRUD over the real wire (capture.md CAP-WIRE-2): a
// signed-in human lists, creates (idempotently), and deletes their own
// personal-mail rules through the mounted handler stack — session auth, the
// human-only agent policy, RFC 7807 validation, and the JSON shapes.

import (
	"net/http"
	"testing"
)

type exclusionRuleDTO struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func TestCaptureExclusionCRUDOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// Create — the value is normalized to lowercase on the way in.
	var created exclusionRuleDTO
	if status := e.call(t, "POST", "/v1/capture/exclusions",
		anyMap{"kind": "sender_domain", "value": "Personal-Family.Example"}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	if created.ID == "" || created.Kind != "sender_domain" || created.Value != "personal-family.example" {
		t.Fatalf("created rule wrong: %+v", created)
	}

	// Re-create the same rule → idempotent, same id, no duplicate.
	var again exclusionRuleDTO
	if status := e.call(t, "POST", "/v1/capture/exclusions",
		anyMap{"kind": "sender_domain", "value": "personal-family.example"}, nil, &again); status != http.StatusCreated {
		t.Fatalf("idempotent re-create status = %d", status)
	}
	if again.ID != created.ID {
		t.Fatalf("idempotent re-add minted a new id: %s != %s", again.ID, created.ID)
	}

	// A second, label rule.
	if status := e.call(t, "POST", "/v1/capture/exclusions",
		anyMap{"kind": "label", "value": "Personal"}, nil, nil); status != http.StatusCreated {
		t.Fatalf("label create status = %d", status)
	}

	// An out-of-vocabulary kind is a 422, never persisted.
	if status := e.call(t, "POST", "/v1/capture/exclusions",
		anyMap{"kind": "subject", "value": "x"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad-kind status = %d, want 422", status)
	}

	// List returns exactly the two real rules.
	var list struct {
		Data []exclusionRuleDTO `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/capture/exclusions", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if len(list.Data) != 2 {
		t.Fatalf("list = %+v, want 2 rules", list.Data)
	}

	// Delete is idempotent: the rule goes, a repeat is still 204.
	if status := e.call(t, "DELETE", "/v1/capture/exclusions/"+created.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("delete status = %d", status)
	}
	if status := e.call(t, "DELETE", "/v1/capture/exclusions/"+created.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("idempotent delete status = %d", status)
	}

	var after struct {
		Data []exclusionRuleDTO `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/capture/exclusions", nil, nil, &after); status != http.StatusOK || len(after.Data) != 1 {
		t.Fatalf("after delete: status=%d rules=%+v, want 200 with 1 rule", status, after.Data)
	}
}
