// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// The two rules that live in the transport rather than in SQL: who is refused,
// and what an empty feed looks like on the wire.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stubReader answers with whatever the case under test wants, and records the
// day boundary it was handed — the one derived value the transport owns.
type stubReader struct {
	live, settled []Item
	gotUser       ids.UUID
	gotStartOfDay time.Time
	called        bool
}

func (s *stubReader) Mine(ctx context.Context, startOfToday time.Time) ([]Item, []Item, error) {
	actor, _ := principal.Actor(ctx)
	s.called, s.gotUser, s.gotStartOfDay = true, actor.UserID, startOfToday
	return s.live, s.settled, nil
}

// fixedNow is the suite's clock. Stated rather than read, because "today" is
// derived from it and a test that took the wall clock would compute a different
// day for the few minutes either side of midnight.
var fixedNow = time.Date(2026, 8, 21, 12, 30, 0, 0, time.FixedZone("CET", 2*60*60))

func clock() func() time.Time { return func() time.Time { return fixedNow } }

func request(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/v1/me/ai-activity", nil).WithContext(ctx)
}

func asHuman(user ids.UUID) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: principal.HumanIDPrefix + user.String(), UserID: user,
	})
}

// An unidentified caller is REFUSED, not served an empty feed. An empty feed is
// the true answer for an AI at rest, so handing one to a caller the server
// never resolved reports "nothing is running" about nobody in particular.
func TestACallerWithNoUserIdentityIsRefusedRatherThanServedEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"no actor at all", context.Background()},
		{"an actor with no user", principal.WithActor(context.Background(), principal.Principal{
			Type: principal.PrincipalSystem, ID: "system:relay",
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &stubReader{}
			rec := httptest.NewRecorder()
			NewHandlers(reader, clock()).GetMyAiActivity(rec, request(tc.ctx))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			if reader.called {
				t.Fatal("the store was reached for a caller with no identity")
			}
		})
	}
}

// An empty feed serializes as [] and never as null: the contract declares both
// fields as arrays, and a client that iterates what it was promised crashes on
// a null.
func TestAnEmptyFeedIsArraysAndNotNulls(t *testing.T) {
	user := ids.NewV7()
	rec := httptest.NewRecorder()
	NewHandlers(&stubReader{}, clock()).GetMyAiActivity(rec, request(asHuman(user)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding the body: %v", err)
	}
	for _, field := range []string{"running", "recent"} {
		if got := string(raw[field]); got != "[]" {
			t.Errorf("%s = %s, want []", field, got)
		}
	}
}

// "Today" is midnight in the SERVER's own location, computed once and handed to
// the store — so the two halves of one response cannot straddle a day boundary
// that moved between them.
func TestTodayIsMidnightInTheServersOwnLocation(t *testing.T) {
	user := ids.NewV7()
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(user)))

	want := time.Date(2026, 8, 21, 0, 0, 0, 0, fixedNow.Location())
	if !reader.gotStartOfDay.Equal(want) {
		t.Fatalf("start of day = %s, want %s", reader.gotStartOfDay, want)
	}
	if reader.gotUser != user {
		t.Fatalf("read for %s, want the authenticated caller %s", reader.gotUser, user)
	}
}

// The feed is the CALLER's. The handler passes the authenticated principal's own
// user id and has no way to express anybody else's.
func TestTheFeedIsAlwaysReadForTheAuthenticatedCaller(t *testing.T) {
	me, someoneElse := ids.NewV7(), ids.NewV7()
	reader := &stubReader{live: []Item{{ID: ids.NewV7(), Kind: "document_extract", State: "running"}}}
	rec := httptest.NewRecorder()
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(me)))
	if reader.gotUser == someoneElse || reader.gotUser != me {
		t.Fatalf("read for %s, want %s", reader.gotUser, me)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
