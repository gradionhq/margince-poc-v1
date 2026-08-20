// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The decision half of the dedupe review queue: the three verbs a human may
// spend on a pair, and the authority each of them needs. dedupequeue.go owns
// the reading half — the list, the single read and their row scope.
//
// A verdict is not a read. Dismissing a pair suppresses two records as
// duplicates for the whole workspace and an undo puts them back into everyone's
// queue, so both carry write authority over BOTH records rather than the
// visibility the read gate applies. Merge takes its own, inside the merge verb,
// with a refusal shape the other two deliberately do not share.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ensurePairWritable narrows the pair's read gate to the authority a decision
// needs. A disposition CHANGES both records the candidate names — a dismissal
// suppresses them as duplicates for the whole workspace, an undo puts the pair
// back into everyone's queue — so each end carries the write-authority probe
// rather than the visibility one, exactly as mergePair states it for the merge
// verb: a colleague handed a `read` share of either record may not spend it on
// the queue's verdict.
//
// The object grant is not that authority and never was. requireDedupeWrite asks
// for update on the PAIR'S OWN type, and all three types are workspace-readable
// identity — so a seat holding organization:update passes it over every
// colleague's organizations, and likewise for the other two.
//
// It refuses with 403, not 404: GetDedupeCandidate has already told this caller
// the pair is theirs to read, so there is nothing left for existence-hiding to
// hide (platform/auth/writescope.go states the same rule for every exported
// spelling). entityType is the table name, and all three are row-scoped.
//
// It takes the CALLER'S transaction rather than opening its own, so the probe
// and the queue write commit together. Split across two transactions, a grant
// revoked in between would leave the write to land on an authority that no
// longer exists, and the CAS on disposition='open' does not close that — it
// guards lost updates, not authority.
func ensurePairWritable(ctx context.Context, tx pgx.Tx, entityType string, left, right ids.UUID) error {
	if err := auth.EnsureWritable(ctx, tx, entityType, left); err != nil {
		return err
	}
	return auth.EnsureWritable(ctx, tx, entityType, right)
}

// DisposeDedupeCandidate decides one pair. merge executes the owner's
// merge verb with the LOSER folding into the winner; not_a_duplicate
// suppresses the pair forever. Human-only (the transport enforces the
// x-agent-access posture; the store re-checks the principal).
func (s *Store) DisposeDedupeCandidate(ctx context.Context, id ids.UUID, disposition string, winnerID *ids.UUID) (DedupeCandidateRow, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return DedupeCandidateRow{}, fmt.Errorf("people: only a human disposes a dedupe pair: %w", apperrors.ErrPermissionDenied)
	}
	row, err := s.GetDedupeCandidate(ctx, id)
	if err != nil {
		return DedupeCandidateRow{}, err
	}
	if err := requireDedupeWrite(ctx, row.EntityType); err != nil {
		return DedupeCandidateRow{}, err
	}
	if row.Disposition != dispositionOpen {
		return DedupeCandidateRow{}, fmt.Errorf("people: candidate already disposed (%s): %w", row.Disposition, apperrors.ErrConflict)
	}

	switch disposition {
	case dispositionNotDuplicate:
		// Per arm, not before the switch. The merge arm takes its own
		// write-authority probe through mergePair, with an asymmetry this one
		// does not share: mergePair answers a BARE CONFLICT for a target the
		// caller cannot change, deliberately, so the refusal names no record
		// the caller has not already been handed. A probe out here would
		// pre-empt that with 403 and change a refusal this fix is not about.
		if err := s.writePairDecision(ctx, row, func(ctx context.Context, tx pgx.Tx) error {
			return setDedupeDispositionTx(ctx, tx, row.ID, dispositionNotDuplicate, actor.UserID)
		}); err != nil {
			return DedupeCandidateRow{}, err
		}
	case "merge":
		if err := s.disposeMerge(ctx, id, row, winnerID, actor.UserID); err != nil {
			return DedupeCandidateRow{}, err
		}
	default:
		return DedupeCandidateRow{}, &DedupeInputError{Field: "disposition", Msg: "must be merge or not_a_duplicate"}
	}
	return s.GetDedupeCandidate(ctx, id)
}

// disposeMerge is the merge arm: validate the winner, mark first (a CAS on
// open, so a concurrent decision cannot double-merge), then run the ONE merge
// verb in its own transaction. A merge the verb REFUSES re-opens the row.
//
// That compensation is not a guarantee, and saying so is the point: the mark
// and the merge are separate transactions, so a reopen that itself fails — or
// a process that stops between the two — leaves the candidate at 'merged' with
// no merge behind it, suppressed for the whole workspace. errors.Join reports
// it and nothing repairs it. Dismiss and undo do not have this shape; they
// commit their probe and their write together through writePairDecision. The
// merge arm cannot yet, because the merge verbs are Store methods that open
// their own transactions rather than joining a caller's. Tracked as its own
// issue (#1970); do not read the compensation as atomicity.
func (s *Store) disposeMerge(ctx context.Context, id ids.UUID, row DedupeCandidateRow, winnerID *ids.UUID, by ids.UUID) error {
	if winnerID == nil || (*winnerID != row.LeftID && *winnerID != row.RightID) {
		return &DedupeInputError{Field: "winner_id", Msg: "must be one of the pair"}
	}
	loser := row.LeftID
	if loser == *winnerID {
		loser = row.RightID
	}
	if err := s.setDedupeDisposition(ctx, id, dispositionMerged, by); err != nil {
		return err
	}
	if err := s.executeDedupeMerge(ctx, row.EntityType, loser, *winnerID); err != nil {
		if reopenErr := s.reopenDedupeCandidate(ctx, id); reopenErr != nil {
			return errors.Join(err, reopenErr)
		}
		return err
	}
	return nil
}

// executeDedupeMerge runs the ONE merge implementation for the pair's type.
func (s *Store) executeDedupeMerge(ctx context.Context, entityType string, loser, winner ids.UUID) error {
	switch entityType {
	case entityPerson:
		_, err := s.MergePerson(ctx, ids.From[ids.PersonKind](loser), ids.From[ids.PersonKind](winner))
		return err
	case entityOrganization:
		_, err := s.MergeOrganization(ctx, ids.From[ids.OrganizationKind](loser), ids.From[ids.OrganizationKind](winner))
		return err
	case entityLead:
		_, err := s.MergeLead(ctx, ids.From[ids.LeadKind](loser), ids.From[ids.LeadKind](winner))
		return err
	default:
		return fmt.Errorf("people: unmergeable entity type %q", entityType)
	}
}

// setDedupeDisposition is the CAS open→disposed; losing the race answers
// conflict, never a second merge. The audit row rides the same commit;
// dedupe_candidate is not a §4.1 stream entity, so the disposition has no
// bus event — the audit ledger is the record (the merge arm's
// person.merged/organization.merged carries the bus-visible fact).
func (s *Store) setDedupeDisposition(ctx context.Context, id ids.UUID, disposition string, by ids.UUID) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		return setDedupeDispositionTx(ctx, tx, id, disposition, by)
	})
}

// writePairDecision commits a human's verdict on a pair under the authority
// that verdict needs: ensurePairWritable and the write share ONE transaction,
// so a grant revoked between them cannot leave the write standing on authority
// that is already gone.
//
// The WRITE is the parameter and the probe is not, which is the direction that
// matters. A caller cannot reach this and skip the guard; it can only choose
// which decision to record under it. The paths that must NOT probe —
// disposeMerge's mark and its compensating rollback — do not come through here
// at all, so no flag decides whether a guard runs.
func (s *Store) writePairDecision(ctx context.Context, row DedupeCandidateRow,
	write func(context.Context, pgx.Tx) error,
) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensurePairWritable(ctx, tx, row.EntityType, row.LeftID, row.RightID); err != nil {
			return err
		}
		return write(ctx, tx)
	})
}

func setDedupeDispositionTx(ctx context.Context, tx pgx.Tx, id ids.UUID, disposition string, by ids.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE dedupe_candidate SET disposition = $2, disposed_by = $3, disposed_at = now()
		WHERE id = $1 AND disposition = 'open'`, id, disposition, by)
	if err != nil {
		return fmt.Errorf("people: disposing dedupe candidate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("people: candidate already disposed: %w", apperrors.ErrConflict)
	}
	_, err = storekit.Audit(ctx, tx, "resolve", auditEntityDedupe, id,
		map[string]any{auditKeyDisposition: dispositionOpen},
		map[string]any{auditKeyDisposition: disposition})
	return err
}

func (s *Store) reopenDedupeCandidate(ctx context.Context, id ids.UUID) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		return reopenDedupeCandidateTx(ctx, tx, id)
	})
}

func reopenDedupeCandidateTx(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE dedupe_candidate SET disposition = 'open', disposed_by = NULL, disposed_at = NULL
		WHERE id = $1 AND disposition <> 'open'`, id)
	if err != nil {
		return fmt.Errorf("people: re-opening dedupe candidate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already open (a concurrent undo) — the desired state holds.
		return nil
	}
	_, err = storekit.Audit(ctx, tx, "restore", auditEntityDedupe, id,
		nil, map[string]any{auditKeyDisposition: dispositionOpen})
	return err
}

// UndoDedupeDisposition re-opens a dismissed pair (the suppression lifts).
// A merged pair answers ErrNotUndoable: reversing a merge needs the merge
// verb's own reversibility (PO-AC-M6), which does not exist yet — the
// queue must not pretend otherwise.
func (s *Store) UndoDedupeDisposition(ctx context.Context, id ids.UUID) (DedupeCandidateRow, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return DedupeCandidateRow{}, fmt.Errorf("people: only a human re-opens a dedupe pair: %w", apperrors.ErrPermissionDenied)
	}
	row, err := s.GetDedupeCandidate(ctx, id)
	if err != nil {
		return DedupeCandidateRow{}, err
	}
	if err := requireDedupeWrite(ctx, row.EntityType); err != nil {
		return DedupeCandidateRow{}, err
	}
	switch row.Disposition {
	case dispositionOpen:
		return DedupeCandidateRow{}, fmt.Errorf("people: candidate is already open: %w", apperrors.ErrConflict)
	case dispositionMerged:
		return DedupeCandidateRow{}, fmt.Errorf("%w: %w", ErrNotUndoable, apperrors.ErrConflict)
	}
	// Through writePairDecision, not reopenDedupeCandidate directly: that
	// function has a second caller which is not a user act — disposeMerge
	// reopens the row as the compensating rollback of a merge that failed, and
	// a grant revoked mid-flight must not strand a candidate at 'merged' with
	// no merge behind it.
	if err := s.writePairDecision(ctx, row, func(ctx context.Context, tx pgx.Tx) error {
		return reopenDedupeCandidateTx(ctx, tx, row.ID)
	}); err != nil {
		return DedupeCandidateRow{}, err
	}
	return s.GetDedupeCandidate(ctx, id)
}
