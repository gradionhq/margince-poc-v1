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
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stubReader answers with whatever the case under test wants, and records the
// day boundary it was handed — the one derived value the transport owns.
type stubReader struct {
	live, settled []Item
	gotUser       ids.UUID
	gotStartOfDay time.Time
	// gotKinds is what the transport passed down, and nil vs empty is the
	// distinction the case cares about: nil asks for every kind, and an empty
	// slice would ask for none.
	gotKinds []string
	called   bool
}

func (s *stubReader) Mine(ctx context.Context, startOfToday time.Time, kinds []string) ([]Item, []Item, error) {
	actor, _ := principal.Actor(ctx)
	s.called, s.gotUser, s.gotStartOfDay, s.gotKinds = true, actor.UserID, startOfToday, kinds
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
			NewHandlers(reader, clock()).GetMyAiActivity(rec, request(tc.ctx), crmcontracts.GetMyAiActivityParams{})
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
	NewHandlers(&stubReader{}, clock()).GetMyAiActivity(rec, request(asHuman(user)), crmcontracts.GetMyAiActivityParams{})
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
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(user)), crmcontracts.GetMyAiActivityParams{})

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
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(me)), crmcontracts.GetMyAiActivityParams{})
	if reader.gotUser == someoneElse || reader.gotUser != me {
		t.Fatalf("read for %s, want %s", reader.gotUser, me)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// The filter is the client saying which part of the record it draws. Omitted
// means the complete record — every AI task reports here, and a client that
// names nothing gets everything rather than the rail's own three kinds, because
// the rail is one client and not the contract.
func TestAnOmittedFilterAsksForEveryKind(t *testing.T) {
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(ids.NewV7())), crmcontracts.GetMyAiActivityParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if reader.gotKinds != nil {
		t.Errorf("the store was handed kinds %v for a request that named none", reader.gotKinds)
	}
}

// A named filter reaches the store as the strings it will match on, so the
// bounds fall inside the caller's own set rather than on a record it draws
// nothing for.
func TestANamedFilterReachesTheStore(t *testing.T) {
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	kinds := []crmcontracts.AiActivityKind{
		crmcontracts.AiActivityKindMorningBrief,
		crmcontracts.AiActivityKindDocumentExtract,
	}
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(ids.NewV7())),
		crmcontracts.GetMyAiActivityParams{Kinds: &kinds})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	want := []string{"morning_brief", "document_extract"}
	if len(reader.gotKinds) != len(want) {
		t.Fatalf("the store was handed %v, want %v", reader.gotKinds, want)
	}
	for i, kind := range want {
		if reader.gotKinds[i] != kind {
			t.Errorf("kind %d = %q, want %q", i, reader.gotKinds[i], kind)
		}
	}
}

// An EMPTY list is refused rather than answered. `?kinds=` is what a client
// sends when the list it meant to send went missing, and an empty feed is the
// true answer for an AI at rest — so serving it would report "nothing happened"
// about a question the server never actually asked.
func TestAnEmptyFilterIsRefusedRatherThanServedEmpty(t *testing.T) {
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	none := []crmcontracts.AiActivityKind{}
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(ids.NewV7())),
		crmcontracts.GetMyAiActivityParams{Kinds: &none})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("the store was reached for a filter that can match nothing")
	}
}

// A kind this contract has no word for is REFUSED, not answered empty. It is
// the same defect as the empty list through a different door: the filter can
// only ever match nothing, and "nothing" is the true answer for an AI at rest.
//
// The generated binder is what makes this reachable — AiActivityKind is a string
// type and BindQueryParameterWithOptions binds anything into it, so nothing
// upstream of the handler checks the enum.
func TestAnUnknownKindIsRefusedRatherThanServedEmpty(t *testing.T) {
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	typo := []crmcontracts.AiActivityKind{
		crmcontracts.AiActivityKindMorningBrief,
		crmcontracts.AiActivityKind("summarise"),
	}
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(ids.NewV7())),
		crmcontracts.GetMyAiActivityParams{Kinds: &typo})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("the store was reached for a filter naming a kind the contract has no word for")
	}
	// The refusal has to name WHICH dial is wrong, or a client cannot act on it.
	if !strings.Contains(rec.Body.String(), "kinds") {
		t.Errorf("the refusal does not name the kinds parameter: %s", rec.Body.String())
	}
}

// A valid kind next to an invalid one does not rescue the request: a filter is
// refused as a whole, because serving the valid half would quietly answer a
// narrower question than the one asked.
func TestOneBadKindRefusesTheWholeFilter(t *testing.T) {
	reader := &stubReader{}
	rec := httptest.NewRecorder()
	mixed := []crmcontracts.AiActivityKind{
		crmcontracts.AiActivityKind("not_a_kind"),
		crmcontracts.AiActivityKindDocumentExtract,
	}
	NewHandlers(reader, clock()).GetMyAiActivity(rec, request(asHuman(ids.NewV7())),
		crmcontracts.GetMyAiActivityParams{Kinds: &mixed})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if reader.called {
		t.Fatal("the store was reached with the surviving half of a refused filter")
	}
}
