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
	var existed bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		existed, err = s.purgeRecordTx(ctx, tx, del)
		return err
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

// purgeRecordTx is PurgeRecord's body, taking the transaction rather than
// opening one, so a caller that must commit MORE than the purge in the same
// transaction can. The write-back path is that caller: a human archive's
// audit_log and event_outbox rows commit with the purge they describe, or
// neither lands (writeaudit.go).
func (s *MirrorStore) purgeRecordTx(ctx context.Context, tx pgx.Tx, del Deletion) (bool, error) {
	if del.ObjectClass == "" || del.ExternalID == "" {
		return false, fmt.Errorf("overlay: purge requires a non-empty object class and external id")
	}
	// A purge only deletes, so it can never resurrect incumbent-derived data
	// — but it DOES emit mirror.deleted, and emitting an overlay event into a
	// workspace that has already gone back to native is the same
	// post-disconnect write the fence exists to stop. Every other mutation on
	// this store asserts the connection; so does this one, when fenced.
	if s.fenced {
		if err := assertActiveConnection(ctx, tx); err != nil {
			return false, err
		}
	}
	id, err := externalIDToUUID(del.ExternalID)
	if err != nil {
		return false, fmt.Errorf("overlay: purging %s/%s: %w", del.ObjectClass, del.ExternalID, err)
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM overlay_mirror WHERE object_class = $1 AND external_id = $2`,
		del.ObjectClass, del.ExternalID)
	if err != nil {
		return false, fmt.Errorf("overlay: purging the mirror row %s/%s: %w", del.ObjectClass, del.ExternalID, err)
	}
	existed := tag.RowsAffected() > 0

	if _, err := tx.Exec(ctx, `
		DELETE FROM overlay_association
		WHERE (from_type = $1 AND from_id = $2) OR (to_type = $1 AND to_id = $2)`,
		del.ObjectClass, del.ExternalID); err != nil {
		return false, fmt.Errorf("overlay: purging the association edges of %s/%s: %w", del.ObjectClass, del.ExternalID, err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM mirror_visibility WHERE object_class = $1 AND external_id = $2`,
		del.ObjectClass, del.ExternalID); err != nil {
		return false, fmt.Errorf("overlay: purging the visibility projection of %s/%s: %w", del.ObjectClass, del.ExternalID, err)
	}

	// Emit only when the mirror actually held the row, and in THIS
	// transaction so the event commits with the purge or not at all.
	// The ledger trace is a system_log row (not audit_log): a mirror
	// purge is a derived-cache health event, the same posture Ingest and
	// mirror.conflict take (mirrorstore.go / reconcile.go). A HUMAN archive
	// riding this same transaction adds its own audit_log row on top
	// (writeaudit.go) — that one IS a domain mutation.
	if !existed {
		return false, nil
	}
	detail := map[string]any{
		"object_class": del.ObjectClass,
		keyExternalID:  del.ExternalID,
		"deleted_at":   del.DeletedAt,
	}
	logID, err := storekit.LogSystem(ctx, tx, "mirror.deleted", detail)
	if err != nil {
		return false, fmt.Errorf("overlay: logging the mirror.deleted system event: %w", err)
	}
	if err := storekit.EmitEventForEntity(ctx, tx, logID, del.ObjectClass, id,
		mirrorDeletedPayload(del.ObjectClass, del.ExternalID, del.DeletedAt)); err != nil {
		return false, fmt.Errorf("overlay: emitting mirror.deleted: %w", err)
	}
	return true, nil
}
