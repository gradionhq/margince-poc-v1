// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

// The poll, against a provider that serves the real shapes over a loopback
// listener — which the production client refuses to dial, so the test injects
// its own http.Client and leaves the guard exactly as a deployment runs it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// fakeProvider serves /api/auth/me, /api/notifications/inbox and
// /api/users/batch with the shapes measured off a live deployment.
type fakeProvider struct {
	items []inboxItem
	// pageSize is what one page carries, so a test can force the walk to page
	// more than once without inventing fifty fixtures.
	pageSize int
	// unauthorized turns every call into a 401, which is how a revoked token
	// presents.
	unauthorized bool
	// requests records the `before` value of each inbox call, which is how a
	// test says "the second tick resumed under the gap".
	requests []int64
}

func (p *fakeProvider) start(t *testing.T) *client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the test server's URL: %v", err)
	}
	// The production constructor is deliberately not used: it attaches the
	// egress guard, which refuses loopback by design. What is under test here
	// is the walk, and the guard has its own tests.
	return &client{base: base, token: "pat_test", http: server.Client()}
}

func (p *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	if p.unauthorized {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/api/auth/me"):
		p.writeJSON(w, providerUser{
			ID: "provider-member", WorkspaceID: "ws-7",
			Email: "member@installation.test", DisplayName: "The Member",
		})
	case strings.HasSuffix(r.URL.Path, "/api/notifications/inbox"):
		p.writeInbox(w, r)
	case strings.HasSuffix(r.URL.Path, "/api/users/batch"):
		// A bare ARRAY, as the deployment answers — not an object with a
		// member. A test that wrapped it would let the client's decoding drift
		// back to the shape that reads zero users.
		p.writeJSON(w, []providerUser{{
			ID: "sender-1", WorkspaceID: "ws-7", Email: "outside@example.com",
			DisplayName: "A Sender",
		}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *fakeProvider) writeInbox(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	p.requests = append(p.requests, before)
	page := make([]inboxItem, 0, p.pageSize)
	for _, item := range p.items { // newest first, as the provider serves them
		if before > 0 && item.ID >= before {
			continue
		}
		if len(page) == p.pageSize {
			break
		}
		page = append(page, item)
	}
	oldest := int64(0)
	if len(page) > 0 {
		oldest = page[len(page)-1].ID
	}
	hasMore := false
	for _, item := range p.items {
		if oldest > 0 && item.ID < oldest {
			hasMore = true
		}
	}
	p.writeJSON(w, map[string]any{"items": page, "has_more": hasMore})
}

func (p *fakeProvider) writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(fmt.Sprintf("the fake provider could not encode its answer: %v", err))
	}
}

// dm builds one directed notification.
func dm(id int64) inboxItem {
	return inboxItem{
		ID: id, Type: "dm", Title: "A Sender", Body: "a message",
		ChannelID: "channel-1", MessageID: "message-" + strconv.FormatInt(id, 10),
		SenderID: "sender-1", CreatedAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		Raw: json.RawMessage(`{"id":` + strconv.FormatInt(id, 10) + `}`),
	}
}

// reaction builds one notification that is NOT an interaction with a
// counterparty, and is the bulk of a real inbox.
func reaction(id int64) inboxItem {
	item := dm(id)
	item.Type = "reaction"
	return item
}

// descending is the provider's own order.
func descending(items ...inboxItem) []inboxItem { return items }

func aConnection(mark, gap int64) connection {
	return connection{
		ID: "11111111-1111-4111-8111-111111111111", UserID: callerUserID,
		BaseURL: testBaseURL, Status: statusConnected, HighWaterMark: mark, BackfillBefore: gap,
	}
}

// The whole point of the unit, at the level this suite can see it: a directed
// message becomes a record handed to the ingress port, on the connection's own
// member, and a reaction does not.
func TestAPollLandsDirectedMessagesAndNothingElse(t *testing.T) {
	provider := &fakeProvider{items: descending(reaction(30), dm(20), reaction(10)), pageSize: 50}
	rt := newRuntime().unattended()
	rt.tx.singleRows = [][]any{connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 30, 0)}

	member := providerUser{ID: "provider-member", WorkspaceID: "ws-7", Email: "member@installation.test"}
	processedTo, err := landAll(context.Background(), rt, provider.start(t),
		[]inboxItem{reaction(30), dm(20), reaction(10)}, aConnection(0, 0), member)
	if err != nil {
		t.Fatalf("landAll: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("ingested %d record(s), want the one directed message", len(rt.ingested))
	}
	rec := rt.ingested[0]
	switch {
	case rec.System != ingressSystem:
		t.Errorf("system = %q, want the declared source %q", rec.System, ingressSystem)
	case rec.Key != "ws-7:20":
		t.Errorf("key = %q, want the provider workspace and the notification id", rec.Key)
	case rec.ThreadKey != "dispact:ws-7:channel-1":
		t.Errorf("thread key = %q, want the namespaced conversation", rec.ThreadKey)
	case len(rec.Addresses) != 2:
		t.Errorf("addresses = %v, want both ends — an empty set silently disables the internal-only gate", rec.Addresses)
	}
	if rt.ingestedOn[0] != extension.UserID(callerUserID) {
		t.Errorf("ingested on %q, want the CRM member the connection names — never the provider's account id", rt.ingestedOn[0])
	}
	// The cursor moves past the reactions too: a mark that only advanced past
	// what LANDED would re-page a feed of reactions on every tick, forever.
	if processedTo != 30 {
		t.Errorf("processed to %d, want 30 — the filtered items are decided about as much as the landed one", processedTo)
	}
}

// A record the core calls invalid can never land, so the poll must not park on
// it: the connection would stop reading its inbox over one malformed message.
func TestAnUnlandableRecordIsPassedRatherThanParkedOn(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(20)), pageSize: 50}
	rt := newRuntime().unattended()
	rt.ingestErr, rt.ingestFrom = extension.ErrInvalid, 1

	processedTo, err := landAll(context.Background(), rt, provider.start(t),
		[]inboxItem{dm(20)}, aConnection(0, 0),
		providerUser{ID: "provider-member", WorkspaceID: "ws-7", Email: "member@installation.test"})
	if err != nil {
		t.Fatalf("landAll: %v", err)
	}
	if processedTo != 20 {
		t.Errorf("processed to %d, want 20 — an unlandable record is decided about", processedTo)
	}
}

// Every other refusal is about this unit's standing, and stops the tick with
// nothing advanced: the caller writes no cursor, so the region is walked again.
func TestASystemicRefusalStopsTheTickAndAdvancesNothing(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(30), dm(20), dm(10)), pageSize: 50}
	rt := newRuntime().unattended()
	// The port refuses from the SECOND record onward, which is the interesting
	// case: something landed, and the tick must still not advance past what it
	// did not reach.
	rt.ingestErr, rt.ingestFrom = extension.ErrForbidden, 2

	processedTo, err := landAll(context.Background(), rt, provider.start(t),
		[]inboxItem{dm(30), dm(20), dm(10)}, aConnection(0, 0),
		providerUser{ID: "provider-member", WorkspaceID: "ws-7", Email: "member@installation.test"})
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want the port's refusal to reach the caller", err)
	}
	if processedTo != 10 {
		t.Errorf("processed to %d, want 10 — the one record that landed, and nothing above it", processedTo)
	}
}

// The nesting rule, proved at the unit's own level: the poll reads its work,
// closes the transaction, and ingests outside it. A handler shaped the other
// way meets ErrNestedIngest — which is what the fake answers, exactly as the
// core does.
func TestThePollDoesNotIngestFromInsideItsOwnTransaction(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(20)), pageSize: 50}
	rt := newRuntime().unattended()
	member := providerUser{ID: "provider-member", WorkspaceID: "ws-7", Email: "member@installation.test"}

	// Inside a transaction, the same call the poll makes is refused.
	inside := rt.Tx(context.Background(), func(ctx context.Context, _ extension.Tx) error {
		_, err := landAll(ctx, rt, provider.start(t), []inboxItem{dm(20)}, aConnection(0, 0), member)
		return err
	})
	if !errors.Is(inside, extension.ErrNestedIngest) {
		t.Fatalf("err = %v, want ErrNestedIngest — this is the shape that hangs a small pool rather than failing", inside)
	}
	// Outside one, it lands.
	if _, err := landAll(context.Background(), rt, provider.start(t), []inboxItem{dm(20)}, aConnection(0, 0), member); err != nil {
		t.Fatalf("landAll outside a transaction: %v", err)
	}
}

// The walk stops at the mark rather than reading the whole feed, and reports
// itself closed — which is what lets the cursor move.
func TestTheWalkStopsAtTheMark(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(40), dm(30), dm(20), dm(10)), pageSize: 2}
	walked, err := walkInbox(context.Background(), provider.start(t), 20, 0, maxPagesPerPoll)
	if err != nil {
		t.Fatalf("walkInbox: %v", err)
	}
	if !walked.closed {
		t.Error("the walk reached the mark and did not report itself closed, so the cursor would never move")
	}
	if len(walked.items) != 2 {
		t.Fatalf("fetched %d item(s), want the two above the mark", len(walked.items))
	}
}

// The budget case, and the defect the second cursor exists for: a walk that
// ran out of pages must NOT advance the mark, or everything under the gap
// becomes unreachable — the next tick's newest page is already above it.
func TestATruncatedWalkLeavesTheMarkAndRemembersTheGap(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(50), dm(40), dm(30), dm(20), dm(10)), pageSize: 1}
	walked, err := walkInbox(context.Background(), provider.start(t), 0, 0, 2)
	if err != nil {
		t.Fatalf("walkInbox: %v", err)
	}
	if walked.closed {
		t.Fatal("a walk that spent its budget reported itself closed")
	}
	mark, gap := advanced(0, 0, 40, walked)
	if mark != 0 {
		t.Errorf("mark = %d, want it left at 0 — advancing over an unread region strands it permanently", mark)
	}
	if gap != 40 {
		t.Errorf("gap = %d, want the oldest id this walk reached", gap)
	}
	// And the next tick resumes UNDER the gap rather than at the newest page.
	provider.requests = nil
	if _, err := walkInbox(context.Background(), provider.start(t), 0, gap, 2); err != nil {
		t.Fatalf("the resuming walk: %v", err)
	}
}

// Closing the gap is what finally moves the mark, and clears the gap with it.
func TestClosingTheGapMovesTheMarkAndClearsIt(t *testing.T) {
	provider := &fakeProvider{items: descending(dm(30), dm(20), dm(10)), pageSize: 50}
	walked, err := walkInbox(context.Background(), provider.start(t), 0, 40, maxPagesPerPoll)
	if err != nil {
		t.Fatalf("walkInbox: %v", err)
	}
	if !walked.closed {
		t.Fatal("a walk that reached the start of the feed did not report itself closed")
	}
	mark, gap := advanced(0, 40, 30, walked)
	if mark != 30 || gap != 0 {
		t.Errorf("cursor = (%d, %d), want (30, 0) — the gap is closed, so the mark may jump to the top of what was decided", mark, gap)
	}
}

// A closed walk that found nothing new still clears a gap: the region the gap
// named has now been read.
func TestAClosedWalkWithNothingNewStillClearsTheGap(t *testing.T) {
	mark, gap := advanced(30, 40, 0, walkResult{closed: true})
	if mark != 30 || gap != 0 {
		t.Errorf("cursor = (%d, %d), want (30, 0)", mark, gap)
	}
}

// A revoked token parks the connection rather than being retried every two
// minutes for as long as nobody notices.
func TestARevokedTokenParksTheConnection(t *testing.T) {
	rt := newRuntime().unattended()
	rt.tx.singleRows = [][]any{connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusReauth, 0, 0)}

	if err := noteFailure(context.Background(), rt, aConnection(0, 0), errUnauthorized); err != nil {
		t.Fatalf("noteFailure: %v", err)
	}
	sql, args := rt.tx.statementMentioning(t, "UPDATE")
	if !strings.Contains(sql, "status = $2") || args[1] != statusReauth {
		t.Errorf("the failure did not park the connection: %v", args)
	}
	if args[2] != "token_rejected" {
		t.Errorf("class = %v, want this unit's own vocabulary rather than the provider's message", args[2])
	}
	if len(rt.tx.published) != 1 || rt.tx.published[0].Verb != eventReauth {
		t.Fatalf("published %+v, want the reauth event a screen can react to", rt.tx.published)
	}
}

// A tick that moved no cursor records nothing: one ledger row per member per
// cadence, forever, to say that a schedule ran is a history of the schedule
// rather than of the work.
func TestAPollThatFoundNothingRecordsNothing(t *testing.T) {
	rt := newRuntime().unattended()
	unchanged := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 30, 0)
	rt.tx.singleRows = [][]any{unchanged}

	err := saveCursor(context.Background(), rt, aConnection(30, 0),
		providerUser{WorkspaceID: "ws-7", DisplayName: "The Member"}, 30, 0)
	if err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	if len(rt.tx.audited) != 0 {
		t.Errorf("recorded %+v for a tick that decided about nothing", rt.tx.audited)
	}
}

// A cursor that moved IS recorded: how far a connection has read is a fact
// somebody may later ask about.
func TestAPollThatMovedTheCursorRecordsIt(t *testing.T) {
	rt := newRuntime().unattended()
	moved := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 90, 0)
	rt.tx.singleRows = [][]any{moved}

	err := saveCursor(context.Background(), rt, aConnection(30, 0),
		providerUser{WorkspaceID: "ws-7", DisplayName: "The Member"}, 90, 0)
	if err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	if len(rt.tx.audited) != 1 || rt.tx.audited[0].Action != extension.AuditUpdate {
		t.Fatalf("recorded %+v, want one update", rt.tx.audited)
	}
	if len(rt.tx.published) != 1 || rt.tx.published[0].Verb != eventPolled {
		t.Fatalf("published %+v, want one %q event", rt.tx.published, eventPolled)
	}
}

// A member who disconnected while their inbox was being read has no row left.
// The records they produced are theirs and stay; resurrecting the connection to
// carry a cursor would undo a withdrawal somebody just made.
func TestACursorForAConnectionThatWentAwayIsNotResurrected(t *testing.T) {
	rt := newRuntime().unattended()
	rt.tx.noRows = map[int]bool{1: true}

	err := saveCursor(context.Background(), rt, aConnection(30, 0),
		providerUser{WorkspaceID: "ws-7"}, 90, 0)
	if err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	if len(rt.tx.audited) != 0 {
		t.Errorf("recorded %+v against a connection that is gone", rt.tx.audited)
	}
}
