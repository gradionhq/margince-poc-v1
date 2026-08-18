// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The expiry of a restriction (A165/ADR-0114 §2): when a held record's
// statutory window closes, the erasure it suspended completes without anybody
// asking again. This is the ONE path allowed to write a restricted row, and it
// takes the only shape the data-layer guard admits — the lift and the erasure
// in a single statement.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// restrictionExpiryStages is the batched stage this file adds to a workspace
// pass, claiming its own retentionBatch like the AI sweeps. It has its own
// count rather than riding aiRetentionStages: that name says what those
// stages are, and the erasure of expired Handelsbriefe is not one of them.
const restrictionExpiryStages = 1

// restrictionExpiredCause is what the tombstone and the event say completed
// the erasure — the window ran out, nobody decided anything.
const restrictionExpiredCause = "restriction_expired"

// evaluateRestrictionExpiry completes the suspended erasure of every held
// record whose window has closed. It runs IRRESPECTIVE of the retain-only
// posture: that posture suspends the storage-limitation ladder an operator
// authored, and this is not a policy the operator may decline — it is the
// second half of an Art. 17 request the engine already accepted and held.
//
// A record under a legal hold reached through ANY of its links is skipped:
// the hold outranks the subject's request until it is lifted, and the row
// stays restricted meanwhile, which is the more protective of the two states.
// The person arm is included here where the erasure's own selectors leave it
// out — the erasure proved its subject unheld before it ran, but a hold can
// land on that (now anonymised) person row during the years the window is
// open, and the sweep must see it. The predicate is repeated in the lift
// statement itself so a hold placed between the selection and the write
// still wins.
func (s *RetentionService) evaluateRestrictionExpiry(ctx context.Context) error {
	var due []ids.UUID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id FROM activity a
			WHERE a.restricted_at IS NOT NULL AND a.restricted_until <= now()
			`+notHeldThroughAnyLink("a.id")+`
			ORDER BY a.restricted_until
			LIMIT $1`, retentionBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return fmt.Errorf("retention restriction expiry: select: %w", err)
	}
	for _, id := range due {
		if err := s.expireRestriction(ctx, id); err != nil {
			return fmt.Errorf("retention restriction expiry on %s: %w", id, err)
		}
	}
	return nil
}

// notHeldThroughAnyLink is notTransitivelyHeld plus the person arm.
func notHeldThroughAnyLink(activityID string) string {
	return `
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link h
	    LEFT JOIN person hp ON hp.id = h.person_id
	    LEFT JOIN organization org ON org.id = h.organization_id
	    LEFT JOIN deal dl ON dl.id = h.deal_id
	    WHERE h.activity_id = ` + activityID + `
	      AND (coalesce(hp.legal_hold, false) OR coalesce(org.legal_hold, false) OR coalesce(dl.legal_hold, false)))`
}

// expireRestriction erases one held record in its own audited transaction.
// The lift and the erasure are ONE statement because the guard admits nothing
// else: a lift that left the body readable would undo the restriction and
// keep the data. The `restricted_until <= now()` predicate is the CAS — a
// rival sweep that already completed this row matches nothing, and nothing is
// audited twice for one erasure.
func (s *RetentionService) expireRestriction(ctx context.Context, id ids.UUID) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var class string
		err := tx.QueryRow(ctx, `
			UPDATE activity a
			   SET restricted_at = NULL, restricted_reason = NULL, restricted_until = NULL,
			       subject = NULL, body = NULL, raw = NULL, counterparty_email = NULL,
			       redacted_fields = a.redacted_fields || ARRAY(SELECT c FROM unnest(ARRAY[
			           CASE WHEN a.subject IS NOT NULL THEN 'subject' END,
			           CASE WHEN a.body IS NOT NULL THEN 'body' END]) AS c
			         WHERE c IS NOT NULL),
			       archived_at = coalesce(a.archived_at, now())
			 WHERE a.id = $1 AND a.restricted_at IS NOT NULL AND a.restricted_until <= now()
			 `+notHeldThroughAnyLink("a.id")+`
			 RETURNING a.retention_class`, id).Scan(&class)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.purgeExpiredRecordTraces(ctx, tx, id); err != nil {
			return err
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionExpire, "activity", id, nil, nil, map[string]any{
			evidenceKeyCause: restrictionExpiredCause, "class": class, "basis": statutoryBasisCorrespondence,
		})
		if err != nil {
			return err
		}
		reason := restrictionExpiredCause
		return storekit.EmitEventForEntity(ctx, tx, auditID, "activity", id, retentionAppliedPayload(actionErase, nil, &reason))
	})
}

// purgeExpiredRecordTraces finishes the erasure the way the person-erase
// cascade does for a destroyed row: the derived copies, the field-level
// provenance of the text now gone, the attachments the restriction kept as
// commercial substance, and the transmitted copy in the send log — which now
// loses its substance too, on the same terms as the activity.
func (s *RetentionService) purgeExpiredRecordTraces(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM field_provenance WHERE object_type = 'activity' AND object_id = $1`, id); err != nil {
		return err
	}
	if err := purgeTranscriptReadings(ctx, tx, []ids.UUID{id}); err != nil {
		return err
	}
	if err := s.eraser.eraseAttachments(ctx, tx, `entity_type = 'activity' AND entity_id = $1`, id); err != nil {
		return err
	}
	return redactDeliveries(ctx, tx, []ids.UUID{id}, erasedName)
}
