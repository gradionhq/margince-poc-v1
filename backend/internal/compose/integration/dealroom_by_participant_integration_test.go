// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// "Which rooms is this contact still in" is answered by address: a live seat
// counts, a revoked one does not, and a stranger's address finds nothing.
func TestRoomsAreListedByTheAddressThatHoldsASeat(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)

	byEmail := func(email string) int {
		var page apptest.AnyMap
		if status := e.Call(t, "GET", "/v1/deal-rooms?participant_email="+url.QueryEscape(email), nil, nil, &page); status != http.StatusOK {
			t.Fatalf("list by %s = %d %v", email, status, page)
		}
		rooms, _ := page["data"].([]any)
		return len(rooms)
	}
	if got := byEmail(room.email); got != 1 {
		t.Fatalf("rooms for the invited buyer = %d, want 1", got)
	}
	if got := byEmail("Laura@Buyer.Example"); got != 1 {
		t.Fatalf("rooms for the same address in another case = %d, want 1", got)
	}
	if got := byEmail("nobody@buyer.example"); got != 0 {
		t.Fatalf("rooms for a stranger = %d, want 0", got)
	}

	var roster apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/participants", nil, nil, &roster); status != http.StatusOK {
		t.Fatalf("roster = %d", status)
	}
	seats, _ := roster["data"].([]any)
	seat, _ := seats[0].(map[string]any)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/participants/"+seat["id"].(string)+"/revoke", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("revoke = %d", status)
	}
	if got := byEmail(room.email); got != 0 {
		t.Fatalf("rooms after revoke = %d, want 0", got)
	}
}
