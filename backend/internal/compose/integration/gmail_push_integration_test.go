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
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ensureRiverSchema applies River's schema once for this package process —
// the harness migrates core+custom only, and the enqueue path needs
// river_job (the same discipline the workqueue suite uses).
func ensureRiverSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	ownerPool, err := pgxpool.New(ctx, os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()
	var present bool
	if err := ownerPool.QueryRow(ctx, `SELECT to_regclass('public.river_migration') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("checking river schema: %v", err)
	}
	if present {
		return
	}
	if _, err := jobs.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("applying river schema: %v", err)
	}
}

func pushBody(t *testing.T, email string) []byte {
	t.Helper()
	note, err := json.Marshal(map[string]any{"emailAddress": email, "historyId": 4711})
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
	e := setupSearch(t)
	const mailbox = "push-owner@ws.example"

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&moodyConnector{name: "gmail"})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// The connector's cursor carries the provider-owned mailbox identity —
	// exactly what a real gmail sync writes — and the pacing clock sits in
	// the future so only the push can make the connection due.
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			UPDATE capture_connection SET sync_cursor = $2 WHERE id = $1`,
			connID, fmt.Sprintf(`{"history_id":"1000","email":%q}`, mailbox)); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO capture_sync_state (connection_id, workspace_id, next_sync_at)
			SELECT id, workspace_id, now() + interval '1 hour' FROM capture_connection WHERE id = $1`,
			connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	ensureRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatal(err)
	}
	const token = "push-secret"
	handler := compose.New(e.Pool, quiet, compose.WithGmailPush(inserter, token))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	post := func(url string, body []byte) int {
		t.Helper()
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		//craft:ignore swallowed-errors test response body close; the status code is the assertion
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	t.Run("wrong token is refused before any work", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail-push?token=wrong", pushBody(t, mailbox)); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", code)
		}
	})

	t.Run("a push zeroes the pacing clock and enqueues the sync", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail-push?token="+token, pushBody(t, mailbox)); code != http.StatusNoContent {
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
		if next.After(time.Now()) {
			t.Fatalf("next_sync_at = %s, want zeroed to now — the push must make the connection due", next)
		}
		if jobRows != 1 {
			t.Fatalf("capture_sync jobs for the connection = %d, want exactly 1", jobRows)
		}
	})

	t.Run("a redelivery cannot double-enqueue", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail-push?token="+token, pushBody(t, mailbox)); code != http.StatusNoContent {
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

	t.Run("an unknown mailbox is a 204 no-op", func(t *testing.T) {
		if code := post(srv.URL+"/webhooks/gmail-push?token="+token, pushBody(t, "stranger@nowhere.example")); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 — Pub/Sub must stop retrying", code)
		}
	})
}
