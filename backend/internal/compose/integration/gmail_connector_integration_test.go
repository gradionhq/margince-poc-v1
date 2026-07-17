// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Gmail capture connector end to end against a stubbed Google: connect
// (OAuth code→refresh token, sealed to the vault), then an incremental sync
// that fetches a message as RFC822 and lands it through the ONE Sink as a
// provenance-stamped gmail activity — idempotent on replay, cursor advancing.
// Google is a local httptest stub, so this exercises the real connector,
// Registry.Connect/SyncOnce, and Sink without a network or real credentials.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// gmailStub answers the handful of Google endpoints the connector calls with
// a single inbound message, so a first sync captures exactly one activity.
func gmailStub(t *testing.T, owner string) *httptest.Server {
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
	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"emailAddress": owner, "historyId": "1001"})
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"messages": []map[string]string{{"id": "m1"}}})
	})
	mux.HandleFunc("/watch", func(w http.ResponseWriter, _ *http.Request) {
		// A watch that expires 7 days out (Gmail's cap), as the ms-since-epoch
		// string Gmail returns — so a renewed connection is no longer "due".
		exp := time.Now().Add(7 * 24 * time.Hour).UnixMilli()
		writeJSON(w, map[string]any{"historyId": "1001", "expiration": strconv.FormatInt(exp, 10)})
	})
	mux.HandleFunc("/messages/m1", func(w http.ResponseWriter, _ *http.Request) {
		rfc822 := "From: Alice <alice@acme.com>\r\n" +
			"To: " + owner + "\r\n" +
			"Subject: Quote please\r\n" +
			"Date: Wed, 04 Jun 2026 08:00:00 +0000\r\n" +
			"Message-ID: <m1@acme.com>\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			"Please send pricing."
		writeJSON(w, map[string]any{"id": "m1", "raw": base64.RawURLEncoding.EncodeToString([]byte(rfc822))})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

//craft:ignore naked-any v is an arbitrary canned JSON response body for the stub
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors test stub write; an encode failure surfaces as the client-side decode error the assertion checks
	_ = json.NewEncoder(w).Encode(v)
}

func TestGmailConnectorSyncsAnActivity(t *testing.T) {
	e := setupSearch(t)
	const owner = "rep@ws.example"
	stub := gmailStub(t, owner)

	oauth := gmail.NewOAuth(gmail.OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: stub.URL + "/token"})
	api := gmail.NewAPI(stub.Client(), stub.URL)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(gmail.New(oauth, api))

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})

	// The callback's own path: exchange the code for an auth bundle, then
	// persist it under the granting human.
	authReq, err := gmail.AuthRequestFrom("the-code", "https://app.test/v1/connectors/gmail/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := gmail.New(oauth, api).Authenticate(context.Background(), authReq)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	connID, err := registry.Connect(grantCtx, "gmail", auth)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// Replay must be a no-op (idempotent on the RFC822 Message-ID).
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("SyncOnce replay: %v", err)
	}

	var activities int
	var capturedBy, sourceID string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE source_system = 'gmail'`).Scan(&activities); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT captured_by, source_id FROM activity WHERE source_system = 'gmail'`).Scan(&capturedBy, &sourceID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if activities != 1 {
		t.Fatalf("gmail activities = %d, want 1 (idempotent across the replay)", activities)
	}
	if capturedBy != "connector:gmail" || sourceID != "m1@acme.com" {
		t.Fatalf("provenance wrong: captured_by=%q source_id=%q", capturedBy, sourceID)
	}

	// The cursor advanced to the mailbox historyId anchored at first sync.
	var cursor []byte
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT sync_cursor FROM capture_connection WHERE id = $1`, connID).Scan(&cursor)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cursor) == 0 || string(cursor) == "" {
		t.Fatalf("cursor did not advance: %q", cursor)
	}

	// --- the standing-connection surface: list, fleet due-poll, disconnect ---
	views, err := registry.Connections(grantCtx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(views) != 1 || views[0].Provider != "gmail" || views[0].Status != "connected" {
		t.Fatalf("Connections = %+v, want one connected gmail", views)
	}

	// Pacing (ADR-0063): the sync that just succeeded scheduled the next one
	// an interval out, so the connection is NOT due right now — a frequent
	// dispatcher scan never means frequent provider calls.
	due, err := registry.DueConnections(context.Background(), "gmail")
	if err != nil {
		t.Fatalf("DueConnections: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("DueConnections right after a successful sync = %+v, want none (paced out)", due)
	}
	// Once the pacing clock passes, the same connection is due again.
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE capture_sync_state SET next_sync_at = now() - interval '1 second' WHERE connection_id = $1`, connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	due, err = registry.DueConnections(context.Background(), "gmail")
	if err != nil {
		t.Fatalf("DueConnections: %v", err)
	}
	if len(due) != 1 || due[0].ID != connID {
		t.Fatalf("DueConnections past the pacing clock = %+v, want the one connection %s", due, connID)
	}

	if err := registry.Disconnect(grantCtx, "gmail"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	after, err := registry.Connections(grantCtx)
	if err != nil {
		t.Fatalf("Connections after disconnect: %v", err)
	}
	if len(after) != 1 || after[0].Status != "disconnected" {
		t.Fatalf("after disconnect Connections = %+v, want status disconnected", after)
	}
	if due2, _ := registry.DueConnections(context.Background(), "gmail"); len(due2) != 0 {
		t.Fatalf("DueConnections after disconnect = %+v, want empty (poller skips revoked)", due2)
	}
}
