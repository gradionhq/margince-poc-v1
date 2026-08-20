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

// FieldFault names the dedupe argument that was malformed.
func (e *DedupeInputError) FieldFault() (field, code, message string) {
	return e.Field, "invalid", e.Error()
}

// DedupeCandidateRow is one queue row as stored.
type DedupeCandidateRow struct {
	ID          ids.UUID
	EntityType  string // person | organization | lead
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
	// auditEntityDedupe names the queue row in its audit entries.
	auditEntityDedupe = "dedupe_candidate"
	// auditKeyDisposition is the audited field name of the queue verdict.
	auditKeyDisposition = "disposition"
	// sqlAlwaysVisible is the no-op arm of the pair-visibility predicate:
	// the caller's scope leaves that record type unbounded.
	sqlAlwaysVisible = "true"
	fieldCursor      = "cursor"
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
	if entityType == "" || entityType == entityLead {
		if err := auth.Require(ctx, entityLead, principal.ActionRead); err != nil {
			return err
		}
	}
	return nil
}

// requireDedupeWrite gates the disposition verbs: deciding a pair mutates
// the records' dedupe fate, so it needs update on the pair's entity type —
// a read-only seat can look at the queue but never decide it.
func requireDedupeWrite(ctx context.Context, entityType string) error {
	return auth.Require(ctx, entityType, principal.ActionUpdate)
}

// dedupeVisibilityClause renders the queue's row-scope filter: a candidate
// surfaces only when BOTH sides of its pair are visible to the caller —
// the evidence snapshot reads both records, so listing a pair IS a read of
// them (H1). Empty for unbounded callers.
func dedupeVisibilityClause(ctx context.Context, arg func(any) int) (string, error) {
	personClause, err := auth.ScopeClauseFor(ctx, entityPerson, "vp", arg)
	if err != nil {
		return "", err
	}
	orgClause, err := auth.ScopeClauseFor(ctx, entityOrganization, "vo", arg)
	if err != nil {
		return "", err
	}
	leadClause, err := auth.ScopeClauseFor(ctx, entityLead, "vl", arg)
	if err != nil {
		return "", err
	}
	if personClause == "" && orgClause == "" && leadClause == "" {
		return "", nil
	}
	personOK := sqlAlwaysVisible
	if personClause != "" {
		personOK = fmt.Sprintf(`(EXISTS (SELECT 1 FROM person vp WHERE vp.id = dedupe_candidate.left_person_id AND %[1]s)
			AND EXISTS (SELECT 1 FROM person vp WHERE vp.id = dedupe_candidate.right_person_id AND %[1]s))`, personClause)
	}
	orgOK := sqlAlwaysVisible
	if orgClause != "" {
		orgOK = fmt.Sprintf(`(EXISTS (SELECT 1 FROM organization vo WHERE vo.id = dedupe_candidate.left_org_id AND %[1]s)
			AND EXISTS (SELECT 1 FROM organization vo WHERE vo.id = dedupe_candidate.right_org_id AND %[1]s))`, orgClause)
	}
	leadOK := sqlAlwaysVisible
	if leadClause != "" {
		leadOK = fmt.Sprintf(`(EXISTS (SELECT 1 FROM lead vl WHERE vl.id = dedupe_candidate.left_lead_id AND %[1]s)
			AND EXISTS (SELECT 1 FROM lead vl WHERE vl.id = dedupe_candidate.right_lead_id AND %[1]s))`, leadClause)
	}
	return fmt.Sprintf(`((entity_type = 'person' AND %s) OR (entity_type = 'organization' AND %s) OR (entity_type = 'lead' AND %s))`,
		personOK, orgOK, leadOK), nil
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
		SELECT id, entity_type, coalesce(left_person_id, left_org_id, left_lead_id), coalesce(right_person_id, right_org_id, right_lead_id),
		       confidence, evidence, disposition, disposed_by, disposed_at, created_at
		FROM dedupe_candidate
		WHERE disposition = $1 AND archived_at IS NULL`
	args := []any{in.Status}
	visClause, err := dedupeVisibilityClause(ctx, func(v any) int { args = append(args, v); return len(args) })
	if err != nil {
		return nil, "", err
	}
	if visClause != "" {
		query += " AND " + visClause
	}
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
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
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

// GetDedupeCandidate reads one row with its full evidence. Both sides of
// the pair must pass the caller's row scope — the evidence names them
// both, so a pair with an out-of-scope side reads as absent (404,
// existence-hiding).
func (s *Store) GetDedupeCandidate(ctx context.Context, id ids.UUID) (DedupeCandidateRow, error) {
	var row DedupeCandidateRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		row, err = readDedupeCandidate(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := auth.EnsureVisible(ctx, tx, row.EntityType, row.LeftID); err != nil {
			return err
		}
		return auth.EnsureVisible(ctx, tx, row.EntityType, row.RightID)
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
		SELECT id, entity_type, coalesce(left_person_id, left_org_id, left_lead_id), coalesce(right_person_id, right_org_id, right_lead_id),
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
