// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- fakes ---------------------------------------------------------------

type fakeOAuth struct {
	refresh, access string
}

func (f fakeOAuth) AuthCodeURL(state, _ string) string { return "https://auth?state=" + state }
func (f fakeOAuth) Exchange(context.Context, string, string) (string, error) {
	return f.refresh, nil
}
func (f fakeOAuth) AccessToken(context.Context, string) (string, error) { return f.access, nil }

type fakeAPI struct {
	email string
	// DeltaInit's canned round (the initial/bounded anchor).
	initIDs   []string
	initDelta string
	initAfter time.Time
	initCalls int
	// Delta's canned round (the incremental resume).
	deltaIDs   []string
	deltaLink  string
	deltaErr   error
	deltaCalls int
	seenDelta  string

	getErr error
	raws   map[string][]byte

	estimate      int
	estimateAfter time.Time
	listIDs       []string
	listNext      string
	listCalls     int
	seenPageToken string
}

func (f *fakeAPI) Profile(context.Context, string) (string, error) { return f.email, nil }

func (f *fakeAPI) DeltaInit(_ context.Context, _ string, after time.Time) ([]string, string, error) {
	f.initCalls++
	f.initAfter = after
	return f.initIDs, f.initDelta, nil
}

func (f *fakeAPI) Delta(_ context.Context, _, deltaLink string) ([]string, string, error) {
	f.deltaCalls++
	f.seenDelta = deltaLink
	if f.deltaErr != nil {
		return nil, "", f.deltaErr
	}
	return f.deltaIDs, f.deltaLink, nil
}

func (f *fakeAPI) GetMIME(_ context.Context, _, id string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.raws[id], nil
}

func (f *fakeAPI) EstimateAfter(_ context.Context, _ string, after time.Time) (int, error) {
	f.estimateAfter = after
	return f.estimate, nil
}

func (f *fakeAPI) ListAfter(_ context.Context, _ string, _ time.Time, pageToken string, _ int) ([]string, string, error) {
	f.listCalls++
	f.seenPageToken = pageToken
	return f.listIDs, f.listNext, nil
}

type recordingSink struct{ recs []connector.NormalizedRecord }

func (s *recordingSink) Upsert(_ context.Context, rec connector.NormalizedRecord) (datasource.EntityRef, error) {
	s.recs = append(s.recs, rec)
	return datasource.EntityRef{}, nil
}

func rawMsg(msgID, from string) []byte {
	return []byte(strings.Join([]string{
		"From: " + from,
		"To: " + owner,
		"Subject: hi",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <" + msgID + ">",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello there",
		"",
	}, "\r\n"))
}

const owner = "rep@myco.com"

func authBytes(t *testing.T) connector.Auth {
	t.Helper()
	// A map (not the authState struct) so the marshal carries no secret-named
	// struct field — same JSON the connector unmarshals, without tripping the
	// marshaled-secret lint on a test fixture.
	b, err := json.Marshal(map[string]any{"refresh_token": "refresh-1", "owner_email": owner, "scopes": []string{"read"}})
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	return b
}

// pinned is the deterministic clock the anchor-window assertions use.
var pinned = time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

func pinnedConn(api *fakeAPI) *Connector {
	c := New(fakeOAuth{access: "access-1"}, api)
	c.now = func() time.Time { return pinned }
	return c
}

// --- tests ---------------------------------------------------------------

func TestDescriptorIsAutoExecuteReadOnly(t *testing.T) {
	d := New(fakeOAuth{}, &fakeAPI{}).Descriptor()
	if d.Name != "graph" {
		t.Errorf("Name = %q, want graph", d.Name)
	}
	if d.RiskTier != mcp.TierAutoExecute {
		t.Errorf("RiskTier = %v, want auto_execute (read-only capture)", d.RiskTier)
	}
	if len(d.Scopes) != 1 || d.Scopes[0] != principal.ScopeRead {
		t.Errorf("Scopes = %v, want [read]", d.Scopes)
	}
	if len(d.Produces) != 1 || d.Produces[0] != datasource.EntityActivity {
		t.Errorf("Produces = %v, want [activity]", d.Produces)
	}
}

func TestAuthenticateBindsRefreshTokenAndOwner(t *testing.T) {
	c := New(fakeOAuth{refresh: "refresh-1", access: "access-1"}, &fakeAPI{email: owner})
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	auth, err := c.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		t.Fatalf("auth is not authState json: %v", err)
	}
	if st.RefreshToken != "refresh-1" || st.Owner != owner {
		t.Errorf("authState = %+v, want refresh-1 / %s", st, owner)
	}
}

func TestAuthenticateRequiresCode(t *testing.T) {
	c := New(fakeOAuth{}, &fakeAPI{})
	req, err := AuthRequestFrom("", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	if _, err := c.Authenticate(context.Background(), req); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Authenticate without a code = %v, want ErrAuthRejected", err)
	}
}

func TestSyncInitialAnchorIsBoundedAndCaptures(t *testing.T) {
	api := &fakeAPI{
		email:     owner,
		initIDs:   []string{"m1", "m2"},
		initDelta: "https://graph/delta?token=d1",
		raws: map[string][]byte{
			"m1": rawMsg("m1@mail.example", "alice@acme.com"),
			"m2": rawMsg("m2@mail.example", "bob@acme.com"),
		},
	}
	c := pinnedConn(api)
	sink := &recordingSink{}

	cur, err := c.Sync(context.Background(), authBytes(t), nil, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 2 {
		t.Fatalf("captured %d records, want 2", len(sink.recs))
	}
	if sink.recs[0].Source != "graph:m1@mail.example" {
		t.Errorf("Source = %q, want graph:m1@mail.example", sink.recs[0].Source)
	}
	if sink.recs[0].CapturedBy != "connector:graph" {
		t.Errorf("CapturedBy = %q, want connector:graph", sink.recs[0].CapturedBy)
	}
	if delta, _ := parseCursor(cur); delta != "https://graph/delta?token=d1" {
		t.Errorf("cursor deltaLink = %q, want the DeltaInit link", delta)
	}
	if want := pinned.Add(-anchorWindow); !api.initAfter.Equal(want) {
		t.Errorf("initial delta bound = %v, want %v (now - anchorWindow)", api.initAfter, want)
	}
	if api.deltaCalls != 0 {
		t.Errorf("initial anchor must not resume a delta, got %d Delta calls", api.deltaCalls)
	}
}

func TestSyncIncrementalResumesDeltaAndAdvancesCursor(t *testing.T) {
	api := &fakeAPI{
		email:     owner,
		deltaIDs:  []string{"m3"},
		deltaLink: "https://graph/delta?token=d2",
		raws:      map[string][]byte{"m3": rawMsg("m3@mail.example", "carol@acme.com")},
	}
	c := pinnedConn(api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{DeltaLink: "https://graph/delta?token=d1", Email: owner})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].NaturalKey.SourceID != "m3@mail.example" {
		t.Fatalf("want 1 record m3, got %+v", sink.recs)
	}
	if api.deltaCalls != 1 || api.initCalls != 0 {
		t.Errorf("incremental path should resume delta once, never re-anchor; got delta=%d init=%d", api.deltaCalls, api.initCalls)
	}
	if api.seenDelta != "https://graph/delta?token=d1" {
		t.Errorf("resumed deltaLink = %q, want the stored one", api.seenDelta)
	}
	if delta, _ := parseCursor(cur); delta != "https://graph/delta?token=d2" {
		t.Errorf("cursor = %q, want advanced to token=d2", delta)
	}
}

func TestSyncDeltaGoneReAnchorsBounded(t *testing.T) {
	api := &fakeAPI{
		email:     owner,
		deltaErr:  ErrDeltaGone,
		initIDs:   []string{"m1"},
		initDelta: "https://graph/delta?token=fresh",
		raws:      map[string][]byte{"m1": rawMsg("m1@mail.example", "alice@acme.com")},
	}
	c := pinnedConn(api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{DeltaLink: "https://graph/delta?token=stale", Email: owner})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("a gone deltaLink must not fail Sync: %v", err)
	}
	if len(sink.recs) != 1 {
		t.Fatalf("re-anchor should capture the bounded window, captured %d want 1", len(sink.recs))
	}
	if api.initCalls != 1 {
		t.Errorf("re-anchor should call DeltaInit once, got %d", api.initCalls)
	}
	if want := pinned.Add(-anchorWindow); !api.initAfter.Equal(want) {
		t.Errorf("re-anchor bound = %v, want %v (now - anchorWindow)", api.initAfter, want)
	}
	if delta, _ := parseCursor(cur); delta != "https://graph/delta?token=fresh" {
		t.Errorf("cursor should re-anchor at the fresh deltaLink, got %q", delta)
	}
}

func TestSyncOtherDeltaErrorPropagates(t *testing.T) {
	api := &fakeAPI{email: owner, deltaErr: ErrUnreachable}
	c := pinnedConn(api)
	prior, _ := json.Marshal(cursorState{DeltaLink: "https://graph/delta?token=d1", Email: owner})
	if _, err := c.Sync(context.Background(), authBytes(t), prior, &recordingSink{}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a non-gone delta fault should propagate, got %v", err)
	}
	if api.initCalls != 0 {
		t.Errorf("an unreachable provider must not trigger a re-anchor, got %d DeltaInit calls", api.initCalls)
	}
}

func TestSyncUnreadableCursorStopsWithoutReAnchor(t *testing.T) {
	api := &fakeAPI{email: owner}
	c := pinnedConn(api)
	if _, err := c.Sync(context.Background(), authBytes(t), connector.Cursor("{not json"), &recordingSink{}); err == nil {
		t.Fatal("a corrupt stored cursor must fail Sync, not silently re-anchor")
	}
	if api.initCalls != 0 || api.deltaCalls != 0 {
		t.Errorf("a corrupt cursor must stop before any provider pull (init=%d delta=%d)", api.initCalls, api.deltaCalls)
	}
}

func TestSyncEmptyDeltaKeepsPriorWatermark(t *testing.T) {
	// Provider closes the round with no new link (defensive: Graph always
	// sends one, but the watermark must never regress to empty).
	api := &fakeAPI{email: owner, deltaIDs: nil, deltaLink: ""}
	c := pinnedConn(api)
	prior, _ := json.Marshal(cursorState{DeltaLink: "https://graph/delta?token=d1", Email: owner})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, &recordingSink{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if delta, _ := parseCursor(cur); delta != "https://graph/delta?token=d1" {
		t.Errorf("cursor = %q, want the prior watermark kept", delta)
	}
}

func TestCursorCarriesMailboxEmail(t *testing.T) {
	api := &fakeAPI{email: owner, initDelta: "https://graph/delta?token=d1"}
	c := pinnedConn(api)
	cur, err := c.Sync(context.Background(), authBytes(t), nil, &recordingSink{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var cs cursorState
	if err := json.Unmarshal(cur, &cs); err != nil {
		t.Fatalf("cursor is not cursorState json: %v", err)
	}
	if cs.Email != owner {
		t.Errorf("cursor email = %q, want %q (the mailbox the watermark belongs to)", cs.Email, owner)
	}
}

func TestNormalizeSkipsAutomatedMail(t *testing.T) {
	c := New(fakeOAuth{}, &fakeAPI{})
	c.owner = owner
	auto := []byte(strings.Join([]string{
		"From: system@acme.com", "To: " + owner, "Subject: OOO",
		"Auto-Submitted: auto-replied", "Message-ID: <ooo@acme.com>",
		"Content-Type: text/plain", "", "away", "",
	}, "\r\n"))
	if _, err := c.Normalize(context.Background(), auto); !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("want ErrSkip for auto-submitted mail, got %v", err)
	}

	recs, err := c.Normalize(context.Background(), rawMsg("keep@acme.com", "dave@acme.com"))
	if err != nil {
		t.Fatalf("Normalize a normal message: %v", err)
	}
	if len(recs) != 1 || recs[0].Source != "graph:keep@acme.com" {
		t.Fatalf("want 1 record graph:keep@acme.com, got %+v", recs)
	}
}
