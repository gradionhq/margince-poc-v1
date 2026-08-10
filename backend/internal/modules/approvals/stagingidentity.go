// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// One proposal identity: whether the thing about to be staged is already on the
// table, and who owns that identity while it is decided.
//
// A stager fed by an at-least-once trigger — a connector sync re-hitting the same
// collision, a nightly sweep re-deriving the same diff — asks the same question
// every pass. Answering it is not a lookup but an ORDERING problem, which is why
// these live together: the probes below are only sound while the identity lock is
// held, and a caller that reads before taking it reads a world that is about to
// change underneath it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// lockOrder is the ONE order in which any statement that row-locks more than
// one `approval` may take those locks.
//
// Two transactions that lock a shared set of rows in the same total order
// cannot deadlock; two that lock it in different orders eventually will, and
// PostgreSQL resolves that by aborting one of them with 40P01 — a 500 for
// whichever caller lost, on a decision or a re-proposal that was otherwise
// perfectly valid. That is not hypothetical here: a bundle decision locks every
// member of one bundle, and a re-proposal of the same act locks the rows it
// joins, so the two meet on the same rows routinely.
//
// (created_at, id) rather than created_at alone, because created_at is a
// timestamp two rows staged in one transaction share exactly. An order with
// ties is not a total order, and the ties are precisely the rows a single
// staging pass creates together — the ones most likely to be locked as a set.
//
// TestEveryMultiRowApprovalLockTakesTheCanonicalOrder holds every locking
// statement in this package to it.
const lockOrder = `ORDER BY created_at, id`

// LockPendingGroupInTx takes, in the canonical order, every row lock a caller
// that stages a GROUP of proposals under one kind and target is going to need.
//
// A batch stager locks one row per member as it goes, in whatever order its
// payload happens to be in — the order a website lists its team page, say. The
// per-statement order is right for each statement and wrong for the
// transaction: nothing makes the sequence agree with the order a concurrent
// bundle decision walks the same rows in. Taking the whole set up front, here,
// is what makes those two agree, and every later statement in the batch then
// finds rows this transaction already holds rather than acquiring a new lock in
// payload order.
//
// The predicate is deliberately WIDER than any one member's: kind and target
// alone, so it is a superset of what the joins, the rebundle and the
// supersession will each touch. `now()` is transaction time, so the set it
// locks is the same set those statements later resolve.
func (s *Service) LockPendingGroupInTx(ctx context.Context, tx pgx.Tx, kind string, targetID ids.UUID) error {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("crmapprovals: no workspace bound to context")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM approval
		 WHERE workspace_id = $1 AND kind = $2 AND target_entity_id IS NOT DISTINCT FROM $3
		   AND status = 'pending' AND expires_at > now()
		 `+lockOrder+`
		 FOR UPDATE`, wsID, kind, nullUUID(targetID)); err != nil {
		return fmt.Errorf("lock the pending proposals this act will re-propose: %w", err)
	}
	return nil
}

// stageOrJoinPendingInTx serializes one proposal identity and returns its live
// pending approval when another worker already staged it. The transaction
// lock covers the empty-set case that a row lock cannot protect, so replicas
// cannot both observe no pending row and create duplicates.
// lockProposalIdentity serializes one proposal identity for the rest of the
// transaction: the diff hash by default, the logical Identity when set. Two
// workers proposing DIFFERENT diffs for one identity must not interleave between
// the join-check and the supersede — and, for StageUnlessDeclined, a second
// worker must not read the prior offers before the first has finished writing
// one.
//
// Re-entrant within a transaction, so a caller that takes it before its own
// reads and then calls through to staging pays for it once.
func lockProposalIdentity(ctx context.Context, tx pgx.Tx, wsID ids.UUID, in StageInput) error {
	discriminator := in.DiffHash
	if len(in.Identity) > 0 {
		discriminator = string(in.Identity)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
			'approval_pending:' || $1::text || ':' || $2 || ':' || $3::text || ':' || $4, 0))`,
		wsID, in.Kind, in.TargetID, discriminator); err != nil {
		return fmt.Errorf("lock pending approval identity: %w", err)
	}
	return nil
}

// HasPendingFor reports whether a live pending staging of this kind,
// target and exact proposed change already sits in the inbox. Stagers
// fed by at-least-once triggers (connector syncs re-hitting the same
// collision) consult it so a recurring trigger cannot multiply
// identical proposals.
func (s *Service) HasPendingFor(ctx context.Context, kind string, targetID ids.UUID, diffHash string) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM approval
			  WHERE kind = $1 AND target_entity_id = $2 AND diff_hash = $3
			    AND status = 'pending' AND expires_at > now())`,
			kind, targetID, diffHash).Scan(&exists)
	})
	return exists, err
}

// HasPendingKind reports whether a live pending staging of this kind
// sits against the target at all, whatever its proposed change. Nightly
// sweeps whose proposal moves with "today" consult it — a diff-hash
// identity check (HasPendingFor) would let every pass stack a fresh
// staging on one still awaiting decision.
func (s *Service) HasPendingKind(ctx context.Context, kind string, targetID ids.UUID) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM approval
			  WHERE kind = $1 AND target_entity_id = $2
			    AND status = 'pending' AND expires_at > now())`,
			kind, targetID).Scan(&exists)
	})
	return exists, err
}

// WithdrawInTx takes one live proposal off the inbox on the caller's
// transaction: forced expiry, audited with the reason, deliberately event-free.
//
// The mechanism is supersession's — backdate expires_at a full day, so the row
// reads expired under both the database clock and the service clock that
// effectiveStatus judges with. Withdrawal is not a new status: the CHECK and the
// public ApprovalStatus enum stay closed, and expiry is already invisible on the
// bus (no subscriber can observe a TTL lapse either), so nothing a consumer
// relies on changes.
//
// It exists so an owner of the underlying question can retract it when the
// question stops being one — the capture ledger ageing out an unanswered review
// is the first caller. It reports whether the offer was still live to take:
// withdrawing an already-decided approval does nothing and says so, because what
// a human answered is not the caller's to take back, and a caller that acts on
// the retraction needs to know the retraction happened.
func (s *Service) WithdrawInTx(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, reason string) (bool, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return false, errors.New("crmapprovals: no actor bound to context")
	}
	// The same row lock decideInTx takes, for the same reason: a decision landing
	// concurrently has to be ordered against this write rather than interleaved
	// with it. A human who wins the lock leaves the row decided and this reports
	// false; one who loses re-reads an expired row and is refused.
	var locked ids.ApprovalID
	if err := tx.QueryRow(ctx, `SELECT id FROM approval WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock approval to withdraw: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE approval SET expires_at = now() - interval '1 day'
		 WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return false, fmt.Errorf("withdraw approval: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := s.audit(ctx, tx, p, "update", id.UUID, map[string]any{
		"withdrawn": true, "reason": reason,
	}); err != nil {
		return false, fmt.Errorf("audit withdrawn approval: %w", err)
	}
	return true, nil
}

// declinedProbeSQL locks every offer a proposal identity has ever produced,
// decided or not, so a decision landing on one of them is ordered against the
// staging rather than interleaved with it.
//
// WHAT the memory is keyed on decides whether it works. A caller that declares
// a logical Identity is matched by jsonb containment on that identity; only a
// caller with none falls back to the diff hash. The hash covers the WHOLE
// payload — the corroborating evidence, the record's current name — so a
// refusal keyed on it is forgotten the moment any of that moves, and the next
// pass re-offers the rename a human just refused. Containment matches the
// decision the human actually made: this record, this proposed value.
func declinedProbeSQL(byIdentity bool) string {
	const prefix = `SELECT status FROM approval
		 WHERE workspace_id = $1 AND kind = $2 AND target_entity_id IS NOT DISTINCT FROM $3 AND `
	const suffix = `
		 ` + lockOrder + `
		 FOR UPDATE`
	if byIdentity {
		return prefix + `proposed_change @> $4` + suffix
	}
	return prefix + `diff_hash = $4` + suffix
}

// RejectedChangesFor returns the proposed_change of every REJECTED proposal of
// this kind against this target. It is the read half of the memory
// StageUnlessDeclined enforces, for a caller that can APPLY a change without
// staging one: an auto-apply path that never consults it lets a stronger
// trigger execute exactly what a human refused, and the refusal is invisible
// because no approval is created to join.
//
// It hands back the PAYLOADS rather than answering a containment query,
// because only the caller knows what makes two of its proposals the same
// question. A payload written by an older version of that caller may not carry
// the field today's identity is keyed on, and a decision a human already made
// does not expire because the schema moved — deciding that in SQL would mean
// deciding it wrong.
func (s *Service) RejectedChangesFor(ctx context.Context, kind string, targetID ids.UUID) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.RejectedChangesForTx(ctx, tx, kind, targetID)
		return err
	})
	return out, err
}

// RejectedChangesForTx is RejectedChangesFor on the caller's transaction, and
// it LOCKS every offer of this kind against this target. That lock is what
// makes a check-then-apply caller safe: a plain read commits before the apply
// opens its own transaction, so a human could reject in the gap and the apply
// would go ahead anyway — the exact race StageUnlessDeclined takes the same
// lock to close. A concurrent Decide blocks on these rows until the caller
// commits.
func (s *Service) RejectedChangesForTx(ctx context.Context, tx pgx.Tx, kind string, targetID ids.UUID) ([]json.RawMessage, error) {
	// EVERY offer is locked, not only the already-rejected ones: a pending row
	// is exactly the one a human is about to reject, and leaving it unlocked
	// would reopen the gap this closes.
	rows, err := tx.Query(ctx, `
		SELECT status, proposed_change FROM approval
		 WHERE kind = $1 AND target_entity_id = $2
		 `+lockOrder+`
		 FOR UPDATE`, kind, targetID)
	if err != nil {
		return nil, fmt.Errorf("lock the offers for this proposal: %w", err)
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		var status string
		var change json.RawMessage
		if err := rows.Scan(&status, &change); err != nil {
			return nil, fmt.Errorf("read the declined offers for this proposal: %w", err)
		}
		if status == approvalStatusRejected {
			out = append(out, change)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the declined offers for this proposal: %w", err)
	}
	return out, nil
}

// StageUnlessDeclined stages in — unless a human has already REJECTED this
// proposal. Its identity is the caller's Identity when one is declared and the
// diff hash otherwise (see declinedProbeSQL). It reports whether anything was
// staged.
//
// It exists because a nightly stager re-derives the same proposal every pass, and
// JoinPending joins only a PENDING row: the moment a human says no, the next pass
// finds nothing to join and stages a fresh copy of what was just refused. Their
// "no" would mean nothing.
//
// Checking first and staging afterwards is not enough, and the gap is small but
// real: a decision landing between the two leaves the check reading "not
// declined" and the staging finding no pending row to join, so the refused offer
// is recreated anyway. The row lock closes it — the same
// `SELECT ... FOR UPDATE` decideInTx takes, so the two are ordered rather than
// interleaved. Whoever gets there first wins cleanly: the decision blocks until
// this commits and then decides the offer this joined, or this reads the row as
// already rejected and stages nothing.
func (s *Service) StageUnlessDeclined(ctx context.Context, in StageInput) (ids.ApprovalID, bool, error) {
	// Canonicalized HERE as well as in Stage: the lock discriminator, the
	// containment probe below and supersession must all agree on what "same
	// identity" means, and an uncanonicalized identity differs from the stored
	// one by key order alone — which reads as a different proposal.
	if len(in.Identity) > 0 {
		if !in.JoinPending {
			return ids.ApprovalID{}, false, errors.New("crmapprovals: Identity staging requires JoinPending")
		}
		canonical, err := canonicalIdentity(in.Identity, in.ProposedChange)
		if err != nil {
			return ids.ApprovalID{}, false, err
		}
		in.Identity = canonical
	}
	var id ids.ApprovalID
	staged := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		wsID, ok := principal.WorkspaceID(ctx)
		if !ok {
			return errors.New("crmapprovals: no workspace bound to context")
		}
		// The identity lock FIRST, before the read below — this is the ordering
		// that a row lock alone cannot give.
		//
		// `FOR UPDATE` locks the rows it finds, so it orders this against a
		// decision on an offer that already exists. It locks NOTHING when the
		// query finds nothing, and an empty result is not the same as "no offer
		// can appear": a second pass reading before the first has committed sees
		// no prior offers at all, and by the time it writes, the first pass's
		// offer may exist AND have been rejected. It would then find no PENDING
		// row to join and recreate exactly what the human refused. Serializing on
		// the identity means the second pass reads after the first has finished,
		// so the offer it must not recreate is there to be seen.
		if err := lockProposalIdentity(ctx, tx, wsID, in); err != nil {
			return err
		}
		byIdentity := len(in.Identity) > 0
		var discriminator any = in.DiffHash
		if byIdentity {
			discriminator = in.Identity
		}
		rows, err := tx.Query(ctx, declinedProbeSQL(byIdentity), wsID, in.Kind, nullUUID(in.TargetID), discriminator)
		if err != nil {
			return fmt.Errorf("lock the prior offers for this proposal: %w", err)
		}
		statuses, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read the prior offers for this proposal: %w", err)
		}
		for _, status := range statuses {
			if status == approvalStatusRejected {
				return nil
			}
		}
		if in.JoinPending {
			id, err = s.stageOrJoinPendingInTx(ctx, tx, in)
		} else {
			id, err = s.insertProposalInTx(ctx, tx, in)
		}
		if err != nil {
			return err
		}
		staged = true
		return nil
	})
	return id, staged, err
}
