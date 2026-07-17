// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The activity lifecycle beyond capture: task completion stamps
// done_at, stale If-Match refuses, archive hides from the default
// timeline, and relink is an idempotent, provenance-preserving
// association whose target passes the visibility probe.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// bootstrapWorkspaceSession provisions the installation's organization
// through the A107 boot path (configuration-driven, exactly what cmd/api
// runs at startup) and signs its admin in over HTTP — the arrange step
// every e2e scenario shares. The login also primes the server's
// singleton-organization resolution before a test seeds any
// cross-tenant rows directly.
func bootstrapWorkspaceSession(t *testing.T, e *env, organizationName, adminEmail, adminName string) {
	t.Helper()
	pwFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(pwFile, []byte("correct-horse-battery"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := deployconfig.Config{
		Version:      1,
		Organization: deployconfig.Organization{Name: organizationName},
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: adminEmail, DisplayName: adminName, PasswordFile: pwFile,
		},
	}
	if err := compose.EnsureInstallation(context.Background(), e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
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
	bootstrapWorkspaceSession(t, e, "Act E2E", "act@fable.test", "Admin")
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

	assertRelinkIdempotentAndVisibilityScoped(t, e, taskID, personID)

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

// assertRelinkIdempotentAndVisibilityScoped covers the relink arm:
// an idempotent association onto a visible person, replay-silent in the
// audit trail, with invisible targets (person and lead alike) reading
// as absent.
func assertRelinkIdempotentAndVisibilityScoped(t *testing.T, e *env, taskID, personID string) {
	t.Helper()
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
	// The lead arm (0038): relinking onto a real lead lands on the lead's
	// timeline; a guessed lead id reads as absent like any other target.
	var lead anyMap
	if status := e.call(t, "POST", "/v1/leads", anyMap{
		"full_name": "Relink Lead", "email": "relink@lead.test", "source": "manual",
	}, nil, &lead); status != http.StatusCreated {
		t.Fatalf("create lead → %d", status)
	}
	if status := e.call(t, "POST", "/v1/activities/"+taskID+"/relink", anyMap{
		"entity_type": "lead", "entity_id": lead["id"],
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("lead relink → %d, want 200", status)
	}
	if status := e.call(t, "POST", "/v1/activities/"+taskID+"/relink", anyMap{
		"entity_type": "lead", "entity_id": "00000000-0000-7000-8000-00000000dead",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("guessed lead relink → %d, want 404", status)
	}
}
