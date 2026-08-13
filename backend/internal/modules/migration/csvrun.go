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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The two lifecycle states the direct importer adds to the flip's
// running→complete|failed (IEM-DDL-1): a run is validated first and committed
// only after a human has read what the validation found. The flip has no such
// pair because its own preflight is the gate.
const (
	StatusValidating       = "validating"
	StatusAwaitingApproval = "awaiting_approval"
)

// RunMapping is what a staged run knows about its source beyond the file
// itself: which object the rows are, which source column lands in which field,
// and which column identifies a row.
//
// It lives in import_run.mapping rather than a table of its own — one run's
// mapping is one fact about that run, and the ratification (IEM-GAP-2) chose
// the column over IEM-DDL-4's table, whose composite tenant key ADR-0091 had
// already retired.
type RunMapping struct {
	Object    string            `json:"object"`
	Fields    map[string]string `json:"fields"`
	SourceKey string            `json:"source_key"`
}

// CreateStagedRunInput opens a run that must be approved before it writes.
type CreateStagedRunInput struct {
	Connector string
	SourceRef string
	Source    string
	Mapping   RunMapping
}

// CreateStagedRun opens a run in `validating`: the row exists, carries its
// mapping, and has written nothing to the estate. The dry run reports against
// it and AwaitApproval hands it to a human.
func (s *RunStore) CreateStagedRun(ctx context.Context, in CreateStagedRunInput) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionCreate); err != nil {
		return Run{}, err
	}
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return Run{}, err
	}
	mapping, err := json.Marshal(in.Mapping)
	if err != nil {
		return Run{}, fmt.Errorf("encoding the import mapping: %w", err)
	}

	var run Run
	err = s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO import_run (workspace_id, connector, status, mapping, source_ref, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
			RETURNING id, connector, status, source_ref, checkpoint, created_at, updated_at`,
			in.Connector, StatusValidating, mapping, in.SourceRef, in.Source, capturedBy)
		if err := scanRun(row, &run); err != nil {
			return fmt.Errorf("creating import run: %w", err)
		}
		_, err := storekit.Audit(ctx, tx, "create", importRunObject, run.ID, nil, map[string]any{
			"connector": run.Connector, auditFieldStatus: run.Status, "source_ref": run.SourceRef,
			"object": in.Mapping.Object,
		})
		return err
	})
	if err != nil {
		return Run{}, err
	}
	run.Mapping = &in.Mapping
	return run, nil
}

// AwaitApproval records what the dry run found and parks the run for a human.
// Valid only from `validating`: a run that already moved on is not the one the
// report describes.
func (s *RunStore) AwaitApproval(ctx context.Context, id RunID, report Report) error {
	if err := auth.Require(ctx, importRunObject, principal.ActionUpdate); err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encoding the dry-run report: %w", err)
	}
	return s.stageTransition(ctx, id, StatusValidating, StatusAwaitingApproval, encoded)
}

// Approve moves a validated run to `running`.
//
// Valid ONLY from `awaiting_approval`, and the refusal is the point: approving
// a run that is already running, complete or failed means the approver is
// acting on a state that is not the one they judged. The conflict says so
// rather than starting a second pass over the same file.
func (s *RunStore) Approve(ctx context.Context, id RunID) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionUpdate); err != nil {
		return Run{}, err
	}
	if err := s.stageTransition(ctx, id, StatusAwaitingApproval, StatusRunning, nil); err != nil {
		return Run{}, err
	}
	return s.GetStaged(ctx, id)
}

// stageTransition moves a run between two named states, refusing when it is not
// in the state the caller believed. One statement decides both the move and the
// refusal, so a concurrent approval cannot slip between a check and a write.
func (s *RunStore) stageTransition(ctx context.Context, id RunID, from, to string, report []byte) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE import_run
			   SET status = $3,
			       report = COALESCE($4::jsonb, report),
			       updated_at = now()
			 WHERE id = $1 AND status = $2`, id, from, to, report)
		if err != nil {
			return fmt.Errorf("moving import run %s to %s: %w", id, to, err)
		}
		if tag.RowsAffected() == 0 {
			// Existence-hiding is the store's own job elsewhere; here the row
			// was already proven visible by the read that preceded this call,
			// so the honest answer is the state conflict.
			return fmt.Errorf("import run %s is not %s, so it cannot become %s: %w", id, from, to, apperrors.ErrConflict)
		}
		_, err = storekit.Audit(ctx, tx, "update", importRunObject, id,
			map[string]any{auditFieldStatus: from}, map[string]any{auditFieldStatus: to})
		return err
	})
}

// GetStaged reads one run with its mapping and report — everything the status
// and report surfaces answer from. A run outside the caller's scope answers
// not-found, never forbidden.
func (s *RunStore) GetStaged(ctx context.Context, id RunID) (Run, error) {
	if err := auth.Require(ctx, importRunObject, principal.ActionRead); err != nil {
		return Run{}, err
	}
	var (
		run     Run
		mapping []byte
		report  []byte
		runErr  *string
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, connector, status, source_ref, checkpoint, created_at, updated_at, mapping, report, error
			  FROM import_run WHERE id = $1`, id)
		return row.Scan(&run.ID, &run.Connector, &run.Status, &run.SourceRef, &run.Checkpoint,
			&run.CreatedAt, &run.UpdatedAt, &mapping, &report, &runErr)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, fmt.Errorf("import run %s: %w", id, apperrors.ErrNotFound)
		}
		return Run{}, fmt.Errorf("reading import run %s: %w", id, err)
	}
	if runErr != nil {
		run.Error = *runErr
	}
	if len(mapping) > 0 {
		var m RunMapping
		if err := json.Unmarshal(mapping, &m); err != nil {
			return Run{}, fmt.Errorf("reading the mapping of import run %s: %w", id, err)
		}
		// An empty object is what the flip's own runs carry; only a mapping
		// that actually names one is handed on, so a caller cannot mistake
		// "no mapping" for "a mapping of nothing".
		if m.Object != "" {
			run.Mapping = &m
		}
	}
	if len(report) > 0 {
		var rep Report
		if err := json.Unmarshal(report, &rep); err != nil {
			return Run{}, fmt.Errorf("reading the report of import run %s: %w", id, err)
		}
		run.Report = &rep
	}
	return run, nil
}
