// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The disposition ledger's SWEEPS: the set-based passes that keep it from
// silently filling up, as opposed to pending.go's per-row transitions.
//
// They exist because every other transition needs someone to be holding the row
// — a worker with a claim, or a human with a decision. These handle the cases
// where nobody is: a row whose attempts ran out while no model ever answered, a
// question a human declined (approvals runs only the approved branch, so a
// decline reaches the ledger by reconciliation rather than by being told), and
// the mail a judged-noise sender keeps writing after their verdict.
//
// All three are claim-free and idempotent by construction. A stranded row is
// held by nobody, so requiring a lease to rescue one would mean the rows most in
// need of rescue are exactly the ones that cannot be.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RetireExhausted moves every row that has spent its attempts into `unsure`,
// so exhaustion is a TERMINAL STATE and never a silent dead end.
//
// ClaimDue refuses a row at the attempt bound, and a refused row that nothing
// else transitions is stranded exactly where nobody looks: still `pending`, so
// the review queue ignores it; still counted by the deferral cap, so it consumes
// a slot forever; and still holding the live-unique index, so that sender can
// never raise a new question either. Retiring it turns a row nobody can process
// into a question a human can answer — which is what `unsure` is for.
//
// Claim-free by design: a stranded row is held by nobody, and requiring a lease
// to rescue it would mean the rows most in need of rescue are the ones that
// cannot be.
func (s *PendingStore) RetireExhausted(ctx context.Context, reason string) (int, error) {
	var retired int
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET status = 'unsure', disposition_reason = NULLIF($1, ''),
			       resolved_at = now(), next_attempt_at = NULL,
			       claimed_until = NULL, claimed_by = NULL, updated_at = now()
			 WHERE status = 'pending' AND attempts >= $2
			   AND (claimed_until IS NULL OR claimed_until <= now())`,
			reason, PendingMaxAttempts)
		if err != nil {
			return err
		}
		retired = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("capture: retiring exhausted dispositions: %w", err)
	}
	return retired, nil
}

// ReconcileDeclined closes every `unsure` row whose review offer a human
// rejected. The approvals engine runs only the APPROVED branch — a decline has
// no effect hook — so without this the row stays `unsure` forever: it keeps its
// slot against the deferral ceiling, and it is the tail that makes filling that
// ceiling worth an outsider's while.
//
// Recording the decline is not destructive, which is what makes it safe to do
// from a sweep: no records are created, no mail is touched, and the ledger
// simply stops asking a question that has been answered.
func (s *PendingStore) ReconcileDeclined(ctx context.Context) (int, error) {
	var closed int
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty p
			   SET status = 'rejected',
			       disposition_reason = 'declined in the review queue',
			       resolved_at = now(), updated_at = now()
			  FROM approval a
			 WHERE a.id = p.proposal_id
			   AND p.status = 'unsure'
			   AND a.status = 'rejected'`)
		if err != nil {
			return err
		}
		closed = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("capture: reconciling declined dispositions: %w", err)
	}
	return closed, nil
}

// NoiseAddresses lists the addresses this workspace has judged noise. The hide
// sweep needs them because a verdict is not the last word on a sender: they can
// keep mailing, and mail that arrives AFTER the verdict is captured without a
// new question being asked (the ladder already knows the answer). Something has
// to fold those later messages in, or "noise is not shown" would hold only for
// the mail that happened to arrive before the verdict.
func (s *PendingStore) NoiseAddresses(ctx context.Context, limit int) ([]string, error) {
	var out []string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT email FROM capture_pending_counterparty
			 WHERE status = 'noise'
			 ORDER BY resolved_at DESC
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var email string
			if err := rows.Scan(&email); err != nil {
				return err
			}
			out = append(out, email)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the noise addresses: %w", err)
	}
	return out, nil
}

// NoiseRedaction names one noise disposition whose undo window has expired. It
// carries the ADDRESS, not the trigger message: the disposition covers every
// message that sender wrote, so the redaction does too.
type NoiseRedaction struct {
	ID    ids.UUID
	Email string
}

// DueForRedaction lists noise dispositions resolved longer ago than window and
// not yet redacted. The window is the undo period: until it passes, a wrong
// verdict is fully recoverable because the mail is merely hidden.
//
// The cutoff is computed by the DATABASE from its own now(), like every other
// due-scan here — the app's clock never decides whether someone's undo window
// has run out.
func (s *PendingStore) DueForRedaction(ctx context.Context, window time.Duration, limit int) ([]NoiseRedaction, error) {
	var out []NoiseRedaction
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, email FROM capture_pending_counterparty
			 WHERE status = 'noise' AND redacted_at IS NULL
			   AND resolved_at IS NOT NULL AND resolved_at <= now() - $1::interval
			 ORDER BY resolved_at
			 LIMIT $2`, window.String(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r NoiseRedaction
			if err := rows.Scan(&r.ID, &r.Email); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the redaction backlog: %w", err)
	}
	return out, nil
}

// MarkRedacted records that a disposition's content redaction completed. Stamped
// only after the content is actually gone, so a crash between the two leaves the
// row due again — redoing a redaction is harmless, skipping one is not.
func (s *PendingStore) MarkRedacted(ctx context.Context, id ids.UUID) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET redacted_at = now(), updated_at = now()
			 WHERE id = $1 AND status = 'noise' AND redacted_at IS NULL`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: marking disposition %s redacted: %w", id, err)
	}
	return nil
}
