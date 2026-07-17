// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Google Calendar capture connector end to end against a stubbed Google:
// connect (OAuth code→refresh token, sealed to the vault), then an incremental
// sync that lists calendar events and lands the external meeting through the
// ONE Sink as a provenance-stamped gcal activity — while the all-internal
// meeting (formulas §20) produces zero rows. Idempotent on replay, cursor
// (syncToken) advancing. Google is a local httptest stub, so this exercises the
// real connector, Registry.Connect/SyncOnce, and Sink without a network.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// gcalStub answers the Calendar endpoints the connector calls with one external
// meeting (captured) and one all-internal meeting (skipped), plus a syncToken —
// so a first sync captures exactly one activity and the cursor advances.
func gcalStub(t *testing.T, owner string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		//craft:ignore swallowed-errors test stub; ParseForm on the recorded request can't fail
		_ = r.ParseForm()
		body := map[string]any{"access_token": "access-tok", "expires_in": 3599}
		if r.Form.Get("grant_type") == "authorization_code" {
			body["refresh_token"] = "refresh-tok"
		}
		writeJSON(w, body)
	})
	mux.HandleFunc("/calendars/primary", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": owner})
	})
	mux.HandleFunc("/calendars/primary/events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"items": []map[string]any{
				{
					"id": "evt-ext", "status": "confirmed", "summary": "Customer demo",
					"start":     map[string]string{"dateTime": "2026-07-16T10:00:00Z"},
					"organizer": map[string]string{"email": owner},
					"attendees": []map[string]string{{"email": owner}, {"email": "buyer@acme.com"}},
				},
				{
					// All attendees share the owner's domain → internal → zero rows.
					"id": "evt-int", "status": "confirmed", "summary": "Team standup",
					"start":     map[string]string{"dateTime": "2026-07-16T09:00:00Z"},
					"organizer": map[string]string{"email": owner},
					"attendees": []map[string]string{{"email": owner}, {"email": "peer@ws.example"}},
				},
			},
			"nextSyncToken": "synctok-1",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGcalConnectorSyncsExternalMeetingAndSkipsInternal(t *testing.T) {
	e := setupSearch(t)
	const owner = "rep@ws.example"
	stub := gcalStub(t, owner)

	// gcal reuses the Google OAuth2 client (the gmail.OAuth value satisfies
	// gcal's structurally-identical seam), pointed at the stub's token endpoint.
	oauth := gmail.NewOAuth(gmail.OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: stub.URL + "/token"})
	api := gcal.NewAPI(stub.Client(), stub.URL)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(gcal.New(oauth, api))

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})

	authReq, err := gcal.AuthRequestFrom("the-code", "https://app.test/v1/connectors/gcal/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := gcal.New(oauth, api).Authenticate(context.Background(), authReq)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	connID, err := registry.Connect(grantCtx, "gcal", auth)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// Replay must be a no-op (idempotent on the event id).
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("SyncOnce replay: %v", err)
	}

	var activities int
	var capturedBy, sourceID, kind string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE source_system = 'gcal'`).Scan(&activities); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT captured_by, source_id, kind FROM activity WHERE source_system = 'gcal'`).Scan(&capturedBy, &sourceID, &kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one row: the external meeting. The all-internal one yields zero.
	if activities != 1 {
		t.Fatalf("gcal activities = %d, want 1 (external captured, internal skipped, idempotent replay)", activities)
	}
	if capturedBy != "connector:gcal" || sourceID != "evt-ext" || kind != "meeting" {
		t.Fatalf("row wrong: captured_by=%q source_id=%q kind=%q", capturedBy, sourceID, kind)
	}

	// The cursor advanced to the returned syncToken.
	var cursor []byte
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT sync_cursor FROM capture_connection WHERE id = $1`, connID).Scan(&cursor)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cursor) == 0 {
		t.Fatalf("cursor did not advance: %q", cursor)
	}

	assertGcalConnectionSurface(grantCtx, t, registry, connID)
}

// assertGcalConnectionSurface exercises the standing-connection surface behind
// listConnectors / the fleet due-poll / disconnectConnector for the one gcal
// connection: it lists connected, the poller finds it due, and after disconnect
// the poller skips it.
func assertGcalConnectionSurface(grantCtx context.Context, t *testing.T, registry *capture.Registry, connID ids.UUID) {
	t.Helper()
	views, err := registry.Connections(grantCtx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(views) != 1 || views[0].Provider != "gcal" || views[0].Status != "connected" {
		t.Fatalf("Connections = %+v, want one connected gcal", views)
	}

	due, err := registry.DueConnections(context.Background(), "gcal")
	if err != nil {
		t.Fatalf("DueConnections: %v", err)
	}
	if len(due) != 1 || due[0].ID != connID {
		t.Fatalf("DueConnections = %+v, want the one connection %s", due, connID)
	}

	if err := registry.Disconnect(grantCtx, "gcal"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if due2, _ := registry.DueConnections(context.Background(), "gcal"); len(due2) != 0 {
		t.Fatalf("DueConnections after disconnect = %+v, want empty (poller skips revoked)", due2)
	}
}
