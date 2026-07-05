// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The activity lifecycle beyond capture: task completion stamps
// done_at, stale If-Match refuses, archive hides from the default
// timeline, and relink is an idempotent, provenance-preserving
// association whose target passes the visibility probe.

import (
	"net/http"
	"testing"
)

// bootstrapWorkspaceSession provisions a workspace on the env's slug and
// leaves its admin session authenticated — the arrange step every
// single-workspace e2e scenario shares.
func bootstrapWorkspaceSession(t *testing.T, e *env, workspaceName, adminEmail string) {
	t.Helper()
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": workspaceName, "admin_email": adminEmail,
		"admin_display_name": "Admin", "admin_password": "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap → %d", status)
	}
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": adminEmail, "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}
}

// seedTaskAndTarget logs one task activity plus a person for it to be
// relinked onto, returning both ids.
func seedTaskAndTarget(t *testing.T, e *env) (personID, taskID string) {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Task Target"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var task struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "task", "subject": "Send offer",
	}, nil, &task); status != http.StatusCreated {
		t.Fatalf("log task → %d", status)
	}
	return person.ID, task.ID
}

func TestActivityUpdateArchiveRelink(t *testing.T) {
	e := setup(t)
	e.slug = "act-e2e"
	bootstrapWorkspaceSession(t, e, "Act E2E", "act@fable.test")
	personID, taskID := seedTaskAndTarget(t, e)

	// Completing the task stamps done_at with it.
	var updated struct {
		IsDone bool    `json:"is_done"`
		DoneAt *string `json:"done_at"`
	}
	if status := e.call(t, "PATCH", "/v1/activities/"+taskID, anyMap{"is_done": true}, nil, &updated); status != http.StatusOK {
		t.Fatalf("complete task → %d", status)
	}
	if !updated.IsDone || updated.DoneAt == nil {
		t.Fatalf("completion did not stamp done_at: %+v", updated)
	}
	// A stale If-Match refuses.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "PATCH", "/v1/activities/"+taskID, anyMap{"subject": "x"},
		map[string]string{"If-Match": "999"}, &problem); status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("stale If-Match → %d %q", status, problem.Code)
	}

	// Relink: idempotent association onto a visible person.
	for i := 0; i < 2; i++ {
		if status := e.call(t, "POST", "/v1/activities/"+taskID+"/relink", anyMap{
			"entity_type": "person", "entity_id": personID,
		}, nil, nil); status != http.StatusOK {
			t.Fatalf("relink (round %d) → %d", i, status)
		}
	}
	var links int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM activity_link WHERE person_id = $1`, personID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("relink replay duplicated the link: %d rows", links)
	}
	// One relink audit row despite two calls (the replay is a no-op).
	var relinks int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'activity_relink'`).Scan(&relinks); err != nil {
		t.Fatal(err)
	}
	if relinks != 1 {
		t.Fatalf("relink audits = %d, want 1 (idempotent replay is silent)", relinks)
	}
	// An invisible relink target reads as absent (H1).
	if status := e.call(t, "POST", "/v1/activities/"+taskID+"/relink", anyMap{
		"entity_type": "person", "entity_id": "00000000-0000-7000-8000-00000000dead",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("invisible relink target → %d, want 404", status)
	}
	// The contract admits lead here; the activity_link schema does not —
	// refused as validation, not silently dropped.
	if status := e.call(t, "POST", "/v1/activities/"+taskID+"/relink", anyMap{
		"entity_type": "lead", "entity_id": personID,
	}, nil, nil); status != 422 {
		t.Fatalf("lead relink → %d, want 422", status)
	}

	// Archive is the soft flag (same semantics as every entity): the
	// record stays readable by id, stamped archived_at, and further
	// mutations refuse.
	if status := e.call(t, "DELETE", "/v1/activities/"+taskID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive → %d", status)
	}
	var archived struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if status := e.call(t, "GET", "/v1/activities/"+taskID, nil, nil, &archived); status != http.StatusOK || archived.ArchivedAt == nil {
		t.Fatalf("archive did not stamp: %d %+v", status, archived)
	}
	if status := e.call(t, "PATCH", "/v1/activities/"+taskID, anyMap{"subject": "zombie"}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("mutating an archived activity → %d, want 404", status)
	}
}
