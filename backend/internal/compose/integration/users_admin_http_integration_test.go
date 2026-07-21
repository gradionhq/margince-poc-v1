// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The admin user-management HTTP surface end to end (POST /users invite,
// PATCH /users/{id}/role, POST /users/{id}/deactivate|reactivate, and the
// include_inactive roster widening) as the bootstrap admin.

import (
	"net/http"
	"testing"
)

type userWire struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

type userListWire struct {
	Data []userWire `json:"data"`
}

func TestAdminUserManagementOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// Invite a member.
	var invited userWire
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "Newbie@Acme.test", "display_name": "New Bie", "role": "rep",
	}, nil, &invited); status != http.StatusCreated {
		t.Fatalf("invite -> %d, want 201", status)
	}
	if invited.ID == "" || invited.Email != "newbie@acme.test" || invited.Status != "active" {
		t.Fatalf("invited member = %+v, want active, lowercased email", invited)
	}
	base := "/v1/users/" + invited.ID

	// A duplicate email refuses.
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "newbie@acme.test", "display_name": "Dupe", "role": "rep",
	}, nil, nil); status != http.StatusConflict {
		t.Fatalf("duplicate invite -> %d, want 409", status)
	}

	// The active-only roster shows the invited member.
	var roster userListWire
	if status := e.call(t, "GET", "/v1/users", nil, nil, &roster); status != http.StatusOK {
		t.Fatalf("list users -> %d, want 200", status)
	}
	if !containsUser(roster.Data, invited.ID) {
		t.Fatalf("active roster missing the invited member %s", invited.ID)
	}

	// Change role.
	var afterRole userWire
	if status := e.call(t, "PATCH", base+"/role", map[string]any{"role": "manager"}, nil, &afterRole); status != http.StatusOK {
		t.Fatalf("change role -> %d, want 200", status)
	}

	// Deactivate: the member drops from the active roster but is visible with include_inactive.
	var afterOff userWire
	if status := e.call(t, "POST", base+"/deactivate", nil, nil, &afterOff); status != http.StatusOK {
		t.Fatalf("deactivate -> %d, want 200", status)
	}
	if afterOff.Status != "deactivated" {
		t.Fatalf("deactivated member status = %q, want deactivated", afterOff.Status)
	}
	var activeOnly userListWire
	e.call(t, "GET", "/v1/users", nil, nil, &activeOnly)
	if containsUser(activeOnly.Data, invited.ID) {
		t.Fatalf("active-only roster still lists the deactivated member %s", invited.ID)
	}
	var withInactive userListWire
	e.call(t, "GET", "/v1/users?include_inactive=true", nil, nil, &withInactive)
	if !containsUser(withInactive.Data, invited.ID) {
		t.Fatalf("include_inactive roster missing the deactivated member %s", invited.ID)
	}

	// Reactivate.
	var afterOn userWire
	if status := e.call(t, "POST", base+"/reactivate", nil, nil, &afterOn); status != http.StatusOK {
		t.Fatalf("reactivate -> %d, want 200", status)
	}
	if afterOn.Status != "active" {
		t.Fatalf("reactivated member status = %q, want active", afterOn.Status)
	}

	// The bootstrap admin is the only admin (the invited member is a rep):
	// neither deactivating nor demoting them is allowed — it would lock the org.
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("GET /me -> %d, want 200", status)
	}
	if status := e.call(t, "POST", "/v1/users/"+me.User.ID+"/deactivate", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("deactivating the last admin -> %d, want 409", status)
	}
	if status := e.call(t, "PATCH", "/v1/users/"+me.User.ID+"/role", map[string]any{"role": "rep"}, nil, nil); status != http.StatusConflict {
		t.Fatalf("demoting the last admin -> %d, want 409", status)
	}
}

func containsUser(users []userWire, id string) bool {
	for _, u := range users {
		if u.ID == id {
			return true
		}
	}
	return false
}
