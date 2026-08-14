// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The reversal's own states (IEM-WIRE-9), reachable only from `complete`
// and only for the csv connector. `undoing` doubles as "in progress" AND
// "interrupted, resume by calling undo again" — the same resumable-failure
// shape StatusFailed/Resume already give the forward commit, without a
// second terminal state: a row Reverse cannot process (a genuine store
// error) simply stops the loop and leaves the run here, exactly where a
// retry picks it back up.
const (
	StatusUndoing = "undoing"
	StatusUndone  = "undone"
)

// UndoWriters is the reversal seam (IEM-WIRE-9): compose implements it over
// the object stores a csv import can create, the same module-never-imports-
// a-sibling shape Writers uses. Reverse must be idempotent on the native
// id — a resumed undo may replay a row the checkpoint advanced past but
// never committed (the same crash window Writers.Ensure already has to
// tolerate for the forward commit).
type UndoWriters interface {
	Reverse(ctx context.Context, object string, nativeID ids.UUID) error
}

// KeptRow is one import-created row a human edited since import, therefore
// left in place rather than reversed (A93).
type KeptRow struct {
	Object string   `json:"object"`
	ID     ids.UUID `json:"id"`
}

// UndoReport is the reversal outcome (IEM-WIRE-9; A93's "kept — you edited
// these" list). Since is the reference instant the human-edit check reads —
// captured once, when the reversal starts, so a resumed attempt reads the
// same instant rather than a goalpost that moved with every checkpoint.
type UndoReport struct {
	Since         time.Time `json:"since"`
	ReversedCount int       `json:"reversed_count"`
	Kept          []KeptRow `json:"kept,omitempty"`
}

// mapRow is one import_record_map row this run created.
type mapRow struct {
	object     string
	externalID string
	nativeID   ids.UUID
}

// Undo reverses a completed CSV import run: every row it created that
// nobody has edited since is archived; a row a human touched after import
// is left exactly as they left it and named in the report (A93). Never an
// all-or-nothing hard rollback — reversed and kept are both facts the
// operator needs, not one boolean.
func (s *RunStore) Undo(ctx context.Context, id RunID, w UndoWriters) (UndoReport, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionUpdate); err != nil {
		return UndoReport{}, err
	}
	run, rep, err := s.beginUndo(ctx, id)
	if err != nil {
		return UndoReport{}, err
	}

	rows, err := s.mapRowsForRun(ctx, id, run.Checkpoint)
	if err != nil {
		return UndoReport{}, err
	}
	edited, err := s.humanEditedSince(ctx, rows, rep.Since)
	if err != nil {
		return UndoReport{}, err
	}

	processed := run.Checkpoint
	for _, r := range rows {
		switch {
		case edited[r.nativeID]:
			rep.Kept = append(rep.Kept, KeptRow{Object: r.object, ID: r.nativeID})
		default:
			if err := w.Reverse(ctx, r.object, r.nativeID); err != nil {
				return UndoReport{}, fmt.Errorf("import run %s undo: reversing %s %s: %w", id, r.object, r.nativeID, err)
			}
			rep.ReversedCount++
		}
		processed++
		if err := s.advanceUndoCheckpoint(ctx, id, processed, rep); err != nil {
			return UndoReport{}, err
		}
	}

	if err := s.completeUndo(ctx, id, rep); err != nil {
		return UndoReport{}, err
	}
	return rep, nil
}

// beginUndo validates the run and starts (or resumes) its reversal,
// returning the run as it stood and the report to carry forward: fresh
// (Since = the run's completion instant) when starting, or a resumed
// attempt's own progress when continuing one `undoing` already.
func (s *RunStore) beginUndo(ctx context.Context, id RunID) (Run, UndoReport, error) {
	var run Run
	var rep UndoReport
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var undoReportRaw []byte
		// FOR UPDATE: two concurrent undo calls on the same run must not
		// both read `complete` and both start a fresh reversal — the row
		// lock makes the read-then-transition below race-free.
		row := tx.QueryRow(ctx, `
			SELECT id, connector, status, checkpoint, updated_at, undo_report
			  FROM import_run
			 WHERE id = $1 AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			 FOR UPDATE`, id)
		if err := row.Scan(&run.ID, &run.Connector, &run.Status, &run.Checkpoint, &run.UpdatedAt, &undoReportRaw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return fmt.Errorf("reading import run %s: %w", id, err)
		}
		if run.Connector != ConnectorCSV {
			return fmt.Errorf("import run %s is a %s run, and undo is built for csv only: %w", id, run.Connector, apperrors.ErrConflict)
		}
		before := run.Status
		switch run.Status {
		case StatusComplete:
			rep = UndoReport{Since: run.UpdatedAt}
			run.Checkpoint = 0
		case StatusUndoing:
			if len(undoReportRaw) == 0 {
				return fmt.Errorf("import run %s is undoing with no recorded progress: %w", id, apperrors.ErrConflict)
			}
			if err := json.Unmarshal(undoReportRaw, &rep); err != nil {
				return fmt.Errorf("decoding import run %s undo progress: %w", id, err)
			}
		default:
			return fmt.Errorf("import run %s is %s, not complete, so it cannot be undone: %w", id, run.Status, apperrors.ErrConflict)
		}
		encoded, err := json.Marshal(rep)
		if err != nil {
			return fmt.Errorf("encoding import run %s undo progress: %w", id, err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET status = $2, checkpoint = $3, undo_report = $4, updated_at = now()
			 WHERE id = $1`, id, StatusUndoing, run.Checkpoint, encoded)
		if err != nil {
			return fmt.Errorf("starting import run %s undo: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s: %w", id, apperrors.ErrNotFound)
		}
		_, err = storekit.Audit(ctx, tx, "update", importRunObject, id,
			map[string]any{auditFieldStatus: before}, map[string]any{auditFieldStatus: StatusUndoing})
		return err
	})
	if err != nil {
		return Run{}, UndoReport{}, err
	}
	run.Status = StatusUndoing
	return run, rep, nil
}

// mapRowsForRun reads the rows this run created, in a stable order (the
// checkpoint's resume contract depends on it), skipping the prefix a prior
// attempt already processed.
func (s *RunStore) mapRowsForRun(ctx context.Context, id RunID, skip int) ([]mapRow, error) {
	var rows []mapRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT object, external_id, native_id
			  FROM import_record_map
			 WHERE import_run_id = $1
			   AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			 ORDER BY created_at, external_id
			 OFFSET $2`, id, skip)
		if err != nil {
			return fmt.Errorf("reading import run %s's created rows: %w", id, err)
		}
		defer r.Close()
		for r.Next() {
			var mr mapRow
			if err := r.Scan(&mr.object, &mr.externalID, &mr.nativeID); err != nil {
				return fmt.Errorf("reading import run %s's created rows: %w", id, err)
			}
			rows = append(rows, mr)
		}
		return r.Err()
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// humanEditedSince names which of the given rows a human has updated since
// the reference instant — one indexed audit_log scan per object class,
// mirroring the established pattern (compose/humanprecedence.go) at the
// coarser grain A93 actually asks for: "has anyone touched this row",
// not which field.
func (s *RunStore) humanEditedSince(ctx context.Context, rows []mapRow, since time.Time) (map[ids.UUID]bool, error) {
	edited := map[ids.UUID]bool{}
	if len(rows) == 0 {
		return edited, nil
	}
	byObject := map[string][]ids.UUID{}
	for _, r := range rows {
		byObject[r.object] = append(byObject[r.object], r.nativeID)
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		for object, nativeIDs := range byObject {
			r, err := tx.Query(ctx, `
				SELECT DISTINCT entity_id FROM audit_log
				 WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
				   AND entity_type = $1 AND entity_id = ANY($2) AND actor_type = 'human'
				   AND action = 'update' AND occurred_at > $3`,
				object, nativeIDs, since)
			if err != nil {
				return fmt.Errorf("checking which %s rows a human edited since import: %w", object, err)
			}
			scanErr := func() error {
				defer r.Close()
				for r.Next() {
					var id ids.UUID
					if err := r.Scan(&id); err != nil {
						return err
					}
					edited[id] = true
				}
				return r.Err()
			}()
			if scanErr != nil {
				return fmt.Errorf("checking which %s rows a human edited since import: %w", object, scanErr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return edited, nil
}

// advanceUndoCheckpoint moves the resume cursor forward and persists the
// report so far, the undo's own mirror of advanceCheckpoint.
func (s *RunStore) advanceUndoCheckpoint(ctx context.Context, id RunID, checkpoint int, rep UndoReport) error {
	encoded, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encoding import run %s undo progress: %w", id, err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET checkpoint = $2, undo_report = $3, updated_at = now()
			 WHERE id = $1 AND status = $4 AND checkpoint <= $2`, id, checkpoint, encoded, StatusUndoing)
		if err != nil {
			return fmt.Errorf("advancing import run %s undo checkpoint: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s cannot advance undo to %d (not undoing, or cursor moved past it): %w", id, checkpoint, apperrors.ErrConflict)
		}
		return nil
	})
}

// completeUndo records the finished reversal, audited.
func (s *RunStore) completeUndo(ctx context.Context, id RunID, rep UndoReport) error {
	encoded, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encoding import run %s undo report: %w", id, err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET status = $2, undo_report = $3, updated_at = now()
			 WHERE id = $1 AND status = $4`, id, StatusUndone, encoded, StatusUndoing)
		if err != nil {
			return fmt.Errorf("completing import run %s undo: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s is not undoing, cannot complete undo: %w", id, apperrors.ErrConflict)
		}
		_, err = storekit.Audit(ctx, tx, "import_undo", importRunObject, id,
			map[string]any{auditFieldStatus: StatusUndoing},
			map[string]any{auditFieldStatus: StatusUndone, "reversed_count": rep.ReversedCount, "kept_count": len(rep.Kept)})
		return err
	})
}
