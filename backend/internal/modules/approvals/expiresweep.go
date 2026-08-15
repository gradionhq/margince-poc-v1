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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// expirySweepBatch bounds one pass. A backlog is drained across ticks rather
// than in one transaction: each expiry is an independent decision with its own
// audit row and event, and holding thousands of them open would make the sweep
// a lock the inbox waits behind.
const expirySweepBatch = 200

// ExpiredApproval is one staging the clock decided against, and what a caller
// needs to finish the job outside this module.
//
// Kind and TargetType/TargetID are here rather than looked up again because the
// row they came from is already gone from `pending` by the time a caller acts on
// it — a second read would race the next sweep.
type ExpiredApproval struct {
	ID         ids.ApprovalID
	Kind       string
	TargetType string
	TargetID   ids.UUID
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
func (s *Service) ExpireDue(ctx context.Context) ([]ExpiredApproval, error) {
	due, err := s.dueForExpiry(ctx)
	if err != nil {
		return nil, err
	}
	expired := make([]ExpiredApproval, 0, len(due))
	for _, candidate := range due {
		ok, err := s.expireOne(ctx, candidate.ID)
		if err != nil {
			return expired, err
		}
		if ok {
			expired = append(expired, candidate)
		}
	}
	return expired, nil
}

// dueForExpiry reads the candidates. It is a plain read outside any
// transaction: each row is re-checked under its own lock before being written,
// so a candidate that stops being due between the two is simply skipped.
func (s *Service) dueForExpiry(ctx context.Context) ([]ExpiredApproval, error) {
	var due []ExpiredApproval
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, kind, target_entity_type, target_entity_id
			  FROM approval
			 WHERE status = 'pending' AND expires_at <= $1
			 ORDER BY expires_at
			 LIMIT $2`, s.now().UTC(), expirySweepBatch)
		if err != nil {
			return fmt.Errorf("crmapprovals: reading the stagings whose window closed: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a ExpiredApproval
			var targetType *string
			var targetID *ids.UUID
			if err := rows.Scan(&a.ID, &a.Kind, &targetType, &targetID); err != nil {
				return err
			}
			// A kind that never expires is filtered HERE rather than in SQL: the
			// exemption is a Go predicate the read path already owns, and a
			// second copy of it as a SQL literal list is the drift this file's
			// header warns about.
			if ExpiresNever(a.Kind) {
				continue
			}
			if targetType != nil {
				a.TargetType = *targetType
			}
			if targetID != nil {
				a.TargetID = *targetID
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
		if err := s.emit(ctx, tx, p, auditID, id.UUID, crmcontracts.PublicEventApprovalDecided{
			Kind: a.Kind, Verdict: StatusExpired,
		}); err != nil {
			return err
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
