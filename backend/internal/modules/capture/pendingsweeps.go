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
	"errors"
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

// UnsureReviewWindow is how long an `unsure` row waits for a human before the
// ledger stops asking. It is long on purpose — a question worth putting to a
// person is worth leaving there over a holiday — but it is not forever, because
// an unanswered question holds a slot against the deferral ceiling and against
// its sender's address for as long as it sits there.
const UnsureReviewWindow = 30 * 24 * time.Hour

// StaleReview is one `unsure` row that has waited past the window, with the
// offer standing in the review queue for it.
type StaleReview struct {
	ID         ids.UUID
	ProposalID *ids.UUID
}

// StaleReviews lists the rows whose review window has closed. Read-only: the
// caller ages each one out, because closing the question also means withdrawing
// its offer, and the approval table belongs to another module.
//
// The clock is `resolved_at`, which is stamped when the row BECAME `unsure` —
// not `updated_at`, which a nightly re-offer touches, so an unanswered question
// would keep resetting its own deadline and never age out at all.
func (s *PendingStore) StaleReviews(ctx context.Context, window time.Duration, limit int) ([]StaleReview, error) {
	var out []StaleReview
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, proposal_id
			  FROM capture_pending_counterparty
			 WHERE status = 'unsure'
			   AND resolved_at IS NOT NULL
			   AND resolved_at <= now() - make_interval(secs => $1)
			 ORDER BY resolved_at
			 LIMIT $2`, window.Seconds(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r StaleReview
			if err := rows.Scan(&r.ID, &r.ProposalID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the stale review backlog: %w", err)
	}
	return out, nil
}

// AgeOutReviewTx closes one unanswered question on the caller's transaction, so
// the ledger row and the withdrawal of its offer commit together.
//
// It resolves as `rejected`, which is exactly what it means: nothing is created,
// no mail is touched, and the sender is free to raise the question again the
// next time they write — the live-unique index only covers `pending` and
// `unsure`, so a later message opens a fresh row that gets a fresh verdict.
//
// The caller must have taken the row through ClaimReviewForAgeOut first: that is
// where the row lock and the still-`unsure` check live, and the CAS here repeats
// it so this can never be the write that decides.
func (s *PendingStore) AgeOutReviewTx(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = 'rejected',
		       disposition_reason = 'no decision within the review window',
		       resolved_at = now(), next_attempt_at = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'unsure'`, id); err != nil {
		return fmt.Errorf("capture: ageing out review %s: %w", id, err)
	}
	return nil
}

// ClaimReviewForAgeOut locks one stale row for the rest of the caller's
// transaction and reports the offer standing against it, or ok=false when the
// row stopped being an open question between the scan and now.
//
// The lock is the point. A human can decide the offer in that window, and the
// accept executor then creates the records and CASes the ledger to `real`. If
// this sweep wrote `rejected` in between, the executor's CAS would match nothing
// and the ledger would describe an outcome the database contradicts — records
// for a question recorded as closed unanswered. Holding the row (and, through
// the returned proposal, the offer) makes the two orders the only two possible
// ones: the human wins and this sweep skips, or this sweep wins and the decision
// is refused on an expired offer.
func (s *PendingStore) ClaimReviewForAgeOut(ctx context.Context, tx pgx.Tx, id ids.UUID) (proposalID *ids.UUID, ok bool, err error) {
	var status string
	err = tx.QueryRow(ctx, `
		SELECT status, proposal_id FROM capture_pending_counterparty
		 WHERE id = $1 FOR UPDATE`, id).Scan(&status, &proposalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("capture: locking review %s to age it out: %w", id, err)
	}
	if status != PendingStatusUnsure {
		return nil, false, nil
	}
	return proposalID, true, nil
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
//   - Never attested outbound (the T1 evidence), for the same reason — whether
//     the attestation came from a connector reading the owner's sent copy or
//     from the governed send path stamping its own outbound row.
//   - Never linked to a person, and never for an address a person EXISTS for.
//     A linked message belongs to somebody's record; and once the workspace has
//     a contact at that address — by any route, including a human typing it in
//     to correct a wrong verdict — the sender is a counterparty and a stale
//     disposition has no authority over their mail. Linkage alone is not enough:
//     a manually created contact backfills no activity_link.
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
	       AND c.direction = 'outbound' AND c.counterparty_outbound_attested)
	  AND NOT EXISTS (
	    SELECT 1 FROM person_email pe JOIN person pr ON pr.id = pe.person_id
	     WHERE pe.email = p.email AND pr.archived_at IS NULL)`

// noiseVerdictReach bounds how far past its own verdict a `noise` disposition
// may reach forward in time.
//
// Without a bound the disposition is permanent and unbounded, and that is an
// outsider's opening: forge one message as an address the workspace has never
// written to, shape it to read as bulk marketing, and every mail the REAL owner
// of that address sends afterwards is hidden within the hour and destroyed a
// week later — never seen by a human, so the documented "reply to recover"
// escape is unreachable in practice.
//
// A verdict is evidence about the mail that was in front of it. Mail arriving
// materially later is NEW evidence: it falls outside the disposition's reach, so
// it is not hidden, and it raises its own question to be judged on its own
// merits. The grace period keeps the common case whole — a newsletter that sends
// again the next morning is the same evidence, not new evidence.
//
// Keyed on created_at, the capture clock, NOT on occurred_at: the latter is the
// message's own Date header, as forgeable as the From this whole scope rule
// exists to distrust. A sender who stamped a date a fortnight in the future
// would otherwise fall outside every reach predicate at once and opt their bulk
// mail out of the noise effect entirely.
const noiseVerdictReach = 14 * 24 * time.Hour

// withinVerdictReach is the scope clause that bounds a disposition to the mail
// it is actually evidence about. Composed per query rather than folded into
// noiseMailScope because it carries a duration the const cannot interpolate.
func withinVerdictReach() string {
	return `
	  AND a.created_at <= p.resolved_at + ` + quoteInterval(noiseVerdictReach)
}

// NoiseMailToHide lists captured mail from judged-noise senders that is still
// visible. Driven from the MAIL rather than from the address list: the work is
// bounded by what is actually outstanding, so a workspace with thousands of
// noise senders cannot silently stop covering the oldest of them, and a sender
// who keeps writing after their verdict is folded in without a second pass
// having to remember they exist.
func (s *PendingStore) NoiseMailToHide(ctx context.Context, limit int) ([]ids.UUID, error) {
	return s.noiseMail(ctx, withinVerdictReach()+`
		AND a.archived_at IS NULL`, limit)
}

// NoiseMailToRedact lists hidden mail from judged-noise senders whose undo
// window has passed and that still has content to destroy.
//
// The window is measured from when THIS MESSAGE was hidden, not from when the
// verdict was reached. A sender keeps writing after their verdict, and keying
// the window off the disposition would give mail that arrived a month later no
// undo window at all — for exactly the messages a wrong verdict is most likely
// to catch.
//
// "Still has content" includes the provider original: a message whose activity
// is already nulled but whose raw_capture row survives is unfinished work, not
// finished work. That is what makes the sweep resumable across a crash between
// the two writes, and self-healing if a re-sync ever re-inserts an original for
// a message that was already redacted.
//
// Content-keyed, not flag-keyed, throughout: a one-shot marker on the ledger row
// would redact whatever that sender had written by the time it fired and retain
// everything afterwards.
func (s *PendingStore) NoiseMailToRedact(ctx context.Context, window time.Duration, limit int) ([]ids.UUID, error) {
	return s.noiseMail(ctx, withinVerdictReach()+`
		AND p.resolved_at IS NOT NULL
		AND a.archived_at IS NOT NULL AND a.archived_at <= now() - `+quoteInterval(window)+`
		AND (a.subject IS NOT NULL OR a.body IS NOT NULL OR a.raw IS NOT NULL
		     OR EXISTS (
		       SELECT 1 FROM raw_capture r
		        WHERE r.workspace_id = a.workspace_id
		          AND r.source_system = a.source_system AND r.source_id = a.source_id))`, limit)
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
		 WHERE p.email = $2 AND p.status = 'noise' AND `+noiseMailScope+withinVerdictReach()+`
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

// PurgeRawCaptureTx deletes the provider originals behind the given activities,
// on the caller's transaction so they die with the text they duplicate.
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
func (s *PendingStore) PurgeRawCaptureTx(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) error {
	if len(activityIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM raw_capture r
		 USING activity a
		 WHERE a.id = ANY($1)
		   AND r.workspace_id = a.workspace_id
		   AND r.source_system = a.source_system AND r.source_id = a.source_id`, activityIDs); err != nil {
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

// quoteInterval renders a duration as a SQL interval literal in SECONDS.
//
// Seconds, never Go's own duration format: time.Duration.String() emits things
// Postgres cannot parse — the micro sign in "1µs" being the sharp edge — so a
// duration that happens to be whole minutes today works and the same expression
// breaks at runtime the moment someone tunes it. Every duration this package
// hands to SQL goes through seconds for that reason (the bound ones as
// make_interval(secs => $n), this one inline because it sits in a shared
// predicate fragment where parameter numbering would depend on the caller).
// The value is a compiled-in constant, never user input.
func quoteInterval(d time.Duration) string {
	return "interval '" + strconv.Itoa(int(d.Seconds())) + " seconds'"
}
