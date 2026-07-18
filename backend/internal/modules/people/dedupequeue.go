// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The dedupe review queue over dedupe_candidate (DH-DDL-1, DH-EXT-1/2):
// confidence-sorted reads with the detection-time evidence snapshot
// (DH-N-8 — rendered as captured, never re-derived), and the two
// dispositions. `merge` executes the owner's merge verb — mergePerson /
// mergeOrganization, ONE merge in the system — and `not_a_duplicate`
// flips the row that suppresses the pair from every future sweep
// (AC-dedupe-7: the unique pair index meets the row and re-proposes
// nothing).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ErrNotUndoable marks an undo on a merged pair: the merge verb's
// reversibility (PO-AC-M6) is not built, so the merge stands (409).
var ErrNotUndoable = errors.New("people: a merged pair cannot be re-opened — the merge stands")

// DedupeInputError marks caller input the queue refuses (422 on the wire).
type DedupeInputError struct {
	Field string
	Msg   string
}

func (e *DedupeInputError) Error() string { return "people: " + e.Field + ": " + e.Msg }

// DedupeCandidateRow is one queue row as stored.
type DedupeCandidateRow struct {
	ID          ids.UUID
	EntityType  string // person | organization
	LeftID      ids.UUID
	RightID     ids.UUID
	Confidence  float64
	Evidence    json.RawMessage // the detection-time snapshot, verbatim
	Disposition string          // open | merged | not_a_duplicate
	DisposedBy  *ids.UUID
	DisposedAt  *time.Time
	CreatedAt   time.Time
}

// DedupeQueueInput filters one list page.
type DedupeQueueInput struct {
	Status     string // open (default) | merged | not_a_duplicate
	EntityType string // "" = both
	Cursor     string
	Limit      int
}

const (
	dedupeQueueDefaultLimit = 25
	dispositionOpen         = "open"
	dispositionMerged       = "merged"
	dispositionNotDuplicate = "not_a_duplicate"
	fieldCursor             = "cursor"
)

// dedupeCursor is the queue's keyset: confidence-descending with the id
// as the tiebreak — opaque on the wire.
type dedupeCursor struct {
	Confidence float64  `json:"c"`
	ID         ids.UUID `json:"id"`
}

func encodeDedupeCursor(c dedupeCursor) string {
	b, _ := json.Marshal(c) //nolint:errchkjson // fixed-shape struct never errors
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeDedupeCursor(token string) (dedupeCursor, error) {
	var c dedupeCursor
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return c, &DedupeInputError{Field: fieldCursor, Msg: "malformed page token"}
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, &DedupeInputError{Field: fieldCursor, Msg: "malformed page token"}
	}
	return c, nil
}

// requireDedupeRead gates the queue read on the entities it exposes: the
// unfiltered queue shows both record types, so it needs both reads.
func requireDedupeRead(ctx context.Context, entityType string) error {
	if entityType == "" || entityType == entityPerson {
		if err := auth.Require(ctx, entityPerson, principal.ActionRead); err != nil {
			return err
		}
	}
	if entityType == "" || entityType == entityOrganization {
		if err := auth.Require(ctx, entityOrganization, principal.ActionRead); err != nil {
			return err
		}
	}
	return nil
}

// ListDedupeCandidates pages the queue, confidence-sorted (AC-dedupe-1).
func (s *Store) ListDedupeCandidates(ctx context.Context, in DedupeQueueInput) ([]DedupeCandidateRow, string, error) {
	if err := requireDedupeRead(ctx, in.EntityType); err != nil {
		return nil, "", err
	}
	if in.Status == "" {
		in.Status = dispositionOpen
	}
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = dedupeQueueDefaultLimit
	}

	query := `
		SELECT id, entity_type, coalesce(left_person_id, left_org_id), coalesce(right_person_id, right_org_id),
		       confidence, evidence, disposition, disposed_by, disposed_at, created_at
		FROM dedupe_candidate
		WHERE disposition = $1 AND archived_at IS NULL`
	args := []any{in.Status}
	if in.EntityType != "" {
		args = append(args, in.EntityType)
		query += fmt.Sprintf(" AND entity_type = $%d", len(args))
	}
	if in.Cursor != "" {
		cur, err := decodeDedupeCursor(in.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, cur.Confidence, cur.ID)
		query += fmt.Sprintf(" AND (confidence, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, in.Limit+1)
	query += fmt.Sprintf(" ORDER BY confidence DESC, id DESC LIMIT $%d", len(args))

	var rows []DedupeCandidateRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var r DedupeCandidateRow
			if err := res.Scan(&r.ID, &r.EntityType, &r.LeftID, &r.RightID, &r.Confidence,
				&r.Evidence, &r.Disposition, &r.DisposedBy, &r.DisposedAt, &r.CreatedAt); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return res.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("people: listing dedupe candidates: %w", err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next = encodeDedupeCursor(dedupeCursor{Confidence: last.Confidence, ID: last.ID})
	}
	return rows, next, nil
}

// GetDedupeCandidate reads one row with its full evidence.
func (s *Store) GetDedupeCandidate(ctx context.Context, id ids.UUID) (DedupeCandidateRow, error) {
	var row DedupeCandidateRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = readDedupeCandidate(ctx, tx, id)
		return err
	})
	if err != nil {
		return DedupeCandidateRow{}, err
	}
	if err := requireDedupeRead(ctx, row.EntityType); err != nil {
		return DedupeCandidateRow{}, err
	}
	return row, nil
}

func readDedupeCandidate(ctx context.Context, tx pgx.Tx, id ids.UUID) (DedupeCandidateRow, error) {
	var r DedupeCandidateRow
	err := tx.QueryRow(ctx, `
		SELECT id, entity_type, coalesce(left_person_id, left_org_id), coalesce(right_person_id, right_org_id),
		       confidence, evidence, disposition, disposed_by, disposed_at, created_at
		FROM dedupe_candidate WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&r.ID, &r.EntityType, &r.LeftID, &r.RightID, &r.Confidence,
			&r.Evidence, &r.Disposition, &r.DisposedBy, &r.DisposedAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperrors.ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("people: reading dedupe candidate: %w", err)
	}
	return r, nil
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
	if row.Disposition != dispositionOpen {
		return DedupeCandidateRow{}, fmt.Errorf("people: candidate already disposed (%s): %w", row.Disposition, apperrors.ErrConflict)
	}

	switch disposition {
	case dispositionNotDuplicate:
		if err := s.setDedupeDisposition(ctx, id, dispositionNotDuplicate, actor.UserID); err != nil {
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
// open, so a concurrent decision cannot double-merge), then run the ONE
// merge verb in its own transaction. A merge failure re-opens the row —
// the queue never claims a merge that did not happen.
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
	default:
		return fmt.Errorf("people: unmergeable entity type %q", entityType)
	}
}

// setDedupeDisposition is the CAS open→disposed; losing the race answers
// conflict, never a second merge.
func (s *Store) setDedupeDisposition(ctx context.Context, id ids.UUID, disposition string, by ids.UUID) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE dedupe_candidate SET disposition = $2, disposed_by = $3, disposed_at = now()
			WHERE id = $1 AND disposition = 'open'`, id, disposition, by)
		if err != nil {
			return fmt.Errorf("people: disposing dedupe candidate: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("people: candidate already disposed: %w", apperrors.ErrConflict)
		}
		return nil
	})
}

func (s *Store) reopenDedupeCandidate(ctx context.Context, id ids.UUID) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE dedupe_candidate SET disposition = 'open', disposed_by = NULL, disposed_at = NULL
			WHERE id = $1 AND disposition <> 'open'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Already open (a concurrent undo) — the desired state holds.
			return nil
		}
		return nil
	})
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
	switch row.Disposition {
	case dispositionOpen:
		return DedupeCandidateRow{}, fmt.Errorf("people: candidate is already open: %w", apperrors.ErrConflict)
	case dispositionMerged:
		return DedupeCandidateRow{}, fmt.Errorf("%w: %w", ErrNotUndoable, apperrors.ErrConflict)
	}
	if err := s.reopenDedupeCandidate(ctx, id); err != nil {
		return DedupeCandidateRow{}, err
	}
	return s.GetDedupeCandidate(ctx, id)
}
