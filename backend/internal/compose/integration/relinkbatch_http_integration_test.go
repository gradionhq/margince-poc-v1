// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// relinkBatchDTO is the wire shape both batch relinks answer.
type relinkBatchDTO struct {
	Relinked int `json:"relinked"`
}

// A whole conversation moves over HTTP: the route exists, the body decodes,
// the answer counts, and a member that is archived is left behind.
func TestRelinkingAThreadOverHTTP(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	org := anchorOrg(t, e, "Stark Industries")
	var project projectDTO
	if status := e.Call(t, "POST", "/v1/projects", apptest.AnyMap{
		"name": "Arc reactor", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	key := "thread:" + ids.NewV7().String()
	var members []string
	for range 3 {
		var activity struct {
			ID string `json:"id"`
		}
		if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
			"kind": "note", "source": "manual", "body": "part of one conversation",
		}, nil, &activity); status != http.StatusCreated {
			t.Fatalf("POST /activities → %d, want 201", status)
		}
		members = append(members, activity.ID)
	}
	// The thread key is capture's to stamp; no write endpoint sets it, so the
	// seed reaches the column directly.
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE activity SET thread_key = $1 WHERE id = ANY($2::uuid[])`, key, members); err != nil {
		t.Fatalf("stamping the thread key: %v", err)
	}
	if status := e.Call(t, "DELETE", "/v1/activities/"+members[2], nil, nil, nil); status != http.StatusOK {
		t.Fatalf("DELETE /activities/{id} → %d, want 200", status)
	}

	var out relinkBatchDTO
	if status := e.Call(t, "POST", "/v1/activities/relink-thread", apptest.AnyMap{
		"thread_key": key, "entity_type": "project", "entity_id": project.ID,
	}, nil, &out); status != http.StatusOK {
		t.Fatalf("POST /activities/relink-thread → %d, want 200", status)
	}
	if out.Relinked != 2 {
		t.Errorf("relinked = %d, want the two live members and not the archived one", out.Relinked)
	}

	var problem projectProblem
	if status := e.Call(t, "POST", "/v1/activities/relink-thread", apptest.AnyMap{
		"thread_key": key, "entity_type": "invoice", "entity_id": project.ID,
	}, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Errorf("a destination outside the link vocabulary → %d, want 422", status)
	}

	var bulk relinkBatchDTO
	if status := e.Call(t, "POST", "/v1/activities/relink-bulk", apptest.AnyMap{
		"activity_ids": members[:2], "entity_type": "organization", "entity_id": org,
	}, nil, &bulk); status != http.StatusOK {
		t.Fatalf("POST /activities/relink-bulk → %d, want 200", status)
	}
	if bulk.Relinked != 2 {
		t.Errorf("bulk relinked = %d, want 2", bulk.Relinked)
	}
	if status := e.Call(t, "POST", "/v1/activities/relink-bulk", apptest.AnyMap{
		"activity_ids": []string{ids.NewV7().String()}, "entity_type": "organization", "entity_id": org,
	}, nil, &problem); status != http.StatusNotFound {
		t.Errorf("a named id that does not exist → %d, want 404", status)
	}
}
