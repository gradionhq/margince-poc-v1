// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Pub/Sub push webhook end to end over the real mux: a notification for
// a connected mailbox zeroes its pacing clock and enqueues its sync job; a
// wrong token is refused before any work; a mailbox nobody connected is a
// 204 no-op (Pub/Sub must stop retrying — nothing here a redelivery fixes).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func pushBody(t *testing.T, email string) []byte {
	t.Helper()
	note, err := json.Marshal(map[string]string{"emailAddress": email, "historyId": "4711"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(map[string]any{
		"message": map[string]any{"data": base64.StdEncoding.EncodeToString(note)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestGmailPushWebhookRoutesToTheConnection(t *testing.T) {
	e := SetupSearch(t)
	const mailbox = "push-owner@ws.example"

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&moodyConnector{name: "gmail"})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	var seededNext time.Time
	// The connector's cursor carries the provider-owned mailbox identity —
	// exactly what a real gmail sync writes — and the pacing clock sits in
	// the future so only the push can make the connection due.
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			UPDATE capture_connection SET sync_cursor = $2 WHERE id = $1`,
			connID, fmt.Sprintf(`{"history_id":"1000","email":%q}`, mailbox)); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `
			INSERT INTO capture_sync_state (connection_id, workspace_id, next_sync_at)
			SELECT id, workspace_id, now() + interval '1 hour' FROM capture_connection WHERE id = $1
			RETURNING next_sync_at`,
			connID).Scan(&seededNext)
	})
	if err != nil {
		t.Fatal(err)
	}

	ApplyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatal(err)
	}
	const token = "push-secret"
	handler := compose.New(e.Pool, quiet, compose.WithGmailPush(inserter, compose.GmailPushConfig{Token: token}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	post := func(url string, body []byte) int {
		t.Helper()
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		return code
	}

	t.Run("wrong token is refused before any work", func(t *testing.T) {
		// The chassis's uniform admission failure (design §6.5): 401, not
		// Gmail's old 403 — Pub/Sub re-mints and retries either way.
		if code := post(srv.URL+"/webhooks/gmail?token=wrong", pushBody(t, mailbox)); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("a push zeroes the pacing clock and enqueues the sync", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail?token="+token, pushBody(t, mailbox)); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", code)
		}
		var next time.Time
		var jobRows int
		err := database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
			if err := tx.QueryRow(context.Background(), `
				SELECT next_sync_at FROM capture_sync_state WHERE connection_id = $1`, connID).Scan(&next); err != nil {
				return err
			}
			// river_job is not tenant-scoped; count the enqueued sync for
			// this connection.
			return tx.QueryRow(context.Background(), `
				SELECT count(*) FROM river_job
				WHERE kind = 'capture_sync' AND args->>'connection_id' = $1`, connID.String()).Scan(&jobRows)
		})
		if err != nil {
			t.Fatal(err)
		}
		if !next.Before(seededNext) {
			t.Fatalf("next_sync_at = %s, want moved earlier than the seeded %s — the push must make the connection due", next, seededNext)
		}
		if jobRows != 1 {
			t.Fatalf("capture_sync jobs for the connection = %d, want exactly 1", jobRows)
		}
	})

	t.Run("a redelivery cannot double-enqueue", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail?token="+token, pushBody(t, mailbox)); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", code)
		}
		var jobRows int
		err := database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `
				SELECT count(*) FROM river_job
				WHERE kind = 'capture_sync' AND args->>'connection_id' = $1`, connID.String()).Scan(&jobRows)
		})
		if err != nil {
			t.Fatal(err)
		}
		if jobRows != 1 {
			t.Fatalf("capture_sync jobs after redelivery = %d, want still 1 (unique while incomplete)", jobRows)
		}
	})

	t.Run("malformed input is poison, not retried", func(t *testing.T) {
		// A malformed body is 2xx (Poison, design §6.5), not Gmail's old
		// 400: the same bytes would fail identically on redelivery, so a
		// 4xx would only make Pub/Sub retry a payload that can never succeed.
		if code := post(srv.URL+"/webhooks/gmail?token="+token, []byte("not json")); code < 200 || code >= 300 {
			t.Fatalf("garbage body status = %d, want 2xx", code)
		}
		if code := post(srv.URL+"/webhooks/gmail?token="+token, []byte(`{"message":{"data":"%%%not-base64%%%"}}`)); code < 200 || code >= 300 {
			t.Fatalf("bad base64 status = %d, want 2xx", code)
		}
		empty := base64.StdEncoding.EncodeToString([]byte(`{"historyId":1}`))
		if code := post(srv.URL+"/webhooks/gmail?token="+token, []byte(`{"message":{"data":"`+empty+`"}}`)); code < 200 || code >= 300 {
			t.Fatalf("missing emailAddress status = %d, want 2xx", code)
		}
		resp, err := http.Get(srv.URL + "/webhooks/gmail?token=" + token)
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d, want 405", code)
		}
	})

	t.Run("an unknown mailbox is a 204 no-op", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail?token="+token, pushBody(t, "stranger@nowhere.example")); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 — Pub/Sub must stop retrying", code)
		}
	})
}
