// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The automations surface (B-E15.1/.4, feedback/14): a closed catalog,
// instance CRUD that validates params against it, created-paused,
// If-Match on the enable flip, soft delete — and the workspace
// bootstrap seeding the two starters enabled.

import (
	"net/http"
	"testing"
)

func TestAutomationCatalogAndCRUD(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// The catalog is the closed starter library.
	var catalog struct {
		Data []struct {
			Key          string         `json:"key"`
			Tier         string         `json:"tier"`
			ParamsSchema map[string]any `json:"params_schema"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/automations/catalog", nil, nil, &catalog); status != http.StatusOK {
		t.Fatalf("catalog → %d", status)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog carries %d types, want the closed set of 2", len(catalog.Data))
	}
	for _, entry := range catalog.Data {
		if entry.Tier != "green" {
			t.Fatalf("starter %s tier = %q, want green", entry.Key, entry.Tier)
		}
		if entry.ParamsSchema == nil {
			t.Fatalf("starter %s ships no params_schema — the editor form has nothing to render", entry.Key)
		}
	}

	// Bootstrap seeded the two starters ENABLED (system floor, recorded
	// deviation from created-paused which governs the API path).
	var listed struct {
		Data []struct {
			ID      string `json:"id"`
			Key     string `json:"key"`
			Status  string `json:"status"`
			Version int    `json:"version"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/automations", nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("list → %d", status)
	}
	if len(listed.Data) != 2 {
		t.Fatalf("bootstrap seeded %d automations, want 2", len(listed.Data))
	}
	for _, a := range listed.Data {
		if a.Status != "enabled" {
			t.Fatalf("seeded %s is %s, want enabled", a.Key, a.Status)
		}
	}

	// An unknown catalog key and out-of-schema params are 422s.
	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "invented_type", "name": "Nope", "params": anyMap{},
	}, nil, nil); status != 422 {
		t.Fatalf("unknown key → %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "route_lead", "name": "Bad params", "params": anyMap{"cap_per_owner": "soon"},
	}, nil, nil); status != 422 {
		t.Fatalf("mistyped param → %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "route_lead", "name": "Rogue knob", "params": anyMap{"rule_body": "if x then y"},
	}, nil, nil); status != 422 {
		t.Fatalf("out-of-schema param → %d, want 422 (the anti-DSL guard)", status)
	}

	// A valid create lands PAUSED and round-trips.
	var created struct {
		ID      string         `json:"id"`
		Status  string         `json:"status"`
		Params  map[string]any `json:"params"`
		Version int            `json:"version"`
	}
	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "route_lead", "name": "Slow-lane routing",
		"params": anyMap{"owners": []string{"0198c0de-0000-7000-8000-000000000001"}, "cap_per_owner": 3},
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create → %d", status)
	}
	if created.Status != "paused" {
		t.Fatalf("created status = %s, want paused (enabling is a second deliberate step)", created.Status)
	}
	var fetched struct {
		Name   string         `json:"name"`
		Params map[string]any `json:"params"`
	}
	if status := e.call(t, "GET", "/v1/automations/"+created.ID, nil, nil, &fetched); status != http.StatusOK {
		t.Fatalf("get → %d", status)
	}
	if fetched.Name != "Slow-lane routing" || fetched.Params["cap_per_owner"] != float64(3) {
		t.Fatalf("round-trip lost the config: %+v", fetched)
	}

	// Enable under a stale If-Match is refused; the current version wins.
	stale := map[string]string{"If-Match": "99"}
	if status := e.call(t, "PATCH", "/v1/automations/"+created.ID, anyMap{"status": "enabled"}, stale, nil); status != http.StatusConflict {
		t.Fatalf("stale If-Match → %d, want 409 version_skew", status)
	}
	var updated struct {
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	if status := e.call(t, "PATCH", "/v1/automations/"+created.ID, anyMap{"status": "enabled"},
		map[string]string{"If-Match": "1"}, &updated); status != http.StatusOK {
		t.Fatalf("enable → %d", status)
	}
	if updated.Status != "enabled" || updated.Version != 2 {
		t.Fatalf("enable landed as %+v, want enabled v2", updated)
	}

	// Delete is a soft archive: 204, then the instance reads as absent.
	if status := e.call(t, "DELETE", "/v1/automations/"+created.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("delete → %d", status)
	}
	if status := e.call(t, "GET", "/v1/automations/"+created.ID, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("get after delete → %d, want 404", status)
	}

	// Config is an audited fact end to end.
	var audit struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/audit-log?entity_type=automation", nil, nil, &audit); status != http.StatusOK {
		t.Fatalf("audit read → %d", status)
	}
	if len(audit.Data) != 3 {
		t.Fatalf("automation config audited %d times, want 3 (create, enable, archive)", len(audit.Data))
	}
}

// An agent passport cannot touch automation config: standing automation
// authority stays human (the ADR-0055 human-only class).
func TestAutomationConfigRejectsAgents(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "automation prober", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("mint → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "route_lead", "name": "Agent-made", "params": anyMap{},
	}, bearer, nil); status != http.StatusForbidden {
		t.Fatalf("agent create automation → %d, want 403", status)
	}
}
