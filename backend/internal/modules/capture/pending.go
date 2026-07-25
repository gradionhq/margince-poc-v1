// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The capture disposition ledger (CAP-DDL-8, ADR-0072 §5). The tiered creation
// gate defers the ambiguous first-time sender instead of creating on sight: the
// Sink records what it decided about an address, IN the capture transaction, and
// the verdict engine resolves what it deferred. Suppressions record here too, so
// a wrong registry entry is queryable rather than only a log line.
//
// This file owns the ledger's SQL and nothing else — capture never touches
// person/organization tables, and the resolver seam stays the only way records
// come into being.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Disposition statuses. `pending` and `unsure` are the LIVE states the unique
// index keys on — one open question per address at a time.
const (
	PendingStatusPending    = "pending"
	PendingStatusUnsure     = "unsure"
	PendingStatusReal       = "real"
	PendingStatusNoise      = "noise"
	PendingStatusSuppressed = "suppressed"
	// PendingStatusRejected records a human's decline. The approvals engine has
	// no reject hook — it runs only the approved branch — so the ledger learns
	// of a decline by reconciling against the approval row (ReconcileDeclined)
	// rather than by being told.
	PendingStatusRejected = "rejected"
)

// PendingMaxAttempts bounds the verdict retries (ADR-0072 §5: retries=2). A row
// that exhausts them is retired to `unsure` by RetireExhausted rather than
// retried forever — exhaustion is a terminal state, never a row nothing will
// ever pick up again.
// Exported so the verdict engine can retire a row deliberately — a terminal
// answer is reached by spending the attempts, not by a second spelling of
// "give up".
const PendingMaxAttempts = 2

// pendingLease is how long a claimed row stays off other workers' scans. Longer
// than a batch takes, short enough that a worker that died mid-batch releases
// its rows by expiry instead of stranding them.
const pendingLease = 15 * time.Minute

// NoiseUndoWindow is how long a noise-dispositioned message stays merely hidden
// before its content is redacted (ADR-0072 §4). The delay is the whole safety
// margin on the one verdict that destroys anything: a wrong `noise` is fully
// recoverable — un-archive and the mail is back — right up until the window
// closes, which is why hiding is allowed to be automatic at all.
const NoiseUndoWindow = 7 * 24 * time.Hour

// PendingDeferralCap bounds how many open questions one workspace may hold at
// once. Every deferral is a promised model call, and the party who creates them
// is an OUTSIDER: anyone who can mail the connected mailbox from fresh addresses
// mints ledger rows, so without a ceiling a stranger sets the workspace's AI
// spend. At the cap capture stops queueing questions — the messages still land
// on the timeline, they simply go unjudged, which is the safe direction to fail:
// a backlog of unanswered questions is recoverable, junk records are not.
const PendingDeferralCap = 500

// PendingCounterparty is one deferred disposition as the verdict engine reads
// it: the identity to judge and the message that raised the question.
type PendingCounterparty struct {
	ID          ids.UUID
	Email       string
	Domain      string
	DisplayName string // untrusted header text — for display, never matching
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Subject     string
	Body        string
	// SuppressOrg carries the tier ladder's free-mail decision (CAP-PARAM-5)
	// forward to whoever creates the records: a personal mailbox yields a person
	// and never a company, however long after capture the verdict arrives.
	SuppressOrg bool

	// Claim is this lease's token, minted by the ClaimDue that handed the row
	// out. Every write back to the ledger presents it, so a worker holding an
	// expired lease can no longer resolve a row that someone else has since
	// claimed. Carry it; never construct one.
	Claim ids.UUID
}

// recordDisposition writes one ledger row inside the caller's capture
// transaction. Idempotent on the live-row index: a second message from the same
// stranger joins the open question instead of queuing a second verdict for it.
//
// status decides whether anything is deferred — a T2 suppression records its
// reason and retires immediately (no next_attempt_at), while a T4 ambiguous
// sender stays due for the verdict engine.
//
// It reports whether the workspace's deferral cap refused the row, which the
// caller records as its own breadcrumb: a capture that asks no question is a
// different event from one that joins an existing one, and only the first means
// the workspace is being flooded.
func recordDisposition(ctx context.Context, tx pgx.Tx, in dispositionRow) (bool, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return false, errors.New("capture: a disposition needs a normalized counterparty address")
	}
	// Deletion sticks, at the WRITE and not only in the erasure sweep. An erased
	// subject's address must not re-materialize here — a fresh ledger row would
	// restore their address and header display name in a new table, and a
	// deferred row additionally hands their subject and body to the routed model
	// provider on the verdict call. The two sibling paths (captureLead,
	// EnsureCounterpartyTx) already refuse a suppressed address; this is the
	// same invariant, not a new rule.
	suppressed, err := storekit.EmailSuppressed(ctx, tx, email)
	if err != nil {
		return false, fmt.Errorf("capture: checking the suppression list: %w", err)
	}
	if suppressed {
		return false, nil
	}

	due := in.Status == PendingStatusPending
	if due {
		// Asked before the insert rather than folded into it as a WHERE, because
		// the two zero-row outcomes must stay distinguishable: at the cap nothing
		// is asked, whereas ON CONFLICT DO NOTHING means the question is already
		// open. The count is exact under RLS (the policy scopes it to this
		// workspace) and only the ambiguous tier pays for it.
		//
		// The ceiling applies to NEW questions only. Once it is reached, every
		// further message from any of the already-deferred senders would
		// otherwise be reported as capped-and-unjudged, when its question is in
		// fact open and will be answered — a breadcrumb that misdescribes the
		// system is worse than none.
		capped, err := capRefusesNewQuestion(ctx, tx, email)
		if err != nil {
			return false, err
		}
		if capped {
			return true, nil
		}
	}
	// One disposition per address per state, whichever index arbitrates it: a
	// second message from the same stranger joins the open question, and a
	// second newsletter does not append another copy of the same answer.
	conflict := "(workspace_id, email) WHERE status IN ('pending', 'unsure')"
	if in.Status == PendingStatusSuppressed {
		conflict = "(workspace_id, email) WHERE status = 'suppressed'"
	}
	// Due-ness is stamped with the DATABASE's clock, never the caller's. ClaimDue
	// compares next_attempt_at against Postgres now(), so a next_attempt_at taken
	// from the app process makes the comparison a cross-clock one: an app running
	// even milliseconds ahead of the database writes a row that is not yet due and
	// silently waits out the skew before anything claims it.
	_, err = tx.Exec(ctx, `
		INSERT INTO capture_pending_counterparty
		  (workspace_id, email, domain, display_name, activity_id, owner_id, status,
		   disposition_reason, next_attempt_at, suppress_org)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''),
		        CASE WHEN $8::boolean THEN now() END, $9)
		ON CONFLICT `+conflict+`
		DO NOTHING`,
		email, strings.ToLower(strings.TrimSpace(in.Domain)), in.DisplayName,
		in.ActivityID, in.OwnerID, in.Status, in.Reason, due, in.SuppressOrg)
	if err != nil {
		return false, fmt.Errorf("capture: recording the counterparty disposition: %w", err)
	}
	return false, nil
}

// capRefusesNewQuestion reports whether the ceiling turns this message away. The
// ceiling applies to NEW questions only: a further message from an
// already-deferred sender joins the open question and adds nothing to the count.
func capRefusesNewQuestion(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	open, err := hasOpenQuestion(ctx, tx, email)
	if err != nil || open {
		return false, err
	}
	return atDeferralCap(ctx, tx)
}

// hasOpenQuestion reports whether this address already has a live disposition,
// which a further message joins rather than adding to the ceiling.
func hasOpenQuestion(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM capture_pending_counterparty
		   WHERE email = $1 AND status IN ('pending', 'unsure'))`, email).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("capture: checking for an open disposition: %w", err)
	}
	return open, nil
}

// atDeferralCap reports whether this workspace already holds its ceiling of open
// questions. Counts the live states, not just 'pending': an 'unsure' row is a
// question a human still owes an answer to, so a workspace cannot walk past the
// bound by leaving its review queue unattended.
func atDeferralCap(ctx context.Context, tx pgx.Tx) (bool, error) {
	var live int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM capture_pending_counterparty
		 WHERE status IN ('pending', 'unsure')`).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("capture: counting open dispositions: %w", err)
	}
	return live >= PendingDeferralCap, nil
}

// dispositionRow names one ledger write.
type dispositionRow struct {
	Email       string
	Domain      string
	DisplayName string
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Status      string
	Reason      string
	SuppressOrg bool
}

// PendingStore reads and resolves the ledger. It is the verdict engine's seam
// into capture's own table; compose injects it.
type PendingStore struct{ pool *pgxpool.Pool }

// NewPendingStore builds the ledger store over the capture pool.
func NewPendingStore(pool *pgxpool.Pool) *PendingStore { return &PendingStore{pool: pool} }

// ClaimDue atomically leases up to limit due rows for this workspace. FOR UPDATE
// SKIP LOCKED lets several replicas drain the ledger without double-judging a
// row or serializing on each other; the lease is what a crashed worker releases
// by expiry.
//
// Claiming bumps attempts, so a row that keeps failing walks toward its bound
// rather than being retried forever, and stamps a fresh claim token every
// batch shares — the key Resolve and Defer demand back.
func (s *PendingStore) ClaimDue(ctx context.Context, limit int) ([]PendingCounterparty, error) {
	claim := ids.NewV7()
	var out []PendingCounterparty
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE capture_pending_counterparty p
			   SET attempts = p.attempts + 1,
			       claimed_until = now() + $2::interval,
			       claimed_by = $4,
			       updated_at = now()
			 WHERE p.id IN (
			   SELECT id FROM capture_pending_counterparty
			    WHERE status = 'pending'
			      AND next_attempt_at IS NOT NULL AND next_attempt_at <= now()
			      AND (claimed_until IS NULL OR claimed_until <= now())
			      -- The bound is a property of the ROW, not of a live worker.
			      -- A worker that crashes, is killed, or outruns its lease never
			      -- reaches Defer, so a row whose content reliably kills the
			      -- verdict step would otherwise be re-claimed every lease
			      -- expiry forever, at one model call a time.
			      AND attempts < $3
			    ORDER BY next_attempt_at
			    LIMIT $1
			    FOR UPDATE SKIP LOCKED)
			RETURNING p.id, p.email, coalesce(p.domain, ''), coalesce(p.display_name, ''),
			          p.activity_id, p.owner_id, p.suppress_org,
			          coalesce((SELECT a.subject FROM activity a WHERE a.id = p.activity_id), ''),
			          coalesce((SELECT a.body FROM activity a WHERE a.id = p.activity_id), '')`,
			limit, pendingLease.String(), PendingMaxAttempts, claim)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p := PendingCounterparty{Claim: claim}
			if err := rows.Scan(&p.ID, &p.Email, &p.Domain, &p.DisplayName,
				&p.ActivityID, &p.OwnerID, &p.SuppressOrg, &p.Subject, &p.Body); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: claiming due dispositions: %w", err)
	}
	return out, nil
}

// Resolve closes a claimed row with its verdict. The CAS on `pending` AND on the
// caller's own claim is what makes a racing second worker — or a replayed job,
// or one whose lease expired while it was still running — a no-op rather than a
// second creation: it reports whether THIS call was the one that resolved the
// row, and only that caller may act on the verdict.
//
// It takes the claimed row rather than an id so the token cannot be lost or
// mismatched on the way here.
func (s *PendingStore) Resolve(ctx context.Context, tx pgx.Tx, p PendingCounterparty, status, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = $2, disposition_reason = NULLIF($3, ''),
		       resolved_at = now(), next_attempt_at = NULL,
		       claimed_until = NULL, claimed_by = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'pending' AND claimed_by = $4`, p.ID, status, reason, p.Claim)
	if err != nil {
		return false, fmt.Errorf("capture: resolving disposition %s: %w", p.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Defer returns a claimed row to the queue for a later pass. Ending a row is
// Retire's job, not this one: a deferral says "ask again later", and conflating
// the two is how a row ends up retired for reasons that had nothing to do with
// the question (a provider outage, a budget stop).
//
// Guarded by the same claim as Resolve, for the same reason: a stalled worker
// releasing "its" row would otherwise cut short a lease someone else now holds.
func (s *PendingStore) Defer(ctx context.Context, p PendingCounterparty, backoff time.Duration, reason string) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The attempt is GIVEN BACK. ClaimDue spends one at claim time, before
		// anything is known about how the batch goes, and every caller of Defer
		// is a path where no model ever answered — a budget stop, a provider
		// fault, a malformed reply. Charging the row for those would let two bad
		// cycles exhaust an address's whole allowance without a single verdict
		// having been attempted on its merits.
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET next_attempt_at = now() + $2::interval,
			       attempts = greatest(attempts - 1, 0),
			       disposition_reason = NULLIF($4, ''),
			       claimed_until = NULL, claimed_by = NULL, updated_at = now()
			 WHERE id = $1 AND status = 'pending' AND claimed_by = $3`,
			p.ID, backoff.String(), p.Claim, reason)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: deferring disposition %s: %w", p.ID, err)
	}
	return nil
}

// Retire ends a claimed row at `unsure`: the model was asked its allowance of
// times and never cleared the floor, so the question passes to a human.
//
// Terminal by construction: it stamps the attempt count it asserts and the time
// it stopped, so an operator reading the row sees why it ended rather than
// having to infer it from a counter that says something else.
func (s *PendingStore) Retire(ctx context.Context, p PendingCounterparty, reason string) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET status = 'unsure', disposition_reason = NULLIF($2, ''),
			       attempts = $4, resolved_at = now(),
			       next_attempt_at = NULL, claimed_until = NULL, claimed_by = NULL,
			       updated_at = now()
			 WHERE id = $1 AND status = 'pending' AND claimed_by = $3`,
			p.ID, reason, p.Claim, PendingMaxAttempts)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: retiring disposition %s: %w", p.ID, err)
	}
	return nil
}

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
			SELECT p.id, p.email, coalesce(p.domain, ''), coalesce(p.display_name, ''),
			       p.activity_id, p.owner_id, p.suppress_org
			  FROM capture_pending_counterparty p
			 WHERE p.status = 'unsure'
			   AND NOT EXISTS (
			     SELECT 1 FROM approval a
			      WHERE a.id = p.proposal_id
			        AND (a.decided_at IS NOT NULL
			             OR (a.status = 'pending' AND a.expires_at > now())))
			 ORDER BY p.resolved_at, p.created_at
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PendingCounterparty
			if err := rows.Scan(&p.ID, &p.Email, &p.Domain, &p.DisplayName,
				&p.ActivityID, &p.OwnerID, &p.SuppressOrg); err != nil {
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

// normalizeEmail is the ONE spelling of the ledger's identity: lowercased and
// trimmed, matching activity.counterparty_email and person_email so the verdict,
// the correspondence gate, and the dedupe chokepoint agree on what the same
// address is.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
