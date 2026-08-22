// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What a buyer may do in each room state, and what the published list shows.
// Both are pure functions of the row and the clock, so the rules are held here
// rather than only through the HTTP scenario.

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

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

func TestTheBuyerListIsThePublishedDefinitionsWithLiveCompletion(t *testing.T) {
	kept := openapi_types.UUID(ids.NewV7())
	archived := openapi_types.UUID(ids.NewV7())
	done := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	buyer := "buyer"
	snap := releaseSnapshot{Tasks: []snapshotTask{
		{ID: kept, Side: "buyer", Title: "Sign the DPA", Position: 1},
		{ID: archived, Side: "seller", Title: "Send the redline", Position: 2},
	}}
	live := map[openapi_types.UUID]liveCompletion{
		kept: {doneAt: &done, doneBy: &buyer},
		// The archived task has no live row: its definition was published, the
		// row it would be ticked on is gone.
	}
	got := buyerTasks(snap, live)
	if len(got) != 1 {
		t.Fatalf("got %d tasks, want the one still live: %+v", len(got), got)
	}
	if got[0].Id != kept || !got[0].Done || got[0].DoneBy == nil || *got[0].DoneBy != buyer || got[0].Title != "Sign the DPA" {
		t.Fatalf("task = %+v", got[0])
	}

	// A release published before task definitions rode the snapshot, and a
	// room with no tasks at all, both decode to an empty list rather than nil.
	old, err := decodeSnapshot([]byte(`{"title":"Acme","deal_id":"` + ids.NewV7().String() + `","released_at":"2026-08-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tasks := buyerTasks(old, live); tasks == nil || len(tasks) != 0 {
		t.Fatalf("an old release lists %v, want an empty list", tasks)
	}
	empty := snapshotOf(crmcontracts.DealRoom{Title: "Acme"}, nil)
	if empty.Tasks == nil {
		t.Fatal("a release with no tasks must carry an empty list, not a missing key")
	}
}
