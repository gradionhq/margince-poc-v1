// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The MirrorStore's deletion surface: the per-record purge a continuous-sync
// deletion sweep performs when the incumbent removes a record, together with
// the mirror.deleted event it emits — both in ONE transaction so the purge
// and its promised event can never diverge. Split from mirrorstore.go (the
// modified-record sync surface) so each file stays one concept; the deletion
// feed is the opposite-direction convergence, driven by reconcile.go's
// ReconcileDeletions.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// mirrorDeletedPayload builds the mirror.deleted wire payload — the
// subject travels separately (del.ObjectClass/id, passed to
// storekit.EmitEventForEntity), since this event's entity is dynamic (the
// runtime object class of the purged record).
func mirrorDeletedPayload(objectClass, externalID string, deletedAt time.Time) crmcontracts.PublicEventMirrorDeleted {
	return crmcontracts.PublicEventMirrorDeleted{
		ObjectClass: objectClass,
		ExternalId:  externalID,
		DeletedAt:   deletedAt,
	}
}

// PurgeRecord removes one incumbent-deleted record from the mirror — the
// cache row, every association edge that names it on EITHER endpoint, and
// its per-user visibility projection — AND, when the mirror actually held
// the row, emits mirror.deleted, all in ONE transaction. Doing the purge
// and its event atomically is deliberate: emitting in a separate
// transaction (as an earlier version did) would permanently DROP the event
// if that second transaction failed, because the next sweep sees no mirror
// row and treats the record as already handled — so the purge would have
// happened with no trace on the bus. It reports whether a mirror row was
// present, so a deletion the mirror never held (never visible to this
// workspace, or already purged) is a clean no-op — no event, no error —
// which also makes the full-scan sweep safe to re-run every pass.
//
// Unlike teardown's purge, PurgeRecord writes NO tombstone: an incumbent
// deletion is not a GDPR erasure hold, and a record HubSpot later restores
// must be free to re-mirror; the tombstone is reserved for privacy erasure,
// which owns its own suppression path.
func (s *MirrorStore) PurgeRecord(ctx context.Context, del Deletion) (bool, error) {
	if del.ObjectClass == "" || del.ExternalID == "" {
		return false, fmt.Errorf("overlay: purge requires a non-empty object class and external id")
	}
	id, err := externalIDToUUID(del.ExternalID)
	if err != nil {
		return false, fmt.Errorf("overlay: purging %s/%s: %w", del.ObjectClass, del.ExternalID, err)
	}

	var existed bool
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM overlay_mirror WHERE object_class = $1 AND external_id = $2`,
			del.ObjectClass, del.ExternalID)
		if err != nil {
			return fmt.Errorf("overlay: purging the mirror row %s/%s: %w", del.ObjectClass, del.ExternalID, err)
		}
		existed = tag.RowsAffected() > 0

		if _, err := tx.Exec(ctx, `
			DELETE FROM overlay_association
			WHERE (from_type = $1 AND from_id = $2) OR (to_type = $1 AND to_id = $2)`,
			del.ObjectClass, del.ExternalID); err != nil {
			return fmt.Errorf("overlay: purging the association edges of %s/%s: %w", del.ObjectClass, del.ExternalID, err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM mirror_visibility WHERE object_class = $1 AND external_id = $2`,
			del.ObjectClass, del.ExternalID); err != nil {
			return fmt.Errorf("overlay: purging the visibility projection of %s/%s: %w", del.ObjectClass, del.ExternalID, err)
		}

		// Emit only when the mirror actually held the row, and in THIS
		// transaction so the event commits with the purge or not at all.
		// The ledger trace is a system_log row (not audit_log): a mirror
		// purge is a derived-cache health event, the same posture Ingest and
		// mirror.conflict take (mirrorstore.go / reconcile.go).
		if !existed {
			return nil
		}
		detail := map[string]any{
			"object_class": del.ObjectClass,
			"external_id":  del.ExternalID,
			"deleted_at":   del.DeletedAt,
		}
		logID, err := storekit.LogSystem(ctx, tx, "mirror.deleted", detail)
		if err != nil {
			return fmt.Errorf("overlay: logging the mirror.deleted system event: %w", err)
		}
		if err := storekit.EmitEventForEntity(ctx, tx, logID, del.ObjectClass, id,
			mirrorDeletedPayload(del.ObjectClass, del.ExternalID, del.DeletedAt)); err != nil {
			return fmt.Errorf("overlay: emitting mirror.deleted: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if existed {
		// Counted only once the purge+emit committed — the deletion-rate
		// metric must never run ahead of the event stream it reports on.
		mirrorDeletedTotal.Add(1)
	}
	return existed, nil
}
