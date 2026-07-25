// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The ledger's REVIEW-QUEUE side: the transitions that involve a human rather
// than a worker.
//
// They are guarded differently from the claim lifecycle in pending.go, and the
// difference is the point. A claimed row is held by a worker and every write to
// it presents that worker's claim token; an `unsure` row is held by nobody and
// is waiting for a person, so its writes CAS on the status instead. Mixing the
// two guards is how a row ends up either unwritable or writable by the wrong
// party.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// LinkProposal points an `unsure` row at the review-queue offer staged for it,
// so a later pass finds the existing offer instead of staging a second one. A
// dead link (the previous offer expired) is overwritten — the pairing that
// matters is row-to-LIVE-offer, and refusing to re-link would strand the row
// the moment its first proposal aged out.
//
// Guarded on the row still being `unsure`: one a human has already decided must
// never have a fresh proposal attached to it.
func (s *PendingStore) LinkProposal(ctx context.Context, id, proposalID ids.UUID) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET proposal_id = $2, updated_at = now()
			 WHERE id = $1 AND status = 'unsure'`, id, proposalID)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: linking the review proposal for %s: %w", id, err)
	}
	return nil
}

// AwaitingReview lists `unsure` rows with no LIVE review-queue offer — the
// staging backlog. A row reaches this state by exhausting the model's attempts,
// so what it needs next is a human, and until a live offer exists nobody can
// give it one.
//
// "No live offer", not "no offer": a staged proposal expires after a day if
// nobody acts on it, and a row whose only offer has expired is exactly as
// undecidable as one that never had a proposal. Keying on proposal_id alone
// would strand it permanently — invisible to the review queue, still counting
// against the workspace's open-question ceiling, and clearable only by hand.
// A workspace that takes a weekend off would silently fill its own cap.
//
// A DECIDED offer is the opposite case and must not come back: re-staging one a
// human already answered would ask them the same question every hour forever.
// Expired means unanswered; decided means answered, whichever way.
func (s *PendingStore) AwaitingReview(ctx context.Context, limit int) ([]PendingCounterparty, error) {
	var out []PendingCounterparty
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id, p.email, coalesce(p.domain, ''), coalesce(left(p.display_name, $2), ''),
			       p.activity_id, p.owner_id
			  FROM capture_pending_counterparty p
			 WHERE p.status = 'unsure'
			   AND NOT EXISTS (
			     SELECT 1 FROM approval a
			      WHERE a.id = p.proposal_id
			        AND (a.decided_at IS NOT NULL
			             OR (a.status = 'pending' AND a.expires_at > now())))
			 ORDER BY p.resolved_at, p.created_at
			 LIMIT $1`, limit, MaxCapturedNameChars)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PendingCounterparty
			if err := rows.Scan(&p.ID, &p.Email, &p.Domain, &p.DisplayName,
				&p.ActivityID, &p.OwnerID); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the review backlog: %w", err)
	}
	return out, nil
}

// ResolveReviewed closes an `unsure` row that a human decided, on the caller's
// transaction. Unlike Resolve it carries no claim token — the authority here is
// the redeemed approval, not a worker's lease, and an `unsure` row is held by
// nobody. The CAS on `unsure` is what makes a replayed redemption a no-op.
func (s *PendingStore) ResolveReviewed(ctx context.Context, tx pgx.Tx, id ids.UUID, status, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = $2, disposition_reason = NULLIF($3, ''),
		       resolved_at = now(), next_attempt_at = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'unsure'`, id, status, reason)
	if err != nil {
		return fmt.Errorf("capture: resolving reviewed disposition %s: %w", id, err)
	}
	return nil
}
