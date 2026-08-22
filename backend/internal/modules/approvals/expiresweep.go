// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// Expiry, written down.
//
// Until now expiry was a READING and never a fact: effectiveStatus folded it in
// at read time, so a stale staging displayed as expired everywhere and the row
// still said `pending`. That is enough to stop it being decided, and not enough
// for anything else. Nothing was audited, so an item that auto-rejected left no
// record of having done so — and the spec's own words for expiry are
// "unactioned means rejected … the expiry is logged like any other decision,
// attributed to a system actor" (APPR-PARAM-1, APPR-AC-2). Nothing was emitted,
// so an automation parked behind the staging waited on a decision that had
// already been taken against it, forever (AUTO-AC-10 expects a blocked run).
//
// This is the sweep that makes the reading true. It writes the same three things
// a human decision writes — the status, the audit row, the event — under a
// system actor rather than a person, because nobody decided: the clock did.
//
// The lazy reading STAYS. It is what keeps a row correct between sweeps, and
// removing it would make expiry depend on a worker being alive. The two agree
// by construction because both ask ExpiresNever and both compare against the
// same column; this one just also writes the answer down.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// expirySweepBatch bounds one pass. A backlog is drained across ticks rather
// than in one transaction: each expiry is an independent decision with its own
// audit row and event, and holding thousands of them open would make the sweep
// a lock the inbox waits behind.
const expirySweepBatch = 200

// ExpiredApproval is one staging the clock decided against.
//
// The id alone, deliberately. An earlier shape carried the kind and target so a
// caller could "finish the job outside this module", but no caller may: the run
// transition rides the approval.decided event this sweep emits, precisely so
// the job reaches no other module (jobs_approvalexpiry.go says why at length).
// Fields whose only justification is a caller the design forbids are fields
// nobody reads, and the sweep's own tests are the only thing that would notice
// them go wrong.
type ExpiredApproval struct {
	ID   ids.ApprovalID
	Kind string
}

// ExpireDue writes the terminal outcome for every staging whose window has
// closed, and reports what it decided.
//
// One transaction per row, deliberately. An expiry is a decision, and a batch
// that half-committed would leave some rows audited and others not — the same
// split the write shape exists to prevent. A row that fails is skipped and the
// next tick sees it again, because the predicate is the clock and the clock does
// not move backwards.
//
// Kinds exempt from expiry are excluded by the same predicate the read path
// uses. There is no second definition of "due" here: a divergence between the
// two would be a row that displays as expired and is never swept, or one swept
// while the inbox still offers it.
//
// The CLOCK may call this and nobody else. Every other entry point on this
// service gates on what the caller may see; this one decides approvals in bulk
// without consulting any human's row scope, so the only safe admission rule is
// that no human is calling. Left open it would be an authenticated user's way to
// refuse every pending approval in the installation at once — each one audited
// as though the clock had done it, with their name nowhere in the record.
func (s *Service) ExpireDue(ctx context.Context) ([]ExpiredApproval, error) {
	if err := onlyTheExpirySweep(ctx); err != nil {
		return nil, err
	}
	due, err := s.dueForExpiry(ctx)
	if err != nil {
		return nil, err
	}
	// One bad row must not starve the rest. Candidates come back oldest-first
	// and the batch is capped, so returning on the first failure would let a
	// single permanently-failing approval occupy the front of every batch on
	// every tick — and nothing behind it would ever expire again. The pass
	// continues and reports the failures together, which keeps the job's own
	// retry honest without making the queue depend on the worst row in it.
	expired := make([]ExpiredApproval, 0, len(due))
	var failures []error
	for _, candidate := range due {
		ok, err := s.expireOne(ctx, candidate.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("approval %s: %w", candidate.ID, err))
			continue
		}
		if ok {
			expired = append(expired, candidate)
		}
	}
	return expired, errors.Join(failures...)
}

// dueForExpiry reads the candidates. It is a plain read outside any
// transaction: each row is re-checked under its own lock before being written,
// so a candidate that stops being due between the two is simply skipped.
func (s *Service) dueForExpiry(ctx context.Context) ([]ExpiredApproval, error) {
	var due []ExpiredApproval
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The exempt kinds are excluded HERE rather than after the read. A
		// filter applied to the batch is not the same query as a filter applied
		// to the table: exempt rows inside the window consume slots and produce
		// nothing, so enough of them fill every batch and starve every
		// genuinely-due approval behind them. The list is bound from the same
		// map ExpiresNever reads, so there is still one definition of exempt.
		rows, err := tx.Query(ctx, `
			SELECT id, kind
			  FROM approval
			 WHERE status = 'pending'
			   AND expires_at <= $1
			   AND kind <> ALL($3::text[])
			 ORDER BY expires_at
			 LIMIT $2`, s.now().UTC(), expirySweepBatch, neverExpiringKinds())
		if err != nil {
			return fmt.Errorf("crmapprovals: reading the stagings whose window closed: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a ExpiredApproval
			if err := rows.Scan(&a.ID, &a.Kind); err != nil {
				return err
			}
			// Belt and braces on the exemption the query already applies. The
			// SQL filter is the one that matters — a Go filter after the LIMIT
			// lets exempt rows consume batch slots — and this second check costs
			// nothing while making a future query edit that drops the clause
			// fail the sweep's own tests rather than production.
			if ExpiresNever(a.Kind) {
				continue
			}
			due = append(due, a)
		}
		return rows.Err()
	})
	return due, err
}

// expireOne writes one expiry in the write shape, and reports whether it did.
//
// false with no error is the ordinary outcome for a row somebody decided
// between the read and this write: the lock re-reads the status, and a decided
// row is left exactly as its decider left it. It is not an error and it is not
// a retry — the clock lost that race and the human won it, which is the right
// way round.
func (s *Service) expireOne(ctx context.Context, id ids.ApprovalID) (bool, error) {
	swept := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var locked ids.ApprovalID
		if err := tx.QueryRow(ctx, `SELECT id FROM approval WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		a, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		// Re-checked under the lock, against the STORED status rather than the
		// folded reading: effectiveStatus already answers "expired" for this
		// row, so asking it here would be asking whether the thing we are about
		// to write is already true. What matters is that nobody decided it in
		// the meantime, and that it is genuinely due.
		if a.Status != statusPending || ExpiresNever(a.Kind) || !s.now().After(a.ExpiresAt) {
			return nil
		}

		if _, err := tx.Exec(ctx,
			`UPDATE approval SET status = $2, decided_at = now(),
			        decision_reason = 'the approval window closed with nobody deciding'
			  WHERE id = $1`, id, StatusExpired); err != nil {
			return fmt.Errorf("crmapprovals: recording an expiry: %w", err)
		}

		// decided_by stays NULL and the actor is the system: nobody decided
		// this, and naming a person would put a human's name on a refusal they
		// never made. That is the whole difference between this and Decide.
		p := principal.Principal{Type: principal.PrincipalSystem, ID: ExpiryActor}
		auditID, err := s.audit(ctx, tx, p, "expire", id.UUID, map[string]any{
			approvalKeyKind:   a.Kind,
			"verdict":         StatusExpired,
			approvalKeyReason: "unactioned: the approval window closed",
		})
		if err != nil {
			return err
		}
		// The same event a decision emits, carrying the same verdict vocabulary
		// — a consumer that acts on a rejection must act on this too, and one
		// that had to learn a second event type to notice would be one more
		// place for the two to disagree.
		// DecidedBy is left unset, and the contract makes that expressible: an
		// expiry has no deciding human, and a zero uuid here would attribute a
		// refusal to a user id that resolves to nobody.
		if err := s.emit(ctx, tx, p, auditID, id.UUID, crmcontracts.PublicEventApprovalDecided{
			Kind: a.Kind, Verdict: crmcontracts.Expired,
		}); err != nil {
			return err
		}
		// In the expiry's own transaction, like a decline's effect in the
		// decision's: a subject left waiting by an expiry that committed without
		// it would be exactly the orphan the hook exists to prevent.
		if effect, ok := s.expiries[a.Kind]; ok {
			if err := effect(ctx, tx, id, a.ProposedChange); err != nil {
				return fmt.Errorf("crmapprovals: the %s expiry effect: %w", a.Kind, err)
			}
		}
		swept = true
		return nil
	})
	return swept, err
}

// ExpiryActor names the clock on the audit row. A system id rather than a
// person: APPR-AC-2 asks for the expiry to be "attributed to a system actor",
// and the reason is legibility rather than ceremony — somebody reading the trail
// must be able to tell a refusal a colleague made from one nobody made.
const ExpiryActor = "system:approval-expiry"

// onlyTheExpirySweep admits the scheduled sweep and refuses everyone else.
//
// It checks the ACTOR ID as well as the principal type, because "some system
// principal" is not the claim being made: the audit rows this pass writes are
// attributed to ExpiryActor, and a caller who cannot present that id would be
// writing decisions under a name that is not theirs.
func onlyTheExpirySweep(ctx context.Context) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return fmt.Errorf("crmapprovals: expiry sweep without a bound actor: %w", apperrors.ErrPermissionDenied)
	}
	if p.Type != principal.PrincipalSystem || p.ID != ExpiryActor {
		return fmt.Errorf("crmapprovals: %s may not expire approvals — the sweep runs as %s: %w",
			p.ID, ExpiryActor, apperrors.ErrPermissionDenied)
	}
	return nil
}
