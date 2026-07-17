// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
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
	owner            string
	initial, delta   [][]byte
	initialToken     string
	deltaToken       string
	deltaErr         error
	initialCalls     int
	incrementalCalls int
}

func (f *fakeAPI) PrimaryOwner(context.Context, string) (string, error) { return f.owner, nil }

func (f *fakeAPI) ListInitial(context.Context, string) ([][]byte, string, error) {
	f.initialCalls++
	return f.initial, f.initialToken, nil
}

func (f *fakeAPI) ListIncremental(context.Context, string, string) ([][]byte, string, error) {
	f.incrementalCalls++
	if f.deltaErr != nil {
		return nil, "", f.deltaErr
	}
	return f.delta, f.deltaToken, nil
}

type recordingSink struct{ recs []connector.NormalizedRecord }

func (s *recordingSink) Upsert(_ context.Context, rec connector.NormalizedRecord) (datasource.EntityRef, error) {
	s.recs = append(s.recs, rec)
	return datasource.EntityRef{}, nil
}

func authBytes(t *testing.T) connector.Auth {
	t.Helper()
	// A map (not the authState struct) so the marshal carries no secret-named
	// struct field — same JSON the connector unmarshals, without tripping the
	// marshaled-secret lint on a test fixture.
	b, err := json.Marshal(map[string]any{"refresh_token": "refresh-1", "owner_email": gcalOwner, "scopes": []string{"read"}})
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	return b
}

// --- tests ---------------------------------------------------------------

func TestDescriptorIsGreenReadOnly(t *testing.T) {
	d := New(fakeOAuth{}, &fakeAPI{}).Descriptor()
	if d.Name != "gcal" {
		t.Errorf("Name = %q, want gcal", d.Name)
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
	c := New(fakeOAuth{refresh: "refresh-1", access: "access-1"}, &fakeAPI{owner: gcalOwner})
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	auth, err := c.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	var st googleconn.AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		t.Fatalf("auth is not AuthState json: %v", err)
	}
	if st.RefreshToken != "refresh-1" || st.Owner != gcalOwner {
		t.Errorf("AuthState = %+v, want refresh-1 / %s", st, gcalOwner)
	}
}

func TestSyncInitialBackfillAnchorsCursorAndCaptures(t *testing.T) {
	api := &fakeAPI{
		owner:        gcalOwner,
		initialToken: "sync-abc",
		initial: [][]byte{
			eventJSON(t, "evt-1", "confirmed", "Kickoff", "2026-07-16T10:00:00Z", gcalOwner, "client@acme.com"),
			eventJSON(t, "evt-2", "confirmed", "Demo", "2026-07-16T11:00:00Z", gcalOwner, "buyer@beta.io"),
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
	if sink.recs[0].Source != "gcal:evt-1" || sink.recs[0].CapturedBy != "connector:gcal" {
		t.Errorf("provenance = (%q,%q), want (gcal:evt-1, connector:gcal)", sink.recs[0].Source, sink.recs[0].CapturedBy)
	}
	if tok, _ := parseCursor(cur); tok != "sync-abc" {
		t.Errorf("cursor syncToken = %q, want sync-abc (anchored on initial)", tok)
	}
	if api.incrementalCalls != 0 {
		t.Errorf("initial backfill must not call incremental, got %d", api.incrementalCalls)
	}
}

func TestSyncIncrementalUsesSyncTokenAndAdvancesCursor(t *testing.T) {
	api := &fakeAPI{
		owner:      gcalOwner,
		deltaToken: "sync-next",
		delta:      [][]byte{eventJSON(t, "evt-3", "confirmed", "Follow-up", "2026-07-17T09:00:00Z", gcalOwner, "client@acme.com")},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{SyncToken: "sync-abc"})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].NaturalKey.SourceID != "evt-3" {
		t.Fatalf("want 1 record evt-3, got %+v", sink.recs)
	}
	if api.incrementalCalls != 1 || api.initialCalls != 0 {
		t.Errorf("incremental path should call incremental once, initial never; got incremental=%d initial=%d", api.incrementalCalls, api.initialCalls)
	}
	if tok, _ := parseCursor(cur); tok != "sync-next" {
		t.Errorf("cursor = %q, want advanced to sync-next", tok)
	}
}

func TestSyncTokenGoneFallsBackToInitial(t *testing.T) {
	api := &fakeAPI{
		owner:        gcalOwner,
		deltaErr:     ErrSyncTokenGone,
		initialToken: "sync-fresh",
		initial:      [][]byte{eventJSON(t, "evt-1", "confirmed", "Kickoff", "2026-07-16T10:00:00Z", gcalOwner, "client@acme.com")},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	prior, _ := json.Marshal(cursorState{SyncToken: "stale"})
	cur, err := c.Sync(context.Background(), authBytes(t), prior, sink)
	if err != nil {
		t.Fatalf("a too-old syncToken must not fail Sync: %v", err)
	}
	if len(sink.recs) != 1 {
		t.Fatalf("fallback should re-list, captured %d want 1", len(sink.recs))
	}
	if api.initialCalls != 1 {
		t.Errorf("fallback should call ListInitial once, got %d", api.initialCalls)
	}
	if tok, _ := parseCursor(cur); tok != "sync-fresh" {
		t.Errorf("cursor should re-anchor at the fresh token, got %q", tok)
	}
}

func TestSyncSkipsAllInternalMeetings(t *testing.T) {
	api := &fakeAPI{
		owner:        gcalOwner,
		initialToken: "sync-abc",
		initial: [][]byte{
			eventJSON(t, "internal", "confirmed", "Standup", "2026-07-16T09:00:00Z", gcalOwner, gcalOwner, "peer@myco.com"),
			eventJSON(t, "external", "confirmed", "Demo", "2026-07-16T11:00:00Z", gcalOwner, "client@acme.com"),
		},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	if _, err := c.Sync(context.Background(), authBytes(t), nil, sink); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].NaturalKey.SourceID != "external" {
		t.Fatalf("an all-internal meeting must produce zero rows; got %+v", sink.recs)
	}
}

func TestSyncPropagatesSinkSkipWithoutFailing(t *testing.T) {
	api := &fakeAPI{
		owner:        gcalOwner,
		initialToken: "sync-abc",
		initial:      [][]byte{eventJSON(t, "evt-1", "confirmed", "Demo", "2026-07-16T11:00:00Z", gcalOwner, "client@acme.com")},
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	// A Sink that returns ErrSkip (e.g. the RC-2 personal-mail gate) must not
	// fail the pull — the connector counts it and moves on.
	if _, err := c.Sync(context.Background(), authBytes(t), nil, skipSink{}); err != nil {
		t.Fatalf("a Sink ErrSkip must not fail Sync, got %v", err)
	}
}

func TestSyncUnreadableCursorStopsWithoutBackfill(t *testing.T) {
	api := &fakeAPI{owner: gcalOwner, initialToken: "sync-abc"}
	c := New(fakeOAuth{access: "access-1"}, api)
	if _, err := c.Sync(context.Background(), authBytes(t), connector.Cursor("}not json{"), &recordingSink{}); err == nil {
		t.Fatal("an unreadable non-empty cursor must fail rather than silently re-backfill")
	}
	if api.initialCalls != 0 {
		t.Errorf("a corrupt cursor must not trigger a backfill, got %d initial calls", api.initialCalls)
	}
}

func TestNormalizeSkipsCancelledAndInternal(t *testing.T) {
	c := New(fakeOAuth{}, &fakeAPI{})
	c.owner = gcalOwner

	cancelled := eventJSON(t, "c1", "cancelled", "Off", "2026-07-16T09:00:00Z", gcalOwner, "client@acme.com")
	if _, err := c.Normalize(context.Background(), cancelled); !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("want ErrSkip for a cancelled event, got %v", err)
	}

	internal := eventJSON(t, "i1", "confirmed", "1:1", "2026-07-16T09:00:00Z", gcalOwner, gcalOwner, "peer@myco.com")
	if _, err := c.Normalize(context.Background(), internal); !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("want ErrSkip for an all-internal event, got %v", err)
	}

	keep := eventJSON(t, "k1", "confirmed", "Demo", "2026-07-16T11:00:00Z", gcalOwner, "client@acme.com")
	recs, err := c.Normalize(context.Background(), keep)
	if err != nil {
		t.Fatalf("Normalize a normal meeting: %v", err)
	}
	if len(recs) != 1 || recs[0].Source != "gcal:k1" {
		t.Fatalf("want 1 record gcal:k1, got %+v", recs)
	}
}

func TestAuthenticateRejectsMissingCode(t *testing.T) {
	c := New(fakeOAuth{}, &fakeAPI{})
	req, err := AuthRequestFrom("", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	if _, err := c.Authenticate(context.Background(), req); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for a missing code, got %v", err)
	}
}

func TestHealthCheckVerifiesTokenAndCalendar(t *testing.T) {
	c := New(fakeOAuth{access: "access-1"}, &fakeAPI{owner: gcalOwner})
	if err := c.HealthCheck(context.Background(), authBytes(t)); err != nil {
		t.Fatalf("HealthCheck on a live connection: %v", err)
	}
	if err := c.HealthCheck(context.Background(), connector.Auth("}bad{")); err == nil {
		t.Fatal("HealthCheck must fail on a malformed auth bundle")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cur := marshalCursor("sync-xyz")
	tok, err := parseCursor(cur)
	if err != nil {
		t.Fatalf("parseCursor: %v", err)
	}
	if tok != "sync-xyz" {
		t.Errorf("round-trip token = %q, want sync-xyz", tok)
	}
	if empty, err := parseCursor(nil); err != nil || empty != "" {
		t.Errorf("empty cursor = (%q,%v), want (\"\", nil)", empty, err)
	}
}

// skipSink returns connector.ErrSkip on every Upsert — the exclusion-gate shape.
type skipSink struct{}

func (skipSink) Upsert(context.Context, connector.NormalizedRecord) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, connector.ErrSkip
}
