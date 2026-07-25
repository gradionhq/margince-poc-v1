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
	"strconv"
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

// noiseMailScope decides WHICH captured mail a noise disposition is allowed to
// act on, and it is deliberately much narrower than "every message bearing this
// address".
//
// counterparty_email comes from the message's own From header, which is
// unauthenticated: an outsider can forge any address they like. Acting on the
// address alone would hand them a weapon — mail one message as
// bigcustomer@corp.com, write it to read as bulk marketing, and a `noise`
// verdict would hide and then redact the workspace's real correspondence with
// that company, in both directions. The verdict is evidence about the mail the
// stranger actually sent, so it may only reach mail of that same kind:
//
//   - INBOUND only. The workspace's own sent mail is its own record, and a
//     stranger's forged header must never reach it.
//   - Never provider-attested outbound (the T1 evidence), for the same reason.
//   - Never linked to a person. A linked message belongs to somebody's record;
//     a disposition about an unknown sender has no authority over it.
//
// And the disposition stops applying entirely once the workspace CORRESPONDS
// with the address: writing to someone is the T1 signal that they are a
// counterparty, and it is the recovery path that makes an automatic hide safe to
// live with — reply to a wrongly-hidden sender and the sweep lets go.
const noiseMailScope = `
	  a.kind = 'email' AND a.captured_by LIKE 'connector:%'
	  AND a.direction = 'inbound'
	  AND NOT a.counterparty_outbound_attested
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link l
	     WHERE l.activity_id = a.id AND l.person_id IS NOT NULL)
	  AND NOT EXISTS (
	    SELECT 1 FROM activity c
	     WHERE c.counterparty_email = p.email
	       AND c.direction = 'outbound' AND c.counterparty_outbound_attested)`

// NoiseMailToHide lists captured mail from judged-noise senders that is still
// visible. Driven from the MAIL rather than from the address list: the work is
// bounded by what is actually outstanding, so a workspace with thousands of
// noise senders cannot silently stop covering the oldest of them, and a sender
// who keeps writing after their verdict is folded in without a second pass
// having to remember they exist.
func (s *PendingStore) NoiseMailToHide(ctx context.Context, limit int) ([]ids.UUID, error) {
	return s.noiseMail(ctx, `
		AND a.archived_at IS NULL`, limit)
}

// NoiseMailToRedact lists hidden mail from judged-noise senders whose undo
// window has passed and whose content is still present.
//
// Content-keyed, not flag-keyed: a one-shot marker on the ledger row would
// redact whatever that sender had written by the time it fired and retain
// everything they wrote afterwards, which is the same "acts on one moment
// instead of the question" mistake in slower motion.
func (s *PendingStore) NoiseMailToRedact(ctx context.Context, window time.Duration, limit int) ([]ids.UUID, error) {
	return s.noiseMail(ctx, `
		AND a.archived_at IS NOT NULL
		AND (a.subject IS NOT NULL OR a.body IS NOT NULL OR a.raw IS NOT NULL)
		AND p.resolved_at IS NOT NULL AND p.resolved_at <= now() - `+quoteInterval(window), limit)
}

// NoiseMailForTx is NoiseMailToHide for ONE address on the caller's transaction
// — what the verdict itself hides at the moment it commits. Same scope rule, so
// the immediate effect and the later sweep can never disagree about which mail a
// disposition may touch.
func (s *PendingStore) NoiseMailForTx(ctx context.Context, tx pgx.Tx, email string, limit int) ([]ids.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT a.id, a.occurred_at
		  FROM activity a
		  JOIN capture_pending_counterparty p ON p.email = a.counterparty_email
		 WHERE p.email = $2 AND p.status = 'noise' AND `+noiseMailScope+`
		   AND a.archived_at IS NULL
		 ORDER BY a.occurred_at
		 LIMIT $1`, limit, normalizeEmail(email))
	if err != nil {
		return nil, fmt.Errorf("capture: reading the sender's captured mail: %w", err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		var occurred time.Time
		if err := rows.Scan(&id, &occurred); err != nil {
			return nil, fmt.Errorf("capture: reading the sender's captured mail: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("capture: reading the sender's captured mail: %w", err)
	}
	return out, nil
}

// PurgeRawCapture deletes the provider originals behind the given activities.
//
// Without this the redaction destroys a copy and leaves the original: capture
// writes the verbatim provider payload — full headers and body — to raw_capture,
// keyed on the message's natural key. Nulling activity.subject/body while that
// row survives would make "the content is destroyed" false, and raw_capture has
// no retention sweep of its own; the only other purge is Art. 17 erasure, which
// is scoped to a PERSON and therefore structurally unreachable for a
// noise-judged sender, who has no person record by construction.
//
// The activity row keeps its source key, so the capture natural key still
// tombstones a replay — what goes is the content, not the fact of the message.
func (s *PendingStore) PurgeRawCapture(ctx context.Context, activityIDs []ids.UUID) error {
	if len(activityIDs) == 0 {
		return nil
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM raw_capture r
			 USING activity a
			 WHERE a.id = ANY($1)
			   AND r.source_system = a.source_system AND r.source_id = a.source_id`, activityIDs)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: purging the redacted mail's provider originals: %w", err)
	}
	return nil
}

// noiseMail runs the shared join with one extra predicate.
func (s *PendingStore) noiseMail(ctx context.Context, extra string, limit int) ([]ids.UUID, error) {
	var out []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT a.id, a.occurred_at
			  FROM activity a
			  JOIN capture_pending_counterparty p ON p.email = a.counterparty_email
			 WHERE p.status = 'noise' AND `+noiseMailScope+extra+`
			 ORDER BY a.occurred_at
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			var occurred time.Time
			if err := rows.Scan(&id, &occurred); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the noise mail backlog: %w", err)
	}
	return out, nil
}

// quoteInterval renders a duration as a SQL interval literal. The value is a
// compiled-in constant, never user input — it is spelled here rather than bound
// because it sits inside a shared predicate fragment where parameter numbering
// would depend on the caller.
func quoteInterval(d time.Duration) string {
	return "interval '" + strconv.Itoa(int(d.Seconds())) + " seconds'"
}
