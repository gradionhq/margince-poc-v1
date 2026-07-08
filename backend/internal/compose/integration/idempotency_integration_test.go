// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The contract's Idempotency-Key promise, end to end: a keyed POST
// replayed with the identical body returns the ORIGINAL status and body
// and creates exactly one domain row; the same key with a different
// body is refused (409 idempotency_key_conflict, per the parameter's
// contract description) instead of silently replaying mismatched
// intent; and the transport key takes precedence over the natural
// (source_system, source_id) dedupe on logActivity — the replay never
// reaches the store.

import (
	"net/http"
	"reflect"
	"testing"
)

func TestIdempotencyKeyReplay(t *testing.T) {
	e := setup(t)

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Idem Probe",
		"admin_email":        "admin@idem.test",
		"admin_display_name": "Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap = %d", status)
	}
	e.slug = "idem-probe"

	keyed := map[string]string{"Idempotency-Key": "lead-retry-1"}
	leadReq := anyMap{
		"full_name":    "Retry Prospect",
		"email":        "retry@example.org",
		"company_name": "Retry AG",
		"source":       "import:idem",
	}

	var first anyMap
	if status := e.call(t, "POST", "/v1/leads", leadReq, keyed, &first); status != http.StatusCreated {
		t.Fatalf("keyed create lead = %d %v", status, first)
	}

	// The replay is byte-identical: same status, same body — NOT the 409
	// the natural email dedupe would answer if the request re-executed.
	var replay anyMap
	if status := e.call(t, "POST", "/v1/leads", leadReq, keyed, &replay); status != http.StatusCreated {
		t.Fatalf("keyed replay = %d %v, want the original 201", status, replay)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Errorf("replayed response differs from the original:\n first: %v\nreplay: %v", first, replay)
	}

	// Exactly one lead landed.
	var leads struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/leads", nil, nil, &leads); status != http.StatusOK {
		t.Fatalf("list leads = %d", status)
	}
	if len(leads.Data) != 1 {
		t.Fatalf("replayed create produced %d leads, want exactly 1", len(leads.Data))
	}

	// The same key with a DIFFERENT body is a conflict, never a replay.
	var problem anyMap
	status := e.call(t, "POST", "/v1/leads", anyMap{
		"full_name": "Different Intent",
		"email":     "other@example.org",
		"source":    "import:idem",
	}, keyed, &problem)
	if status != http.StatusConflict || problem["code"] != "idempotency_key_conflict" {
		t.Fatalf("mismatched body under a reused key = %d %v, want 409 idempotency_key_conflict", status, problem)
	}
}

func TestIdempotencyKeyReplay_logActivity(t *testing.T) {
	e := setup(t)

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Idem Activity",
		"admin_email":        "admin@idem-act.test",
		"admin_display_name": "Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap = %d", status)
	}
	e.slug = "idem-activity"

	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Idem Person", "source": "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}

	keyed := map[string]string{"Idempotency-Key": "act-retry-1"}
	logReq := anyMap{
		"kind":    "note",
		"subject": "Keyed note",
		"source":  "ui",
		"links":   []anyMap{{"entity_type": "person", "entity_id": person["id"]}},
	}

	var first, replay anyMap
	if status := e.call(t, "POST", "/v1/activities", logReq, keyed, &first); status != http.StatusCreated {
		t.Fatalf("keyed log activity = %d %v", status, first)
	}
	if status := e.call(t, "POST", "/v1/activities", logReq, keyed, &replay); status != http.StatusCreated {
		t.Fatalf("keyed activity replay = %d %v, want the original 201", status, replay)
	}
	if first["id"] != replay["id"] {
		t.Errorf("replay returned a different activity: %v vs %v", first["id"], replay["id"])
	}

	var activities struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/activities", nil, nil, &activities); status != http.StatusOK {
		t.Fatalf("list activities = %d", status)
	}
	if len(activities.Data) != 1 {
		t.Fatalf("replayed log produced %d activities, want exactly 1", len(activities.Data))
	}
}
