// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The activity lifecycle beyond capture: task completion stamps
// done_at, stale If-Match refuses, archive hides from the default
// timeline, and relink is an idempotent, provenance-preserving
// association whose target passes the visibility probe.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// seedTaskAndTarget logs one task activity plus a person for it to be
// relinked onto, returning both ids.
func seedTaskAndTarget(t *testing.T, e *apptest.AppEnv) (personID, taskID string) {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{"full_name": "Task Target"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var task struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "task", "subject": "Send offer",
	}, nil, &task); status != http.StatusCreated {
		t.Fatalf("log task → %d", status)
	}
	return person.ID, task.ID
}

func TestActivityUpdateArchiveRelink(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "act-e2e"
	apptest.BootstrapWorkspaceSession(t, e, "Act E2E", "act@fable.test", "Admin")
	personID, taskID := seedTaskAndTarget(t, e)

	// Completing the task stamps done_at with it.
	var updated struct {
		IsDone bool    `json:"is_done"`
		DoneAt *string `json:"done_at"`
	}
	if status := e.Call(t, "PATCH", "/v1/activities/"+taskID, apptest.AnyMap{"is_done": true}, nil, &updated); status != http.StatusOK {
		t.Fatalf("complete task → %d", status)
	}
	if !updated.IsDone || updated.DoneAt == nil {
		t.Fatalf("completion did not stamp done_at: %+v", updated)
	}
	// A stale If-Match refuses.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.Call(t, "PATCH", "/v1/activities/"+taskID, apptest.AnyMap{"subject": "x"},
		map[string]string{"If-Match": "999"}, &problem); status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("stale If-Match → %d %q", status, problem.Code)
	}

	assertRelinkIdempotentAndVisibilityScoped(t, e, taskID, personID)

	// Archive is the soft flag (same semantics as every entity): the
	// record stays readable by id, stamped archived_at, and further
	// mutations refuse.
	if status := e.Call(t, "DELETE", "/v1/activities/"+taskID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive → %d", status)
	}
	var archived struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if status := e.Call(t, "GET", "/v1/activities/"+taskID, nil, nil, &archived); status != http.StatusOK || archived.ArchivedAt == nil {
		t.Fatalf("archive did not stamp: %d %+v", status, archived)
	}
	if status := e.Call(t, "PATCH", "/v1/activities/"+taskID, apptest.AnyMap{"subject": "zombie"}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("mutating an archived activity → %d, want 404", status)
	}
}

// assertRelinkIdempotentAndVisibilityScoped covers the relink arm:
// an idempotent association onto a visible person, replay-silent in the
// audit trail, with invisible targets (person and lead alike) reading
// as absent.
func assertRelinkIdempotentAndVisibilityScoped(t *testing.T, e *apptest.AppEnv, taskID, personID string) {
	t.Helper()
	// Relink: idempotent association onto a visible person.
	for i := 0; i < 2; i++ {
		if status := e.Call(t, "POST", "/v1/activities/"+taskID+"/relink", apptest.AnyMap{
			"entity_type": "person", "entity_id": personID,
		}, nil, nil); status != http.StatusOK {
			t.Fatalf("relink (round %d) → %d", i, status)
		}
	}
	var links int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM activity_link WHERE person_id = $1`, personID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("relink replay duplicated the link: %d rows", links)
	}
	// One relink audit row despite two calls (the replay is a no-op).
	var relinks int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'activity_relink'`).Scan(&relinks); err != nil {
		t.Fatal(err)
	}
	if relinks != 1 {
		t.Fatalf("relink audits = %d, want 1 (idempotent replay is silent)", relinks)
	}
	// An invisible relink target reads as absent (H1).
	if status := e.Call(t, "POST", "/v1/activities/"+taskID+"/relink", apptest.AnyMap{
		"entity_type": "person", "entity_id": "00000000-0000-7000-8000-00000000dead",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("invisible relink target → %d, want 404", status)
	}
	// The lead arm (0038): relinking onto a real lead lands on the lead's
	// timeline; a guessed lead id reads as absent like any other target.
	var lead apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/leads", apptest.AnyMap{
		"full_name": "Relink Lead", "email": "relink@lead.test", "source": "manual",
	}, nil, &lead); status != http.StatusCreated {
		t.Fatalf("create lead → %d", status)
	}
	if status := e.Call(t, "POST", "/v1/activities/"+taskID+"/relink", apptest.AnyMap{
		"entity_type": "lead", "entity_id": lead["id"],
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("lead relink → %d, want 200", status)
	}
	if status := e.Call(t, "POST", "/v1/activities/"+taskID+"/relink", apptest.AnyMap{
		"entity_type": "lead", "entity_id": "00000000-0000-7000-8000-00000000dead",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("guessed lead relink → %d, want 404", status)
	}
}
