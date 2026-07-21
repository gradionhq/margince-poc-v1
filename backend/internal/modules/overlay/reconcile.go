// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file is the incremental reconcile poller (design.md §4.4): the
// always-on sweep that keeps the mirror converging on the incumbent's
// current state — branch 1's one continuous-sync trigger (the
// webhook-as-signal push lane is deferred to branch 1b). Every sync
// trigger converges on the ONE ingest MirrorStore.Ingest already owns
// (mirrorstore.go) — this file's own job is deciding, for every record
// the sweep observes, whether the mirror's PRIOR state was a genuine
// divergence worth surfacing as mirror.conflict, and metering the sweep
// against the shared OVB budget.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// Reconcile sweeps objectClass's records modified at or after since via
// inc.Modified, ingesting every page into ms and metering the sweep
// against the poller lane. objectClass is the INCUMBENT class name (the
// same seam rule Backfill/Modified obey, overlay/backfill.go's own doc
// comment) — the vocabulary inc.Modified itself takes — while the
// records it returns already carry the CANONICAL object class Reconcile
// reads back through ms.getRaw/ms.Ingest (the adapter's mapRecord
// translation, hubspot/adapter.go).
//
// For a record whose mirror row already existed and was NOT
// pending_sync (design.md §4.4: "Incumbent-wins applies to fresh/stale
// rows only"), an incoming value that actually lands (i.e. its
// ModifiedAt is genuinely newer than the stored baseline, so Ingest's
// own staleness guard lets the write through) is a real divergence: the
// incumbent changed a record Margince had already synced, so Reconcile
// emits mirror.conflict (OVA-EVT-1/AC-OV-8) alongside the overwrite — an
// observable trace of every incumbent-driven correction, not an alarm
// reserved for rare cases. A pending_sync row is left exactly as
// Ingest's own no-clobber-dirty guard protects it (mirrorstore.go's
// ingestSQL): Reconcile adds no additional logic to hold it back, and
// emits nothing for it — the dirty write stays the authority until
// branch 2's write-back drains it.
//
// The returned watermark is the latest ModifiedAt Reconcile observed
// across every record in every page swept, or since unchanged if the
// sweep saw nothing new — the caller (jobs.go's poller) persists it via
// MirrorStore.SaveReconcileWatermark so the next pass resumes forward,
// never rewinding.
//
// Two-mode keyset (design.md §4.4/§7): HubSpot Search honors only one
// sort, so a >10k-tied-timestamp block needs a second numeric-id-keyset
// mode which design.md §7 itself still lists as an open, spike-validate
// item — not built at this seam (the Incumbent seam's Modified(since,
// cursor) signature has no way to signal "switch mode" through to the
// adapter).
// Reconcile therefore drives the single always-available mode this seam
// exposes (a persisted-watermark sweep, keyset-paged via cursor within
// one watermark window) — the >10k-per-timestamp fallback stays a named,
// upstream-tracked gap rather than an invented behavior against an
// unconfirmed wire contract.
func Reconcile(ctx context.Context, inc Incumbent, ms *MirrorStore, meter *Meter, objectClass string, since time.Time) (time.Time, error) {
	watermark := since
	cursor := ""
	for {
		page, err := inc.Modified(ctx, objectClass, since, cursor)
		if err != nil {
			return watermark, fmt.Errorf("overlay: reconcile %s: sweeping modified records at cursor %q: %w", objectClass, cursor, err)
		}

		watermark, err = reconcilePage(ctx, ms, meter, objectClass, page, watermark)
		if err != nil {
			return watermark, err
		}

		if page.NextCursor == "" {
			return watermark, nil
		}
		cursor = page.NextCursor
	}
}

// reconcilePage lands every record of one Modified page, meters the
// page's cost against the poller lane, and answers the watermark
// advanced across those records (or watermark unchanged for an empty
// page) — split out of Reconcile's own loop so the pagination control
// flow and the per-page landing logic each read as one clear thing.
func reconcilePage(ctx context.Context, ms *MirrorStore, meter *Meter, objectClass string, page Page, watermark time.Time) (time.Time, error) {
	if len(page.Records) == 0 {
		return watermark, nil
	}
	if meter != nil {
		if err := meter.Consume(ctx, LanePoller, len(page.Records)); err != nil {
			return watermark, fmt.Errorf("overlay: reconcile %s: metering the poller lane: %w", objectClass, err)
		}
	}
	for _, rec := range page.Records {
		if err := reconcileOne(ctx, ms, rec); err != nil {
			return watermark, err
		}
		if rec.ModifiedAt.After(watermark) {
			watermark = rec.ModifiedAt
		}
	}
	return watermark, nil
}

// reconcileOne lands one incumbent record into the mirror and, when it
// diverged a pre-existing non-dirty row, emits mirror.conflict.
func reconcileOne(ctx context.Context, ms *MirrorStore, rec Record) error {
	prior, priorErr := ms.getRaw(ctx, rec.ObjectClass, rec.ExternalID)
	existed := priorErr == nil
	if priorErr != nil && !errors.Is(priorErr, apperrors.ErrNotFound) {
		return fmt.Errorf("overlay: reconcile: reading the prior mirror state of %s/%s: %w", rec.ObjectClass, rec.ExternalID, priorErr)
	}

	if err := ms.Ingest(ctx, rec); err != nil {
		return fmt.Errorf("overlay: reconcile: ingesting %s/%s: %w", rec.ObjectClass, rec.ExternalID, err)
	}

	// A divergence worth surfacing requires: the row already existed, it
	// was NOT protected as dirty (pending_sync — Ingest's own no-clobber
	// guard already held THAT case back with no write at all), and the
	// incoming value actually landed (strictly newer than the stored
	// baseline — the same predicate Ingest's staleness guard applies, so
	// this never emits a conflict for a stale page that changed nothing).
	if existed && prior.SyncState != syncStatePendingSync && rec.ModifiedAt.After(prior.UpdatedAtBaseline) {
		if err := emitMirrorConflict(ctx, ms, rec, prior); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileDeletions sweeps objectClass's incumbent deletion feed
// (inc.Deletions), purging every reported record from the mirror through
// MirrorStore.PurgeRecord (which also emits mirror.deleted, atomically,
// for each one the mirror actually held). objectClass is the INCUMBENT
// class name inc.Deletions takes (the same seam rule Reconcile obeys),
// while each Deletion it returns already carries the CANONICAL class
// PurgeRecord keys the mirror by. It is the removal counterpart to
// Reconcile: continuous sync converges the mirror on the incumbent's
// current state in BOTH directions — modified records land, deleted
// records leave — so an incumbent-side deletion stops being readable
// rather than lingering visible until disconnect.
//
// It full-scans the deletion feed from the epoch every pass rather than
// riding a watermark. HubSpot's archived feed is NOT archivedAt-ordered on
// the wire, so a watermark filter risks permanently skipping a record
// archived behind the cursor mid-sweep (its DeletedAt could sort before an
// advanced watermark and be filtered out forever). PurgeRecord is
// idempotent — an already-purged record is a no-op that emits nothing — so
// re-scanning the full archived set each pass is correct; bounding that
// scan's cost is a budget concern (branch-1b budget metering), not a
// correctness one. The incumbent adapter still pages the whole archived set
// regardless of the since it is passed, so the epoch costs no more than a
// watermark would have.
func ReconcileDeletions(ctx context.Context, inc Incumbent, ms *MirrorStore, meter *Meter, objectClass string) error {
	cursor := ""
	for {
		page, err := inc.Deletions(ctx, objectClass, time.Time{}, cursor)
		if err != nil {
			return fmt.Errorf("overlay: reconcile %s: sweeping deletions at cursor %q: %w", objectClass, cursor, err)
		}
		if err := reconcileDeletionPage(ctx, ms, meter, objectClass, page); err != nil {
			return err
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// reconcileDeletionPage purges every record of one deletion page and meters
// the page's cost against the poller lane — the deletion-feed sibling of
// reconcilePage. PurgeRecord owns the per-record purge+event atomicity, so
// this only fans the page out.
func reconcileDeletionPage(ctx context.Context, ms *MirrorStore, meter *Meter, objectClass string, page DeletionPage) error {
	if len(page.Deletions) == 0 {
		return nil
	}
	if meter != nil {
		if err := meter.Consume(ctx, LanePoller, len(page.Deletions)); err != nil {
			return fmt.Errorf("overlay: reconcile %s: metering the deletion sweep: %w", objectClass, err)
		}
	}
	for _, del := range page.Deletions {
		if _, err := ms.PurgeRecord(ctx, del); err != nil {
			return fmt.Errorf("overlay: reconcile: purging deleted %s/%s: %w", del.ObjectClass, del.ExternalID, err)
		}
	}
	return nil
}

// emitMirrorConflict stages mirror.conflict (events catalog, OVA-EVT-1)
// in its own short transaction over the mirror store's pool — the same
// LogSystem+Emit shape freshness.go's emitBudgetDegraded uses for
// mirror.budget_degraded: a reconcile overwrite mutates no row this
// transaction itself owns an audit trail for (Ingest already committed
// it, deliberately without Audit — mirrorstore.go's own doc), so the
// event's ledger trace is a system_log row, not an audit_log one.
// rec.ExternalID bridges to the frozen EntityRef.ID shape the same way
// provider.go's recordFromRow does (externalIDToUUID) — HubSpot's own
// numeric object ids make that bridge exact.
func emitMirrorConflict(ctx context.Context, ms *MirrorStore, rec Record, prior Row) error {
	id, err := externalIDToUUID(rec.ExternalID)
	if err != nil {
		return fmt.Errorf("overlay: reconcile: emitting mirror.conflict for %s/%s: %w", rec.ObjectClass, rec.ExternalID, err)
	}
	detail := map[string]any{
		"object_class":         rec.ObjectClass,
		"external_id":          rec.ExternalID,
		"prior_updated_at":     prior.UpdatedAtBaseline,
		"incumbent_updated_at": rec.ModifiedAt,
	}
	err = database.WithWorkspaceTx(ctx, ms.pool, func(tx pgx.Tx) error {
		logID, err := storekit.LogSystem(ctx, tx, "mirror.conflict", detail)
		if err != nil {
			return fmt.Errorf("overlay: reconcile: logging the mirror.conflict system event: %w", err)
		}
		if err := storekit.Emit(ctx, tx, logID, "mirror.conflict", rec.ObjectClass, id, detail); err != nil {
			return fmt.Errorf("overlay: reconcile: emitting mirror.conflict: %w", err)
		}
		return nil
	})
	if err == nil {
		// Counted only once the emit actually committed — the
		// conflict-rate metric (metrics.go) must never run ahead of the
		// event stream it's reporting on.
		mirrorConflictTotal.Add(1)
	}
	return err
}
