// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

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
	email, historyID   string
	recent, added      []string
	addedHistoryID     string
	historyErr, getErr error
	raws               map[string][]byte
	historyCalls       int
	listCalls          int
	watchHistoryID     string
	watchExpiry        time.Time
	watchErr           error
	watchCalls         int
	watchTopic         string
}

func (f *fakeAPI) Profile(context.Context, string) (string, string, error) {
	return f.email, f.historyID, nil
}

func (f *fakeAPI) ListRecent(context.Context, string, int) ([]string, error) {
	f.listCalls++
	return f.recent, nil
}

func (f *fakeAPI) History(context.Context, string, string) ([]string, string, error) {
	f.historyCalls++
	if f.historyErr != nil {
		return nil, "", f.historyErr
	}
	return f.added, f.addedHistoryID, nil
}

func (f *fakeAPI) Watch(_ context.Context, _, topic string) (string, time.Time, error) {
	f.watchCalls++
	f.watchTopic = topic
	if f.watchErr != nil {
		return "", time.Time{}, f.watchErr
	}
	return f.watchHistoryID, f.watchExpiry, nil
}

func (f *fakeAPI) GetRaw(_ context.Context, _, id string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.raws[id], nil
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

// --- tests ---------------------------------------------------------------

func TestDescriptorIsGreenReadOnly(t *testing.T) {
	d := New(fakeOAuth{}, &fakeAPI{}).Descriptor()
	if d.Name != "gmail" {
		t.Errorf("Name = %q, want gmail", d.Name)
	}
	if d.RiskTier != mcp.TierGreen {
		t.Errorf("RiskTier = %v, want green (read-only capture)", d.RiskTier)
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

func TestSyncInitialBackfillAnchorsCursorAndCaptures(t *testing.T) {
	api := &fakeAPI{
		email:     owner,
		historyID: "12345",
		recent:    []string{"m1@mail.gmail.com", "m2@mail.gmail.com"},
		raws: map[string][]byte{
			"m1@mail.gmail.com": rawMsg("m1@mail.gmail.com", "alice@acme.com"),
			"m2@mail.gmail.com": rawMsg("m2@mail.gmail.com", "bob@acme.com"),
		},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	cur, err := c.Sync(context.Background(), authBytes(t), nil, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 2 {
		t.Fatalf("captured %d records, want 2", len(sink.recs))
	}
	if sink.recs[0].Source != "gmail:m1@mail.gmail.com" {
		t.Errorf("Source = %q, want gmail:m1@mail.gmail.com", sink.recs[0].Source)
	}
	if sink.recs[0].CapturedBy != "connector:gmail" {
		t.Errorf("CapturedBy = %q, want connector:gmail", sink.recs[0].CapturedBy)
	}
	if hid, _ := parseCursor(cur); hid != "12345" {
		t.Errorf("cursor historyId = %q, want 12345 (anchored at profile)", hid)
	}
	if api.historyCalls != 0 {
		t.Errorf("initial backfill must not call history, got %d calls", api.historyCalls)
	}
}

func TestSyncIncrementalUsesHistoryAndAdvancesCursor(t *testing.T) {
	api := &fakeAPI{
		email:          owner,
		added:          []string{"m3@mail.gmail.com"},
		addedHistoryID: "99999",
		raws:           map[string][]byte{"m3@mail.gmail.com": rawMsg("m3@mail.gmail.com", "carol@acme.com")},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{HistoryID: "12345"})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].NaturalKey.SourceID != "m3@mail.gmail.com" {
		t.Fatalf("want 1 record m3, got %+v", sink.recs)
	}
	if api.historyCalls != 1 || api.listCalls != 0 {
		t.Errorf("incremental path should call history once, list never; got history=%d list=%d", api.historyCalls, api.listCalls)
	}
	if hid, _ := parseCursor(cur); hid != "99999" {
		t.Errorf("cursor = %q, want advanced to 99999", hid)
	}
}

func TestSyncHistoryGoneFallsBackToList(t *testing.T) {
	api := &fakeAPI{
		email:      owner,
		historyID:  "55555",
		historyErr: ErrHistoryGone,
		recent:     []string{"m1@mail.gmail.com"},
		raws:       map[string][]byte{"m1@mail.gmail.com": rawMsg("m1@mail.gmail.com", "alice@acme.com")},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{HistoryID: "1"})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("a too-old cursor must not fail Sync: %v", err)
	}
	if len(sink.recs) != 1 {
		t.Fatalf("fallback should re-list, captured %d want 1", len(sink.recs))
	}
	if api.listCalls != 1 {
		t.Errorf("fallback should call ListRecent once, got %d", api.listCalls)
	}
	if hid, _ := parseCursor(cur); hid != "55555" {
		t.Errorf("cursor should re-anchor at profile historyId 55555, got %q", hid)
	}
}

func TestWatchRegistersAndReturnsExpiry(t *testing.T) {
	exp := time.UnixMilli(1431990098200)
	api := &fakeAPI{watchHistoryID: "99999", watchExpiry: exp}
	c := New(fakeOAuth{access: "access-1"}, api)

	res, err := c.Watch(context.Background(), authBytes(t), "projects/p/topics/gmail-push")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if res.HistoryID != "99999" {
		t.Errorf("HistoryID = %q, want 99999", res.HistoryID)
	}
	if !res.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", res.ExpiresAt, exp)
	}
	if api.watchTopic != "projects/p/topics/gmail-push" {
		t.Errorf("topic passed to Gmail = %q, want the configured topic", api.watchTopic)
	}
	if api.watchCalls != 1 {
		t.Errorf("watch called %d times, want 1", api.watchCalls)
	}
}

func TestWatchPropagatesProviderError(t *testing.T) {
	api := &fakeAPI{watchErr: ErrAuthRejected}
	c := New(fakeOAuth{access: "access-1"}, api)
	if _, err := c.Watch(context.Background(), authBytes(t), "projects/p/topics/gmail-push"); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected propagated, got %v", err)
	}
}

// The Gmail connector satisfies the optional push-watch seam the registry's
// renewal scan invokes.
var _ connector.Watcher = (*Connector)(nil)

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
	if len(recs) != 1 || recs[0].Source != "gmail:keep@acme.com" {
		t.Fatalf("want 1 record gmail:keep@acme.com, got %+v", recs)
	}
}

func TestAccountIDReturnsOwner(t *testing.T) {
	c := New(nil, nil)
	auth, err := json.Marshal(authState{RefreshToken: "r", Owner: "rep@ws.example", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AccountID(auth)
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	if got != "rep@ws.example" {
		t.Errorf("AccountID = %q, want rep@ws.example", got)
	}
}

func TestAccountIDRejectsMalformedAuth(t *testing.T) {
	if _, err := New(nil, nil).AccountID([]byte("not json")); err == nil {
		t.Fatal("AccountID(malformed) = nil error, want an error")
	}
}
