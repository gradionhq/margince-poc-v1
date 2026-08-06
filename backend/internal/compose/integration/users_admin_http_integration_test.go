// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The admin user-management HTTP surface end to end (POST /users invite,
// PATCH /users/{id}/role, POST /users/{id}/deactivate|reactivate, and the
// include_inactive roster widening) as the bootstrap admin.

import (
	"net/http"
	"strings"
	"testing"
)

type userWire struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	// A pointer so an absent field stays distinguishable from an empty one:
	// absent means the caller was not admitted to the role keys, empty means
	// the member holds none.
	Roles *[]string `json:"roles"`
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
	assertRoles(t, "invite", invited, "rep")
	base := "/v1/users/" + invited.ID

	// A duplicate email refuses, and says so in words.
	var dupe refusalWire
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "newbie@acme.test", "display_name": "Dupe", "role": "rep",
	}, nil, &dupe); status != http.StatusConflict {
		t.Fatalf("duplicate invite -> %d, want 409", status)
	}
	assertActionableRefusal(t, "duplicate invite", dupe)

	// The same unknown-role refusal as change-role: the `role` enum is
	// documentation, not binding validation, so a mistyped key reaches the
	// server here too and must not read as "no such member".
	var badRole refusalWire
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "badrole@acme.test", "display_name": "Bad Role", "role": "no-such-role",
	}, nil, &badRole); status != http.StatusNotFound {
		t.Fatalf("invite with an undefined role -> %d, want 404", status)
	}
	if badRole.Code != "unknown_role" {
		t.Errorf("invite with an undefined role: code = %q, want unknown_role", badRole.Code)
	}

	// Malformed input is refused before any member is created.
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "not-an-email", "display_name": "X", "role": "rep",
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("invite with a malformed email -> %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/users", map[string]any{
		"email": "blank@acme.test", "display_name": "   ", "role": "rep",
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("invite with a blank display name -> %d, want 422", status)
	}
	if status := e.call(t, "POST", base+"/deactivate", map[string]any{
		"reason": strings.Repeat("x", 501),
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("deactivate with an over-long reason -> %d, want 422", status)
	}

	// The active-only roster shows the invited member.
	var roster userListWire
	if status := e.call(t, "GET", "/v1/users", nil, nil, &roster); status != http.StatusOK {
		t.Fatalf("list users -> %d, want 200", status)
	}
	if !containsUser(roster.Data, invited.ID) {
		t.Fatalf("active roster missing the invited member %s", invited.ID)
	}
	// An admin's roster carries every member's role keys — the admin card reads
	// the current role off them.
	for _, u := range roster.Data {
		if u.Roles == nil {
			t.Errorf("roster member %q has no roles field; an admin caller must see it", u.Email)
		}
	}

	// Change role. The response reports the role now held, not the one replaced.
	var afterRole userWire
	if status := e.call(t, "PATCH", base+"/role", map[string]any{"role": "manager"}, nil, &afterRole); status != http.StatusOK {
		t.Fatalf("change role -> %d, want 200", status)
	}
	assertRoles(t, "change role", afterRole, "manager")

	// A role nobody defines is a 404, like a missing member — but a DIFFERENT
	// one, and the code is what lets a client tell an admin which mistake they
	// made instead of sending them to look for a member who is right there.
	var unknownRole refusalWire
	if status := e.call(t, "PATCH", base+"/role", map[string]any{"role": "no-such-role"}, nil, &unknownRole); status != http.StatusNotFound {
		t.Fatalf("change role to an undefined role -> %d, want 404", status)
	}
	if unknownRole.Code != "unknown_role" {
		t.Errorf("undefined role code = %q, want unknown_role — a bare not_found sends the admin hunting for the member", unknownRole.Code)
	}
	assertActionableRefusal(t, "an undefined role", unknownRole)

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
	// include_inactive AND q together: the only combination that reaches the
	// widened+filtered query, and the one whose bind numbering is longest.
	// Without this the suite executes that string nowhere.
	var withInactiveFiltered userListWire
	if status := e.call(t, "GET", "/v1/users?include_inactive=true&q=NEWBIE", nil, nil, &withInactiveFiltered); status != http.StatusOK {
		t.Fatalf("include_inactive + q -> %d, want 200", status)
	}
	if len(withInactiveFiltered.Data) != 1 || withInactiveFiltered.Data[0].ID != invited.ID {
		t.Fatalf("include_inactive + q = %+v, want only the deactivated member", withInactiveFiltered.Data)
	}
	if withInactiveFiltered.Data[0].Roles == nil {
		t.Error("include_inactive + q dropped the role keys an admin is owed")
	}

	// Reactivate.
	var afterOn userWire
	if status := e.call(t, "POST", base+"/reactivate", nil, nil, &afterOn); status != http.StatusOK {
		t.Fatalf("reactivate -> %d, want 200", status)
	}
	if afterOn.Status != "active" {
		t.Fatalf("reactivated member status = %q, want active", afterOn.Status)
	}

	// A SUSPENDED member is not merely deactivated — the hold was placed for a
	// different reason (e.g. lockout), so reactivating would quietly clear it.
	// The refusal has to explain that, or an admin reads it as a broken button.
	seedInWorkspace(t, e, wsID(t, e, e.slug),
		stmt(`UPDATE app_user SET status = 'suspended' WHERE id = $1::uuid`, invited.ID))
	var suspended refusalWire
	if status := e.call(t, "POST", base+"/reactivate", nil, nil, &suspended); status != http.StatusConflict {
		t.Fatalf("reactivating a suspended member -> %d, want 409", status)
	}
	assertActionableRefusal(t, "reactivating a suspended member", suspended)

	// The bootstrap admin is the only admin (the invited member holds manager
	// by now): neither deactivating nor demoting them is allowed — it would
	// lock the organization out of user administration entirely.
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("GET /me -> %d, want 200", status)
	}
	var offLast, demoteLast refusalWire
	if status := e.call(t, "POST", "/v1/users/"+me.User.ID+"/deactivate", nil, nil, &offLast); status != http.StatusConflict {
		t.Fatalf("deactivating the last admin -> %d, want 409", status)
	}
	assertActionableRefusal(t, "deactivating the last admin", offLast)
	if status := e.call(t, "PATCH", "/v1/users/"+me.User.ID+"/role", map[string]any{"role": "rep"}, nil, &demoteLast); status != http.StatusConflict {
		t.Fatalf("demoting the last admin -> %d, want 409", status)
	}
	assertActionableRefusal(t, "demoting the last admin", demoteLast)
}

type refusalWire struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// assertActionableRefusal holds this surface's 409s to the bar the repo sets for
// every error: say what went wrong AND what to do. The refusals here reached the
// operator as the bare word "conflict" — a status slug rendered as if it were a
// message — so the check is that a detail exists, reads as a sentence, and is
// not just the code again.
func assertActionableRefusal(t *testing.T, what string, got refusalWire) {
	t.Helper()
	if got.Detail == "" {
		t.Errorf("%s: refusal carries no detail; the operator is shown only %q", what, got.Code)
		return
	}
	if strings.EqualFold(strings.TrimSpace(got.Detail), got.Code) {
		t.Errorf("%s: detail = %q, which is just the code — it names neither the cause nor the fix", what, got.Detail)
	}
	if len(strings.Fields(got.Detail)) < 5 {
		t.Errorf("%s: detail = %q is too terse to tell an admin what to do next", what, got.Detail)
	}
}

// assertRoles checks the member response reports exactly the role keys the
// admin surface renders a current role from.
func assertRoles(t *testing.T, what string, got userWire, want ...string) {
	t.Helper()
	if got.Roles == nil {
		t.Fatalf("%s: roles absent from an admin's response; want %v", what, want)
	}
	if len(*got.Roles) != len(want) {
		t.Fatalf("%s: roles = %v, want %v", what, *got.Roles, want)
	}
	for i, key := range want {
		if (*got.Roles)[i] != key {
			t.Errorf("%s: roles = %v, want %v", what, *got.Roles, want)
			return
		}
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
