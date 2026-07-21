// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The overlay sync-health metrics surface (design.md §4.7): three
// in-process atomic counters (the mirror's inbound sync rate, its
// conflict rate, and its deletion rate — Prometheus counters, exposed as
// *_total and rated client-side, the same shape
// platform/events.PublishedTotal already establishes for the outbox
// relay) plus SourceLagByClass, a fleet-wide
// staleness read the /metrics endpoint drives (it has no one workspace's
// request context to scope a WithWorkspaceTx to, so it walks the fleet
// the same rls-exempt way DueOverlayConnections/jobs.go's
// liveWorkspaces do).

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mirrorSyncedTotal counts every mirror row that actually landed via
// Ingest (RowsAffected>0 — a guard-held no-op does not count as a sync)
// since process start — the inbound "sync_rate" counter (design.md
// §4.7), fed by the poller/backfill lanes, which converge on the one
// Ingest (mirrorstore.go's own doc).
var mirrorSyncedTotal atomic.Uint64

// MirrorSyncedTotal answers the process's inbound-sync counter.
func MirrorSyncedTotal() uint64 { return mirrorSyncedTotal.Load() }

// mirrorConflictTotal counts every mirror.conflict event Reconcile
// (reconcile.go) has emitted since process start — the "conflict rate"
// counter (design.md §4.7, OVA-EVT-1).
var mirrorConflictTotal atomic.Uint64

// MirrorConflictTotal answers the process's mirror-conflict counter.
func MirrorConflictTotal() uint64 { return mirrorConflictTotal.Load() }

// mirrorDeletedTotal counts every mirror.deleted event PurgeRecord
// (mirrordeletion.go) has emitted since process start — one per
// incumbent-deleted record actually purged from the mirror, the
// removal-rate counterpart to the conflict counter.
var mirrorDeletedTotal atomic.Uint64

// MirrorDeletedTotal answers the process's mirror-deletion counter.
func MirrorDeletedTotal() uint64 { return mirrorDeletedTotal.Load() }

// selectFleetSourceLagSQL answers, for one already-workspace-scoped tx,
// the OLDEST last_synced_at per object class — the worst-case staleness
// SourceLagByClass folds across the whole fleet (an older last_synced_at
// anywhere is a worse lag than a newer one anywhere else for the SAME
// object class).
const selectFleetSourceLagSQL = `
SELECT object_class, min(last_synced_at) FROM overlay_mirror GROUP BY object_class`

// SourceLagByClass fleet-walks every overlay-mode workspace and answers,
// per CANONICAL object class, now minus the oldest last_synced_at seen
// anywhere in the fleet for that class — a single worst-case number per
// class (design.md §4.7's "source_lag per object class"), not one series
// per tenant: branch 1's fleet is small enough that an operator watching
// for "is any workspace's sync stuck" is better served by the worst case
// than by a label set with no current size bound. A workspace whose own
// read fails is logged into the joined error but does not stop the rest
// of the fleet from being folded in, the same posture
// DueOverlayConnections already takes.
func SourceLagByClass(ctx context.Context, pool *pgxpool.Pool, now func() time.Time) (map[string]time.Duration, error) {
	// rls-exempt: fleet enumeration — workspace is not itself workspace-scoped.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL AND x_sor_mode = 'overlay' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("overlay: listing overlay-mode workspaces for source-lag: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, fmt.Errorf("overlay: collecting overlay-mode workspace ids: %w", err)
	}

	oldest := make(map[string]time.Time)
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			pageRows, err := tx.Query(wsCtx, selectFleetSourceLagSQL)
			if err != nil {
				return err
			}
			defer pageRows.Close()
			for pageRows.Next() {
				var objectClass string
				var lastSyncedAt time.Time
				if err := pageRows.Scan(&objectClass, &lastSyncedAt); err != nil {
					return err
				}
				if prior, ok := oldest[objectClass]; !ok || lastSyncedAt.Before(prior) {
					oldest[objectClass] = lastSyncedAt
				}
			}
			return pageRows.Err()
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("overlay: reading source lag for workspace %s: %w", wsID, err))
		}
	}

	lag := make(map[string]time.Duration, len(oldest))
	nowT := now()
	for objectClass, lastSyncedAt := range oldest {
		lag[objectClass] = nowT.Sub(lastSyncedAt)
	}
	return lag, errs
}
