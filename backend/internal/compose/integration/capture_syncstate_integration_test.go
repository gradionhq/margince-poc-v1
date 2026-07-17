// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ADR-0063 scheduling state machine over a real migrated Postgres: a
// transient failure backs off but never kills the connection, a rate limit
// honors Retry-After, persistent failure degrades to a daily probe that one
// success heals, and an auth failure parks the row for its human — with the
// error DETAIL landing in system_log and only the class on the sidecar row.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// moodyConnector fails or succeeds on command — the provider weather dial.
type moodyConnector struct {
	name string
	err  error
}

func (m *moodyConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: m.name, Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (m *moodyConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (m *moodyConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	if m.err != nil {
		return cursor, m.err
	}
	return connector.Cursor(`{"n":1}`), nil
}

func (m *moodyConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (m *moodyConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

type syncStateRow struct {
	next     time.Time
	failures int
	class    *string
}

func readSyncState(t *testing.T, e *searchEnv, connID ids.UUID) (string, syncStateRow) {
	t.Helper()
	var status string
	var row syncStateRow
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT status FROM capture_connection WHERE id = $1`, connID).Scan(&status); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `
			SELECT next_sync_at, consecutive_failures, last_error_class
			FROM capture_sync_state WHERE connection_id = $1`, connID).
			Scan(&row.next, &row.failures, &row.class)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, row
}

func TestSyncFailureNeverKillsAConnection(t *testing.T) {
	e := setupSearch(t)
	moody := &moodyConnector{name: "gmail"}
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(moody)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	t.Run("rate limit honors Retry-After and stays connected", func(t *testing.T) {
		moody.err = &connector.RateLimitedError{RetryAfter: 30 * time.Minute}
		if err := registry.SyncOnce(wsCtx, connID); err == nil {
			t.Fatal("SyncOnce must surface the sync error")
		}
		status, row := readSyncState(t, e, connID)
		if status != "connected" {
			t.Fatalf("status = %s, want connected — a rate limit is weather, not death", status)
		}
		if row.class == nil || *row.class != "rate_limited" {
			t.Fatalf("class = %v, want rate_limited", row.class)
		}
		if until := time.Until(row.next); until < 25*time.Minute {
			t.Fatalf("next_sync_at only %s out, want ≥ the provider's 30m Retry-After (minus slack)", until)
		}
	})

	t.Run("transient failure backs off, success heals and paces", func(t *testing.T) {
		moody.err = connector.ErrUnreachable
		if err := registry.SyncOnce(wsCtx, connID); err == nil {
			t.Fatal("SyncOnce must surface the sync error")
		}
		status, row := readSyncState(t, e, connID)
		if status != "connected" || row.failures != 2 {
			t.Fatalf("status=%s failures=%d, want connected/2", status, row.failures)
		}

		moody.err = nil
		forceDue(t, e, connID)
		if err := registry.SyncOnce(wsCtx, connID); err != nil {
			t.Fatalf("healthy SyncOnce: %v", err)
		}
		status, row = readSyncState(t, e, connID)
		if status != "connected" || row.failures != 0 || row.class != nil {
			t.Fatalf("after success: status=%s failures=%d class=%v, want a clean slate", status, row.failures, row.class)
		}
		if !row.next.After(time.Now()) {
			t.Fatal("a successful sync must pace the next one into the future")
		}
	})

	t.Run("persistent failure degrades to a daily probe that heals", func(t *testing.T) {
		// Fast-forward the ladder to the brink instead of failing 20 times.
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(),
				`UPDATE capture_sync_state SET consecutive_failures = 19 WHERE connection_id = $1`, connID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		moody.err = connector.ErrUnreachable
		if err := registry.SyncOnce(wsCtx, connID); err == nil {
			t.Fatal("SyncOnce must surface the sync error")
		}
		status, row := readSyncState(t, e, connID)
		if status != "error" {
			t.Fatalf("status = %s, want error after 20 consecutive failures", status)
		}
		if until := time.Until(row.next); until < 20*time.Hour {
			t.Fatalf("degraded probe only %s out, want ~daily", until)
		}

		// Degraded is NOT dead: forced due, the error row is still swept…
		forceDue(t, e, connID)
		due, err := registry.DueConnections(context.Background(), "gmail")
		if err != nil {
			t.Fatalf("DueConnections: %v", err)
		}
		if len(due) != 1 || due[0].ID != connID {
			t.Fatalf("DueConnections = %+v, want the degraded connection — error is probed, never tombstoned", due)
		}
		// …and one success flips it back to connected.
		moody.err = nil
		if err := registry.SyncOnce(wsCtx, connID); err != nil {
			t.Fatalf("healing SyncOnce: %v", err)
		}
		if status, _ := readSyncState(t, e, connID); status != "connected" {
			t.Fatalf("status = %s, want connected — one success heals", status)
		}
	})

	t.Run("auth failure parks the connection for its human", func(t *testing.T) {
		moody.err = connector.ErrAuthRejected
		forceDue(t, e, connID)
		if err := registry.SyncOnce(wsCtx, connID); err == nil {
			t.Fatal("SyncOnce must surface the sync error")
		}
		status, row := readSyncState(t, e, connID)
		if status != "reauth_required" {
			t.Fatalf("status = %s, want reauth_required — auth needs the human, not a retry", status)
		}
		if row.class == nil || *row.class != "auth" {
			t.Fatalf("class = %v, want auth", row.class)
		}
		forceDue(t, e, connID)
		if due, _ := registry.DueConnections(context.Background(), "gmail"); len(due) != 0 {
			t.Fatalf("DueConnections = %+v, want none — a parked connection is not swept", due)
		}

		// Reconnect (the OAuth callback path) un-parks it with a clean ladder.
		if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("fresh")); err != nil {
			t.Fatalf("reconnect: %v", err)
		}
		status, row = readSyncState(t, e, connID)
		if status != "connected" || row.failures != 0 {
			t.Fatalf("after reconnect: status=%s failures=%d, want connected/0", status, row.failures)
		}
	})

	// The detail landed in the ledger; the row never carries it.
	var ledger int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM system_log WHERE action = 'capture_sync_error'`).Scan(&ledger)
	})
	if err != nil {
		t.Fatal(err)
	}
	if ledger < 3 {
		t.Fatalf("system_log capture_sync_error rows = %d, want one per recorded failure", ledger)
	}
}

// forceDue moves the connection's pacing clock into the past.
func forceDue(t *testing.T, e *searchEnv, connID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE capture_sync_state SET next_sync_at = now() - interval '1 second' WHERE connection_id = $1`, connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
