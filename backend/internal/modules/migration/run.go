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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// importRunObject is the RBAC object every store entry point gates on —
// admin/ops-only on every verb (identity/internal/policy).
const importRunObject = "import_run"

// auditFieldStatus names the run status in an import_run audit row.
const auditFieldStatus = "status"

// RunID names an import_run row.
type RunID = ids.UUID

// Run statuses (IEM-DDL-1's lifecycle; the engine drives
// running→complete|failed, the direct importer's dry-run/approve states
// arrive with its own ticket).
const (
	StatusRunning  = "running"
	StatusComplete = "complete"
	StatusFailed   = "failed"
)

// Run is one import_run row.
type Run struct {
	ID         RunID
	Connector  string
	Status     string
	SourceRef  string
	Checkpoint int
	Report     *Report
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// RunStore owns the import_run table; every status transition rides the
// storekit audit shape.
type RunStore struct {
	pool *pgxpool.Pool
}

// NewRunStore wires the store over the RLS-bound app pool.
func NewRunStore(pool *pgxpool.Pool) *RunStore {
	return &RunStore{pool: pool}
}

func (s *RunStore) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

// CreateRunInput starts a run record. Source is the provenance stamp
// (DM-CONV-11) naming who/what initiated the run (e.g. "overlay:flip").
type CreateRunInput struct {
	Connector string
	SourceRef string
	Source    string
}

// Create inserts a run in the running state and audits it — the flip's
// confirm-first gate (typed phrase + sealed preflight) happens before
// this call; recording the run IS the audited start gate.
func (s *RunStore) Create(ctx context.Context, in CreateRunInput) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionCreate); err != nil {
		return Run{}, err
	}
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO import_run (workspace_id, connector, status, source_ref, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)
			RETURNING id, connector, status, source_ref, checkpoint, created_at, updated_at`,
			in.Connector, StatusRunning, in.SourceRef, in.Source, capturedBy)
		if err := scanRun(row, &run); err != nil {
			return fmt.Errorf("creating import run: %w", err)
		}
		_, err := storekit.Audit(ctx, tx, "create", importRunObject, run.ID, nil, map[string]any{
			"connector": run.Connector, auditFieldStatus: run.Status, "source_ref": run.SourceRef,
		})
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// Get reads one run; a foreign workspace's run answers not-found (RLS).
func (s *RunStore) Get(ctx context.Context, id RunID) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionRead); err != nil {
		return Run{}, err
	}
	var run Run
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, connector, status, source_ref, checkpoint, created_at, updated_at, report, error
			FROM import_run WHERE id = $1`, id)
		var report []byte
		var runErr *string
		if err := row.Scan(&run.ID, &run.Connector, &run.Status, &run.SourceRef, &run.Checkpoint,
			&run.CreatedAt, &run.UpdatedAt, &report, &runErr); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return fmt.Errorf("reading import run: %w", err)
		}
		if runErr != nil {
			run.Error = *runErr
		}
		if len(report) > 0 {
			var rep Report
			if err := json.Unmarshal(report, &rep); err != nil {
				return fmt.Errorf("decoding import run report: %w", err)
			}
			run.Report = &rep
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// Latest returns the newest run for a connector, or ErrNotFound.
func (s *RunStore) Latest(ctx context.Context, connector string) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionRead); err != nil {
		return Run{}, err
	}
	var run Run
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, connector, status, source_ref, checkpoint, created_at, updated_at
			FROM import_run WHERE connector = $1 ORDER BY created_at DESC LIMIT 1`, connector)
		if err := scanRun(row, &run); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return fmt.Errorf("reading latest %s import run: %w", connector, err)
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// FlipImportRunning answers, inside the caller's transaction, whether a
// mirror-connector run (the overlay→native flip's import) is mid-run.
// The overlay module's Disconnect consults it through an injected probe
// so tearing the mirror down cannot race a running import — a read, not
// a mutation, so it carries no RBAC gate of its own beyond the
// transaction the caller already authorized.
func FlipImportRunning(ctx context.Context, tx pgx.Tx) (bool, error) {
	var running bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM import_run WHERE connector = $1 AND status = $2)`,
		ConnectorMirror, StatusRunning,
	).Scan(&running); err != nil {
		return false, fmt.Errorf("migration: checking for a running flip import: %w", err)
	}
	return running, nil
}

// advanceCheckpoint moves the resume cursor forward — called after every
// upsert (IEM-FORM-1), so a killed run restarts from the last landed row,
// never from zero and never past it.
func (s *RunStore) advanceCheckpoint(ctx context.Context, id RunID, checkpoint int) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET checkpoint = $2, updated_at = now()
			WHERE id = $1 AND status = 'running' AND checkpoint <= $2`, id, checkpoint)
		if err != nil {
			return fmt.Errorf("advancing import run checkpoint: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s cannot advance to %d (not running, or cursor moved past it): %w", id, checkpoint, apperrors.ErrConflict)
		}
		return nil
	})
}

// complete records the finished run with its report, audited.
func (s *RunStore) complete(ctx context.Context, id RunID, rep Report) error {
	raw, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encoding import run report: %w", err)
	}
	return s.transition(ctx, id, StatusComplete, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE import_run SET report = $2 WHERE id = $1`, id, raw)
		return err
	})
}

// failRun records why the run stopped — a resumable state, not a dead
// end (the same run id re-enters Engine.Run after the cause clears).
func (s *RunStore) failRun(ctx context.Context, id RunID, cause error) error {
	return s.transition(ctx, id, StatusFailed, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE import_run SET error = $2 WHERE id = $1`, id, cause.Error())
		return err
	})
}

// Resume flips a failed run back to running so Engine.Run re-enters it
// from its checkpoint.
func (s *RunStore) Resume(ctx context.Context, id RunID) error {
	if err := auth.Require(ctx, importRunObject, principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET status = 'running', error = NULL, updated_at = now()
			WHERE id = $1 AND status = 'failed'`, id)
		if err != nil {
			return fmt.Errorf("resuming import run: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s is not failed, nothing to resume: %w", id, apperrors.ErrConflict)
		}
		_, err = storekit.Audit(ctx, tx, "update", importRunObject, id, map[string]any{auditFieldStatus: StatusFailed}, map[string]any{auditFieldStatus: StatusRunning})
		return err
	})
}

func (s *RunStore) transition(ctx context.Context, id RunID, to string, extra func(pgx.Tx) error) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run SET status = $2, updated_at = now()
			WHERE id = $1 AND status = 'running'`, id, to)
		if err != nil {
			return fmt.Errorf("transitioning import run to %s: %w", to, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("import run %s is not running, cannot become %s: %w", id, to, apperrors.ErrConflict)
		}
		if extra != nil {
			if err := extra(tx); err != nil {
				return fmt.Errorf("recording import run %s payload: %w", to, err)
			}
		}
		_, err = storekit.Audit(ctx, tx, "update", importRunObject, id, map[string]any{auditFieldStatus: StatusRunning}, map[string]any{auditFieldStatus: to})
		return err
	})
}

func scanRun(row pgx.Row, run *Run) error {
	return row.Scan(&run.ID, &run.Connector, &run.Status, &run.SourceRef, &run.Checkpoint, &run.CreatedAt, &run.UpdatedAt)
}
