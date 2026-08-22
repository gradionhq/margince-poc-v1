// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What a buyer may do in each room state, and what the published list shows.
// Both are pure functions of the row and the clock, so the rules are held here
// rather than only through the HTTP scenario.

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBuyerAccessFollowsTheRoomStateAndTheExpiryClock(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	cases := []struct {
		name      string
		state     string
		expiresAt *time.Time
		want      string
	}{
		{"draft before anything is published still counts as live access", stateDraft, nil, accessLive},
		{"building", "building", nil, accessLive},
		{"ready", "ready", nil, accessLive},
		{"publishing", "publishing", nil, accessLive},
		{"live", stateLive, nil, accessLive},
		{"live with a future expiry", stateLive, &future, accessLive},
		{"live whose expiry has passed is expired without any sweep", stateLive, &past, accessExpired},
		{"paused", statePaused, nil, accessPaused},
		{"paused outranks a passed expiry", statePaused, &past, accessPaused},
		{"closed keeps reading", stateClosed, nil, accessClosed},
		{"closed outranks a passed expiry", stateClosed, &past, accessClosed},
		{"expired", "expired", nil, accessExpired},
		{"archived", stateArchived, nil, accessExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := roomStanding{state: tc.state, expiresAt: tc.expiresAt}
			if got := st.access(now); got != tc.want {
				t.Fatalf("access(%s, expires %v) = %s, want %s", tc.state, tc.expiresAt, got, tc.want)
			}
		})
	}
	if servesContent(accessPaused) || servesContent(accessExpired) {
		t.Fatal("a paused or expired room must serve no content")
	}
	if !servesContent(accessLive) || !servesContent(accessClosed) {
		t.Fatal("a live or closed room serves its release")
	}
}

func TestAnOldReleaseCarryingKeysTheSnapshotNoLongerHasStillDecodes(t *testing.T) {
	// Releases once froze a shared to-do list under a "tasks" key. The key is
	// gone from the struct; a release that carries it must still decode, and
	// what it froze there is simply not served again.
	old, err := decodeSnapshot([]byte(`{"title":"Acme","deal_id":"` + ids.NewV7().String() +
		`","released_at":"2026-08-01T00:00:00Z","tasks":[{"id":"` + ids.NewV7().String() + `","side":"buyer","title":"Sign","position":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if old.Title != "Acme" || old.Documents != nil {
		t.Fatalf("old release decoded as %+v", old)
	}
	empty := snapshotOf(crmcontracts.DealRoom{Title: "Acme"}, nil)
	if empty.Documents == nil {
		t.Fatal("a release with no documents must carry an empty list, not a missing key")
	}
}
