// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// A sync and a backfill page both read a connection, spend real time at the
// provider, and write back afterwards. Disconnect is instant. These pin what
// happens when the human wins that race: the deferred write lands on nothing,
// and nothing claims the withdrawn connection was healthy.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// interruptedConnector runs the caller's interruption at the moment the
// provider would be answering — the deterministic stand-in for "the human hit
// Disconnect while the sync was out at Gmail".
type interruptedConnector struct {
	*pagedConnector
	duringSync func()
	duringPage func()
}

func (c *interruptedConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "gmail", Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (c *interruptedConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	if c.duringSync != nil {
		c.duringSync()
	}
	return connector.Cursor(`{"email":"owner@myco.example"}`), nil
}

func (c *interruptedConnector) BackfillPage(ctx context.Context, auth connector.Auth, after time.Time, pageToken string, sink connector.Sink) (connector.BackfillPageResult, error) {
	if c.duringPage != nil {
		c.duringPage()
	}
	return c.pagedConnector.BackfillPage(ctx, auth, after, pageToken, sink)
}

// readConnectionSyncState reads the two facts a superseded cycle must not
// touch: the connection's watermark and the sidecar's health verdict.
func readConnectionSyncState(t *testing.T, e *integration.SearchEnv, connID ids.UUID) (status string, cursor []byte, lastSynced *time.Time) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT c.status, c.sync_cursor, s.last_synced_at
			FROM capture_connection c
			LEFT JOIN capture_sync_state s ON s.connection_id = c.id
			WHERE c.id = $1`, connID).Scan(&status, &cursor, &lastSynced)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, cursor, lastSynced
}

func TestASyncDisconnectedMidFlightCommitsNothing(t *testing.T) {
	e := integration.SetupSearch(t)
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &interruptedConnector{pagedConnector: &pagedConnector{messages: 25, pageSize: 10}}
	registry.Register(fake)

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fake.duringSync = func() {
		if err := registry.Disconnect(grantCtx, "gmail"); err != nil {
			t.Errorf("mid-sync disconnect: %v", err)
		}
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	// Nothing failed — the work was superseded — so the caller is not told a
	// sync went wrong. It simply did not count.
	if err := registry.SyncOnce(wsCtx, connID); err != nil {
		t.Fatalf("SyncOnce over a disconnected connection: %v, want a clean return", err)
	}

	status, cursor, lastSynced := readConnectionSyncState(t, e, connID)
	if status != "disconnected" {
		t.Fatalf("status = %s, want disconnected — the human's withdrawal stands", status)
	}
	if len(cursor) != 0 {
		t.Fatalf("sync_cursor = %q, want unwritten — the withdrawn grant read that mail, and the row must not remember it", cursor)
	}
	if lastSynced != nil {
		t.Fatal("the sidecar recorded a successful sync against a connection its human had already disconnected")
	}
}

func TestABackfillPageDisconnectedMidFlightCommitsNothing(t *testing.T) {
	e := integration.SetupSearch(t)
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &interruptedConnector{pagedConnector: &pagedConnector{messages: 25, pageSize: 10}}
	registry.Register(fake)

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	run, err := registry.StartBackfill(grantCtx, "gmail", ids.From[ids.UserKind](e.Rep1), 6, 25, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	fake.duringPage = func() {
		if err := registry.Disconnect(grantCtx, "gmail"); err != nil {
			t.Errorf("mid-page disconnect: %v", err)
		}
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	done, completed, retryAfter, err := registry.RunBackfillStep(wsCtx, run.ID)
	if err != nil {
		t.Fatalf("RunBackfillStep over a disconnected connection: %v, want a clean return", err)
	}
	if !done || completed || retryAfter != 0 {
		t.Fatalf("done=%v completed=%v retryAfter=%v, want the pager stopped with nothing completed", done, completed, retryAfter)
	}

	status, scanned, captured, cursor := readBackfillRow(t, e, run.ID)
	if scanned != 0 || captured != 0 || len(cursor) != 0 {
		t.Fatalf("the superseded page was credited: scanned=%d captured=%d cursor=%q", scanned, captured, cursor)
	}
	// The live-run index would otherwise hold this run forever: no worker pages
	// a run whose every commit is fenced off, and every later start for the
	// connection would answer 409.
	if status != "cancelled" {
		t.Fatalf("status = %s, want cancelled — a run whose connection went away is over, not stuck running", status)
	}
}
