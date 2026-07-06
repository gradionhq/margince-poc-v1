// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Lists & tags over HTTP: the organizational surfaces respect the same
// laws as everything else — member/tag references are reads of
// row-scoped records (H1 probe), duplicates answer 409, dynamic
// segments refuse manual members, archived tags read as absent.

import (
	"net/http"
	"testing"
)

func setupCollections(t *testing.T) (*env, string) {
	t.Helper()
	e := setup(t)
	e.slug = "collections-e2e"
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Collections E2E", "admin_email": "org@fable.test",
		"admin_display_name": "Organizer", "admin_password": "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap → %d", status)
	}
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "org@fable.test", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "List Target"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	return e, person.ID
}

func TestListsLifecycleAndMembership(t *testing.T) {
	e, personID := setupCollections(t)

	var list struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/lists", anyMap{
		"name": "Q3 Targets", "entity_type": "person",
	}, nil, &list); status != http.StatusCreated {
		t.Fatalf("create list → %d", status)
	}
	// A static list refuses a definition; a dynamic one demands it.
	if status := e.call(t, "POST", "/v1/lists", anyMap{
		"name": "Broken", "entity_type": "person", "list_type": "dynamic",
	}, nil, nil); status != 422 {
		t.Fatalf("definition-less dynamic list → %d, want 422", status)
	}

	if status := e.call(t, "POST", "/v1/lists/"+list.ID+"/members", anyMap{
		"entity_type": "person", "entity_id": personID,
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("add member → %d", status)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/lists/"+list.ID+"/members", anyMap{
		"entity_type": "person", "entity_id": personID,
	}, nil, &problem); status != http.StatusConflict {
		t.Fatalf("duplicate member → %d, want 409", status)
	}
	// A wrong-typed member is refused before any probe.
	if status := e.call(t, "POST", "/v1/lists/"+list.ID+"/members", anyMap{
		"entity_type": "deal", "entity_id": personID,
	}, nil, nil); status != 422 {
		t.Fatalf("type-mismatched member → %d, want 422", status)
	}
	// A member reference outside visibility is absent (H1).
	if status := e.call(t, "POST", "/v1/lists/"+list.ID+"/members", anyMap{
		"entity_type": "person", "entity_id": "00000000-0000-7000-8000-00000000dead",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("invisible member target → %d, want 404", status)
	}

	var members struct {
		Data []struct {
			EntityID string `json:"entity_id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/lists/"+list.ID+"/members", nil, nil, &members); status != http.StatusOK || len(members.Data) != 1 {
		t.Fatalf("list members → %d %+v", status, members)
	}

	if status := e.call(t, "DELETE", "/v1/lists/"+list.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive list → %d", status)
	}
	var lists struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/lists", nil, nil, &lists); status != http.StatusOK || len(lists.Data) != 0 {
		t.Fatalf("archived list still listed: %+v", lists)
	}
}

func TestTagsLifecycleAndApplication(t *testing.T) {
	e, personID := setupCollections(t)

	var tag struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/tags", anyMap{"name": "Champion", "color": "#ff6b00"}, nil, &tag); status != http.StatusCreated {
		t.Fatalf("create tag → %d", status)
	}
	// The name is unique case-insensitively.
	if status := e.call(t, "POST", "/v1/tags", anyMap{"name": "champion"}, nil, nil); status != http.StatusConflict {
		t.Fatalf("duplicate tag name → %d, want 409", status)
	}

	if status := e.call(t, "POST", "/v1/tags/"+tag.ID+"/apply", anyMap{
		"entity_type": "person", "entity_id": personID,
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("apply tag → %d", status)
	}
	if status := e.call(t, "POST", "/v1/tags/"+tag.ID+"/apply", anyMap{
		"entity_type": "person", "entity_id": personID,
	}, nil, nil); status != http.StatusConflict {
		t.Fatalf("re-apply → %d, want 409", status)
	}

	if status := e.call(t, "DELETE", "/v1/tags/"+tag.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive tag → %d", status)
	}
	// An archived tag reads as absent for new applications.
	if status := e.call(t, "POST", "/v1/tags/"+tag.ID+"/apply", anyMap{
		"entity_type": "person", "entity_id": personID,
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("apply on archived tag → %d, want 404", status)
	}
}
