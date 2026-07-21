// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// SourceLagByClass's own real-Postgres proof: the fleet-wide, rls-exempt
// walk over every overlay-mode workspace (metrics.go's own doc — it has
// no one workspace's request context to scope a WithWorkspaceTx to)
// needs a real workspace.x_sor_mode='overlay' row and a real
// overlay_mirror row to fold over, so this is integration-only exactly
// like DueOverlayConnections' own suite.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

func TestSourceLagByClassReportsTheOldestWatermarkPerClass(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	ms := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, ms)

	// Flip this workspace into overlay mode — SourceLagByClass's own
	// fleet query filters on x_sor_mode='overlay', the same condition
	// Connect flips (connection.go).
	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-token"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fixedModified := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := ms.Ingest(ctx, Record{
		ObjectClass: "person", ExternalID: "1", Fields: map[string]any{"first_name": "Ada"}, ModifiedAt: fixedModified,
	}); err != nil {
		t.Fatalf("ingesting the person fixture: %v", err)
	}
	if err := ms.Ingest(ctx, Record{
		ObjectClass: "person", ExternalID: "2", Fields: map[string]any{"first_name": "Bob"}, ModifiedAt: fixedModified,
	}); err != nil {
		t.Fatalf("ingesting the second person fixture: %v", err)
	}

	// Ingest itself always stamps last_synced_at=now() (mirrorstore.go's
	// ingestSQL) — Record.ModifiedAt lands on updated_at_baseline instead.
	// To exercise SourceLagByClass's "report the OLDEST last_synced_at"
	// fold deterministically, set BOTH rows' last_synced_at to fixed
	// timestamps directly (external_id "1" the older of the two), the same
	// "assert against the real column via a raw workspace-scoped update"
	// pattern connection_integration_test.go's queryRowWS uses for reads.
	// With both watermarks fixed and the clock injected below, the reported
	// lag is exact — no wall-clock read, no fudge tolerance (T11).
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	oldSyncedAt := now.Add(-2 * time.Hour).Truncate(time.Millisecond)
	newSyncedAt := now.Add(-30 * time.Minute).Truncate(time.Millisecond)
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE overlay_mirror SET last_synced_at = $1 WHERE object_class = 'person' AND external_id = '1'`, oldSyncedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE overlay_mirror SET last_synced_at = $1 WHERE object_class = 'person' AND external_id = '2'`, newSyncedAt)
		return err
	}); err != nil {
		t.Fatalf("pinning the fixtures' last_synced_at: %v", err)
	}

	lag, err := SourceLagByClass(ctx, pool, func() time.Time { return now })
	if err != nil {
		t.Fatalf("SourceLagByClass: %v", err)
	}

	personLag, ok := lag["person"]
	if !ok {
		t.Fatalf("lag = %#v, want a \"person\" entry", lag)
	}
	// The reported lag must be against the OLDEST last_synced_at seen for
	// the class — the worst-case, never the freshest — so it is exactly
	// now-oldSyncedAt, not the smaller now-newSyncedAt.
	if want := now.Sub(oldSyncedAt); personLag != want {
		t.Fatalf("lag[person] = %v, want exactly %v (measured against the OLDEST record, not the newest)", personLag, want)
	}
}

// TestSourceLagByClassIgnoresNativeModeWorkspaces proves the fleet
// query's own filter (metrics.go's selectFleetSourceLagSQL is only ever
// run inside a workspace scope this function itself selected via
// x_sor_mode='overlay'): a workspace that never flipped to overlay mode
// is excluded from the fold even when its own overlay_mirror table
// somehow carries a row (MirrorStore.Ingest itself gates on nothing
// mode-related — the mode boundary here is SourceLagByClass's own
// workspace enumeration, not Ingest).
func TestSourceLagByClassIgnoresNativeModeWorkspaces(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t) // testWorkspaceCtx's own fixture never flips to overlay mode
	ms := NewMirrorStore(pool, noOwnerEmails{})
	if err := ms.Ingest(ctx, Record{
		ObjectClass: "native_probe", ExternalID: "99", Fields: map[string]any{}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	lag, err := SourceLagByClass(ctx, pool, func() time.Time { return now })
	if err != nil {
		t.Fatalf("SourceLagByClass: %v", err)
	}
	if _, ok := lag["native_probe"]; ok {
		t.Fatalf("lag = %#v, want no \"native_probe\" entry — its workspace never flipped to overlay mode", lag)
	}
}
