// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Pipeline/stage configuration beyond create (B-EP02): bounded config
// mutations, each a first-class fact per events.md §5.3b — renames and
// probability changes ride stage.updated, reorders ride ONE
// pipeline.updated with the position delta (never N stage.updated).

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type UpdatePipelineInput struct {
	Name      *string
	IsDefault *bool
	Position  *int
	IfVersion *int64
}

func (s *Store) UpdatePipeline(ctx context.Context, id ids.UUID, in UpdatePipelineInput) (crmcontracts.Pipeline, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionUpdate); err != nil {
		return crmcontracts.Pipeline{}, err
	}
	var out crmcontracts.Pipeline
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var version int64
		err := tx.QueryRow(ctx, `SELECT version FROM pipeline WHERE id = $1 AND archived_at IS NULL`, id).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if in.IfVersion != nil && *in.IfVersion != version {
			return apperrors.ErrVersionSkew
		}
		// Exactly one default pipeline: promoting this one demotes the
		// incumbent in the same transaction.
		if in.IsDefault != nil && *in.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE pipeline SET is_default = false WHERE is_default AND id <> $1`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE pipeline SET
			  name = coalesce($2, name),
			  is_default = coalesce($3, is_default),
			  position = coalesce($4, position)
			WHERE id = $1`,
			id, in.Name, in.IsDefault, in.Position); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "pipeline", id, nil, map[string]any{
			"name": in.Name, "is_default": in.IsDefault, "position": in.Position,
		})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "pipeline.updated", "pipeline", id, map[string]any{
			"delta": map[string]any{"name": in.Name, "is_default": in.IsDefault, "position": in.Position},
		}); err != nil {
			return err
		}
		out, err = readPipeline(ctx, tx, id)
		return err
	})
	return out, err
}

type CreateStageInput struct {
	PipelineID     ids.UUID
	Name           string
	Position       int
	Semantic       string
	WinProbability *int
}

func (s *Store) CreateStage(ctx context.Context, in CreateStageInput) (crmcontracts.Stage, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionUpdate); err != nil {
		return crmcontracts.Stage{}, err
	}
	if in.Semantic == "" {
		in.Semantic = "open"
	}
	// The terminal-probability rule (won=100, lost=0) is a DDL CHECK;
	// filling the canonical value here turns an omitted probability into
	// the right one instead of a 500.
	probability := 0
	if in.WinProbability != nil {
		probability = *in.WinProbability
	} else if in.Semantic == "won" {
		probability = 100
	}
	var out crmcontracts.Stage
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pipeline WHERE id = $1 AND archived_at IS NULL)`,
			in.PipelineID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperrors.ErrNotFound
		}
		var stageID ids.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO stage (workspace_id, pipeline_id, name, position, semantic, win_probability)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)
			RETURNING id`,
			in.PipelineID, in.Name, in.Position, in.Semantic, probability).Scan(&stageID)
		if err != nil {
			if storekit.IsUniqueViolation(err) {
				return apperrors.ErrConflict
			}
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "stage", stageID, nil, map[string]any{
			"pipeline_id": in.PipelineID, "name": in.Name, "semantic": in.Semantic,
		})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "stage.created", "stage", stageID, map[string]any{
			"pipeline_id": in.PipelineID, "name": in.Name, "position": in.Position,
			"semantic": in.Semantic, "win_probability": probability,
		}); err != nil {
			return err
		}
		out, err = readStage(ctx, tx, stageID, storekit.LiveOnly)
		return err
	})
	return out, err
}

func (s *Store) GetStage(ctx context.Context, id ids.UUID) (crmcontracts.Stage, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionRead); err != nil {
		return crmcontracts.Stage{}, err
	}
	var out crmcontracts.Stage
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readStage(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

func (s *Store) ListStages(ctx context.Context, pipelineID *ids.UUID, archived storekit.ArchivedFilter) ([]crmcontracts.Stage, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.Stage
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := "true"
		if pipelineID != nil {
			where = storekit.SQLf("pipeline_id = $%d", arg(*pipelineID))
		}
		if archived == storekit.LiveOnly {
			where += " AND archived_at IS NULL"
		}
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT id FROM stage WHERE %s ORDER BY pipeline_id, position`, where), args...)
		if err != nil {
			return err
		}
		var stageIDs []ids.UUID
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			stageIDs = append(stageIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range stageIDs {
			stage, err := readStage(ctx, tx, id, storekit.IncludeArchived)
			if err != nil {
				return err
			}
			out = append(out, stage)
		}
		return nil
	})
	return out, err
}

type UpdateStageInput struct {
	Name           *string
	Position       *int
	Semantic       *string
	WinProbability *int
	IfVersion      *int64
}

func (s *Store) UpdateStage(ctx context.Context, id ids.UUID, in UpdateStageInput) (crmcontracts.Stage, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionUpdate); err != nil {
		return crmcontracts.Stage{}, err
	}
	var out crmcontracts.Stage
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var version int64
		var pipelineID ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT version, pipeline_id FROM stage WHERE id = $1 AND archived_at IS NULL`, id).
			Scan(&version, &pipelineID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if in.IfVersion != nil && *in.IfVersion != version {
			return apperrors.ErrVersionSkew
		}
		if _, err := tx.Exec(ctx, `
			UPDATE stage SET
			  name = coalesce($2, name),
			  position = coalesce($3, position),
			  semantic = coalesce($4, semantic),
			  win_probability = CASE
			    WHEN $4 = 'won' THEN 100
			    WHEN $4 = 'lost' THEN 0
			    ELSE coalesce($5, win_probability) END
			WHERE id = $1`,
			id, in.Name, in.Position, in.Semantic, in.WinProbability); err != nil {
			if storekit.IsUniqueViolation(err) {
				return apperrors.ErrConflict
			}
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "stage", id, nil, map[string]any{
			"name": in.Name, "position": in.Position, "semantic": in.Semantic, "win_probability": in.WinProbability,
		})
		if err != nil {
			return err
		}
		// Reorders are pipeline-level facts (ONE pipeline.updated with
		// the position delta); everything else is a stage.updated.
		if in.Position != nil {
			err = storekit.Emit(ctx, tx, auditID, "pipeline.updated", "pipeline", pipelineID, map[string]any{
				"delta": map[string]any{"stage_positions": map[string]any{id.String(): *in.Position}},
			})
		} else {
			err = storekit.Emit(ctx, tx, auditID, "stage.updated", "stage", id, map[string]any{
				"pipeline_id": pipelineID,
				"delta":       map[string]any{"name": in.Name, "semantic": in.Semantic, "win_probability": in.WinProbability},
			})
		}
		if err != nil {
			return err
		}
		out, err = readStage(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

func readStage(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Stage, error) {
	q := `SELECT id, workspace_id, pipeline_id, name, position, semantic, win_probability, created_at, updated_at, archived_at
	      FROM stage WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	var out crmcontracts.Stage
	var stageID, wsID, pipelineID ids.UUID
	err := tx.QueryRow(ctx, q, id).Scan(&stageID, &wsID, &pipelineID, &out.Name, &out.Position,
		&out.Semantic, &out.WinProbability, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Stage{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Stage{}, err
	}
	out.Id = openapi_types.UUID(stageID)
	out.WorkspaceId = openapi_types.UUID(wsID)
	out.PipelineId = openapi_types.UUID(pipelineID)
	return out, nil
}
