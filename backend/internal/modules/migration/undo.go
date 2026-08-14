// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The reversal's own states (IEM-WIRE-9), reachable only from `complete`
// and only for the csv connector.
const (
	StatusUndoing = "undoing"
	StatusUndone  = "undone"
)

// undoPageSize bounds one page of import_record_map rows read and reversed
// per pass — the same reason the forward engine pages at pageSize: an undo
// over thousands of rows must not hold the whole set in memory or rewrite a
// growing report on every single row.
const undoPageSize = 200

// UndoWriters is the reversal seam (IEM-WIRE-9): compose implements it over
// the object stores a csv import can create, the same module-never-imports-
// a-sibling shape Writers uses. Reverse must be idempotent on the native
// id — a resumed undo may replay a row a crash left reversed but
// unrecorded.
type UndoWriters interface {
	Reverse(ctx context.Context, object string, nativeID ids.UUID) error
}

// KeptRow is one import-created row a human touched since import, therefore
// left in place rather than reversed (A93).
type KeptRow struct {
	Object string   `json:"object"`
	ID     ids.UUID `json:"id"`
}

// ErroredRow is one import-created row the reversal could not archive — a
// business rule refused it, or it is no longer visible to the caller.
// Recorded and left in place; it never stops the rest of the run.
type ErroredRow struct {
	Object string   `json:"object"`
	ID     ids.UUID `json:"id"`
	Reason string   `json:"reason"`
}

// UndoReport is the reversal outcome (IEM-WIRE-9). Every row this run
// created lands in exactly one bucket.
type UndoReport struct {
	ReversedCount int          `json:"reversed_count"`
	Kept          []KeptRow    `json:"kept,omitempty"`
	Errored       []ErroredRow `json:"errored,omitempty"`
}

// mapRow is one import_record_map row this run created.
type mapRow struct {
	object   string
	nativeID ids.UUID
}

// Undo reverses a completed CSV import run: every row it created that
// nobody has touched since is archived; a row a human touched after import
// is left exactly as they left it and named in the report's `kept` list
// (A93); a row that cannot be reversed for any other reason is named in
// `errored` — never a reason to abort the rows after it. Never an
// all-or-nothing hard rollback.
//
// Refuses a second call while one is already under way for this run
// (claimUndo), and pages the reversal (undoPageSize) so neither the row set
// nor the persisted report grows unbounded in memory or on the wire.
func (s *RunStore) Undo(ctx context.Context, id RunID, w UndoWriters) (UndoReport, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionUpdate); err != nil {
		return UndoReport{}, err
	}
	release, err := s.claimUndo(ctx, id)
	if err != nil {
		return UndoReport{}, err
	}
	defer release()

	run, rep, err := s.beginUndo(ctx, id)
	if err != nil {
		return UndoReport{}, err
	}

	processed := run.Checkpoint
	for {
		rows, err := s.mapRowsForRun(ctx, id, processed, undoPageSize)
		if err != nil {
			return UndoReport{}, err
		}
		if len(rows) == 0 {
			break
		}
		touched, err := s.humanTouchedSince(ctx, id, rows)
		if err != nil {
			return UndoReport{}, err
		}
		for _, r := range rows {
			switch {
			case touched[r.nativeID]:
				rep.Kept = append(rep.Kept, KeptRow{Object: r.object, ID: r.nativeID})
			default:
				if err := w.Reverse(ctx, r.object, r.nativeID); err != nil {
					rep.Errored = append(rep.Errored, ErroredRow{Object: r.object, ID: r.nativeID, Reason: reversalRefusalReason(err)})
				} else {
					rep.ReversedCount++
				}
			}
			processed++
		}
		if err := s.advanceUndoCheckpoint(ctx, id, processed, rep); err != nil {
			return UndoReport{}, err
		}
		if len(rows) < undoPageSize {
			break
		}
	}

	if err := s.completeUndo(ctx, id, rep); err != nil {
		return UndoReport{}, err
	}
	return rep, nil
}

// reversalRefusalReason turns a Reverse error into the operator-facing
// sentence errored rows carry — never a database or driver message, the
// same discipline the forward commit's SkippedRow reasons already keep.
func reversalRefusalReason(err error) string {
	switch {
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return "the caller's grant no longer covers this record"
	case errors.Is(err, apperrors.ErrNotFound):
		return "the record is no longer visible to this caller"
	case errors.Is(err, apperrors.ErrConflict):
		return "the record refused the reversal (a business rule protects it)"
	default:
		return "the record could not be reversed"
	}
}

// claimUndo takes an exclusive, session-scoped advisory lock on this run's
// undo, held for the lifetime of the caller's Undo call. beginUndo's row
// lock only covers its own transaction; the reversal that follows spans
// many transactions, so without a lock held across the whole call, two
// concurrent `POST /imports/{id}/undo` requests on the SAME run would both
// pass beginUndo's `complete`/`undoing` check and both loop over the same
// rows. A second caller is refused immediately rather than piling a
// duplicate pass onto the first.
func (s *RunStore) claimUndo(ctx context.Context, id RunID) (func(), error) {
	conn, err := s.db.Pool().Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("import run %s: acquiring the undo lock connection: %w", id, err)
	}
	var acquired bool
	// Two-argument form, classid fixed to this feature's own namespace: the
	// lock space is import_run undo alone, never shared with another
	// advisory-lock user in the codebase (the flip's own liveness probe
	// keys the single-argument form differently).
	const undoLockClass = 723 // this feature's own issue number, as good a fixed namespace as any
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext($2::text))`,
		undoLockClass, id.String()).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("import run %s: acquiring the undo lock: %w", id, err)
	}
	if !acquired {
		conn.Release()
		return nil, fmt.Errorf("import run %s: an undo is already under way for this run: %w", id, apperrors.ErrConflict)
	}
	//nolint:contextcheck // deliberately context.Background() below, not the caller's ctx: the caller's
	// request may already be cancelled by the time this runs, and the unlock must still happen so a
	// later caller is not wedged behind a lock nobody will ever release.
	return func() {
		// Best-effort: the lock also dies with the connection when it is
		// released back to the pool, which is the backstop if this
		// explicit unlock ever fails.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1, hashtext($2::text))`, undoLockClass, id.String())
		conn.Release()
	}, nil
}

// beginUndo validates the run and starts (or resumes) its reversal,
// returning the run as it stood and the report to carry forward: fresh
// when starting, or a resumed attempt's own progress when continuing one
// `undoing` already.
func (s *RunStore) beginUndo(ctx context.Context, id RunID) (Run, UndoReport, error) {
	var run Run
	var rep UndoReport
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var undoReportRaw []byte
		// FOR UPDATE: two concurrent calls reaching beginUndo back to back
		// (claimUndo already refuses a genuinely concurrent second caller,
		// but a caller retrying right after the first releases its lock
		// must still see a consistent read-then-transition) do not both
		// read `complete` and both start a fresh reversal.
		row := tx.QueryRow(ctx, `
			SELECT id, connector, status, checkpoint, undo_report
			  FROM import_run
			 WHERE id = $1 AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			 FOR UPDATE`, id)
		if err := row.Scan(&run.ID, &run.Connector, &run.Status, &run.Checkpoint, &undoReportRaw); err != nil {
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
			rep = UndoReport{}
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
			 WHERE id = $1 AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
			id, StatusUndoing, run.Checkpoint, encoded)
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

// mapRowsForRun reads one page of the rows this run created, in a stable
// order (the checkpoint's resume contract depends on it), skipping the
// prefix a prior pass already processed.
func (s *RunStore) mapRowsForRun(ctx context.Context, id RunID, skip, limit int) ([]mapRow, error) {
	var rows []mapRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT object, native_id
			  FROM import_record_map
			 WHERE import_run_id = $1
			   AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			 ORDER BY created_at, external_id
			 OFFSET $2 LIMIT $3`, id, skip, limit)
		if err != nil {
			return fmt.Errorf("reading import run %s's created rows: %w", id, err)
		}
		defer r.Close()
		for r.Next() {
			var mr mapRow
			if err := r.Scan(&mr.object, &mr.nativeID); err != nil {
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

// humanTouchedSince names which of the given rows a human has touched since
// THAT ROW was created — not since the run finished (a run resumed or
// re-entered long after its rows landed would otherwise miss an edit made
// while it was still in flight). Joined directly against
// import_record_map.created_at in SQL rather than a single run-level
// instant passed from Go, one indexed scan per object class.
//
// "Touched" is any human-actor audit row on the entity, not narrowly
// action='update': a human who independently archived or otherwise acted
// on an imported row is exactly the case A93's protection exists for, and
// a narrower filter would silently reverse their action too.
func (s *RunStore) humanTouchedSince(ctx context.Context, id RunID, rows []mapRow) (map[ids.UUID]bool, error) {
	touched := map[ids.UUID]bool{}
	if len(rows) == 0 {
		return touched, nil
	}
	byObject := map[string][]ids.UUID{}
	for _, r := range rows {
		byObject[r.object] = append(byObject[r.object], r.nativeID)
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		for object, nativeIDs := range byObject {
			r, err := tx.Query(ctx, `
				SELECT m.native_id
				  FROM import_record_map m
				 WHERE m.import_run_id = $1 AND m.object = $2
				   AND m.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
				   AND m.native_id = ANY($3)
				   AND EXISTS (
				     SELECT 1 FROM audit_log a
				      WHERE a.workspace_id = m.workspace_id
				        AND a.entity_type = $2 AND a.entity_id = m.native_id
				        AND a.actor_type = 'human' AND a.occurred_at > m.created_at)`,
				id, object, nativeIDs)
			if err != nil {
				return fmt.Errorf("checking which %s rows a human touched since import: %w", object, err)
			}
			scanErr := func() error {
				defer r.Close()
				for r.Next() {
					var nid ids.UUID
					if err := r.Scan(&nid); err != nil {
						return err
					}
					touched[nid] = true
				}
				return r.Err()
			}()
			if scanErr != nil {
				return fmt.Errorf("checking which %s rows a human touched since import: %w", object, scanErr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return touched, nil
}

// advanceUndoCheckpoint moves the resume cursor forward one page and
// persists the report so far, the undo's own mirror of advanceCheckpoint —
// once per page rather than once per row, so a run with a long kept/errored
// list does not rewrite an ever-growing jsonb blob on every single row.
func (s *RunStore) advanceUndoCheckpoint(ctx context.Context, id RunID, checkpoint int, rep UndoReport) error {
	encoded, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encoding import run %s undo progress: %w", id, err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET checkpoint = $2, undo_report = $3, updated_at = now()
			 WHERE id = $1 AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			   AND status = $4 AND checkpoint <= $2`, id, checkpoint, encoded, StatusUndoing)
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
			 WHERE id = $1 AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			   AND status = $4`, id, StatusUndone, encoded, StatusUndoing)
		if err != nil {
			return fmt.Errorf("completing import run %s undo: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s is not undoing, cannot complete undo: %w", id, apperrors.ErrConflict)
		}
		_, err = storekit.Audit(ctx, tx, "import_undo", importRunObject, id,
			map[string]any{auditFieldStatus: StatusUndoing},
			map[string]any{auditFieldStatus: StatusUndone, "reversed_count": rep.ReversedCount, "kept_count": len(rep.Kept), "errored_count": len(rep.Errored)})
		return err
	})
}
