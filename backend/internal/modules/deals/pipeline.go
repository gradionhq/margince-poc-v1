// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type StageInput struct {
	Name           string
	Position       int
	Semantic       string
	WinProbability int
}

type CreatePipelineInput struct {
	Name      string
	IsDefault bool
	Position  int
	Stages    []StageInput
}

// CreatePipeline creates the pipeline with its initial stages. The
// stage_terminal_prob CHECK and uq_pipeline_default index enforce the
// won=100/lost=0 and one-default rules at the database.
func (s *Store) CreatePipeline(ctx context.Context, in CreatePipelineInput) (crmcontracts.Pipeline, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionCreate); err != nil {
		return crmcontracts.Pipeline{}, err
	}
	var out crmcontracts.Pipeline
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = createPipelineTx(ctx, tx, in)
		return err
	})
	return out, err
}

// createPipelineTx is the pipeline+stages write, factored out so it can run
// either in its own transaction (CreatePipeline) or inside a caller's — the
// atomic bootstrap seeds defaults in the same transaction that mints the
// workspace (C5), so a seed failure rolls the whole tenant back.
func createPipelineTx(ctx context.Context, tx pgx.Tx, in CreatePipelineInput) (crmcontracts.Pipeline, error) {
	wsID := storekit.MustWorkspace(ctx)
	id := ids.New[ids.PipelineKind]()
	_, err := tx.Exec(ctx,
		`INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, $3, $4, $5)`,
		id, wsID, in.Name, in.IsDefault, in.Position)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.Pipeline{}, apperrors.ErrConflict
		}
		return crmcontracts.Pipeline{}, fmt.Errorf("insert pipeline: %w", err)
	}

	for _, st := range in.Stages {
		if _, err := tx.Exec(ctx,
			`INSERT INTO stage (workspace_id, pipeline_id, name, position, semantic, win_probability)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			wsID, id, st.Name, st.Position, st.Semantic, st.WinProbability); err != nil {
			return crmcontracts.Pipeline{}, fmt.Errorf("insert stage: %w", err)
		}
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "pipeline", id.UUID, nil, map[string]any{"name": in.Name})
	if err != nil {
		return crmcontracts.Pipeline{}, fmt.Errorf("audit pipeline create: %w", err)
	}
	// events.md §5.3b: config changes are first-class facts — one
	// pipeline.created carries the whole stage set.
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, pipelineCreatedPayload(in.Name, in.IsDefault, in.Stages)); err != nil {
		return crmcontracts.Pipeline{}, fmt.Errorf("emit pipeline.created: %w", err)
	}
	out, err := readPipeline(ctx, tx, id)
	if err != nil {
		return crmcontracts.Pipeline{}, fmt.Errorf("read created pipeline: %w", err)
	}
	return out, nil
}

// pipelineCreatedPayload builds the pipeline.created wire payload from
// createPipelineTx's inputs — the ONE place that maps CreatePipeline's
// local values onto the published schema, so a future field rename shows
// up here rather than at an independently-drifting map literal.
func pipelineCreatedPayload(name string, isDefault bool, stages []StageInput) crmcontracts.PublicEventPipelineCreated {
	out := make([]crmcontracts.PublicEventPipelineCreatedStage, 0, len(stages))
	for _, st := range stages {
		out = append(out, crmcontracts.PublicEventPipelineCreatedStage{Name: st.Name, Position: st.Position, Semantic: st.Semantic})
	}
	return crmcontracts.PublicEventPipelineCreated{Name: name, IsDefault: isDefault, Stages: out}
}

func (s *Store) GetPipeline(ctx context.Context, id ids.PipelineID) (crmcontracts.Pipeline, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionRead); err != nil {
		return crmcontracts.Pipeline{}, err
	}
	var out crmcontracts.Pipeline
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readPipeline(ctx, tx, id)
		return err
	})
	return out, err
}

func (s *Store) ListPipelines(ctx context.Context) ([]crmcontracts.Pipeline, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.Pipeline
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id FROM pipeline WHERE archived_at IS NULL ORDER BY position, created_at`)
		if err != nil {
			return err
		}
		var pipelineIDs []ids.PipelineID
		for rows.Next() {
			var id ids.PipelineID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			pipelineIDs = append(pipelineIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, id := range pipelineIDs {
			p, err := readPipeline(ctx, tx, id)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	if out == nil {
		out = []crmcontracts.Pipeline{}
	}
	return out, err
}

// DefaultPipeline returns the workspace's seeded default.
func (s *Store) DefaultPipeline(ctx context.Context) (crmcontracts.Pipeline, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionRead); err != nil {
		return crmcontracts.Pipeline{}, err
	}
	var out crmcontracts.Pipeline
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var id ids.PipelineID
		err := tx.QueryRow(ctx,
			`SELECT id FROM pipeline WHERE is_default AND archived_at IS NULL`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = readPipeline(ctx, tx, id)
		return err
	})
	return out, err
}

func readPipeline(ctx context.Context, tx pgx.Tx, id ids.PipelineID) (crmcontracts.Pipeline, error) {
	var p crmcontracts.Pipeline
	var pid, wsID ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT id, workspace_id, name, is_default, position, created_at, updated_at, archived_at
		 FROM pipeline WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&pid, &wsID, &p.Name, &p.IsDefault, &p.Position, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, apperrors.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Id = openapi_types.UUID(pid)
	p.WorkspaceId = openapi_types.UUID(wsID)

	rows, err := tx.Query(ctx,
		`SELECT id, workspace_id, pipeline_id, name, position, semantic, win_probability, created_at, updated_at
		 FROM stage WHERE pipeline_id = $1 AND archived_at IS NULL ORDER BY position`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()

	stages := []crmcontracts.Stage{}
	for rows.Next() {
		var st crmcontracts.Stage
		var stID, stWs, stPipeline ids.UUID
		var semantic string
		if err := rows.Scan(&stID, &stWs, &stPipeline, &st.Name, &st.Position, &semantic, &st.WinProbability, &st.CreatedAt, &st.UpdatedAt); err != nil {
			return p, err
		}
		st.Id = openapi_types.UUID(stID)
		st.WorkspaceId = openapi_types.UUID(stWs)
		st.PipelineId = openapi_types.UUID(stPipeline)
		st.Semantic = crmcontracts.StageSemantic(semantic)
		stages = append(stages, st)
	}
	p.Stages = &stages
	return p, rows.Err()
}

// defaultStages is the seeded pipeline shape a fresh workspace gets.
var defaultStages = []StageInput{
	{Name: "Qualified", Position: 1, Semantic: "open", WinProbability: 10},
	{Name: "Discovery", Position: 2, Semantic: "open", WinProbability: 25},
	{Name: "Proposal", Position: 3, Semantic: "open", WinProbability: 50},
	{Name: "Negotiation", Position: 4, Semantic: "open", WinProbability: 75},
	{Name: "Won", Position: 5, Semantic: "won", WinProbability: 100},
	{Name: "Lost", Position: 6, Semantic: "lost", WinProbability: 0},
}

// SeedDefaults provisions a fresh workspace's default pipeline. Called by
// the bootstrap composition at the edge — deals owns pipeline data,
// identity owns users and sessions; neither reaches into the other.
func (s *Store) SeedDefaults(ctx context.Context) error {
	_, err := s.CreatePipeline(ctx, CreatePipelineInput{
		Name: "Sales", IsDefault: true, Position: 0, Stages: defaultStages,
	})
	return err
}

// SeedDefaultsTx provisions the default pipeline inside a transaction the
// caller owns — the bootstrap path (C5) runs it in the same transaction as
// the workspace/admin/role inserts, so the tenant and its defaults are all
// committed or all rolled back. The caller must have bound the workspace
// GUC and a system actor on ctx.
func (s *Store) SeedDefaultsTx(ctx context.Context, tx pgx.Tx) error {
	_, err := createPipelineTx(ctx, tx, CreatePipelineInput{
		Name: "Sales", IsDefault: true, Position: 0, Stages: defaultStages,
	})
	return err
}

// StageSeed is one configured open stage of the bootstrap pipeline
// (A107/ADR-0061: the deployment file may shape the default pipeline).
type StageSeed struct {
	Name           string
	WinProbability int
}

// SeedPipelineTx provisions a configured default pipeline in the caller's
// bootstrap transaction. The configuration names only the OPEN stages —
// the Won/Lost terminal pair is appended here because stage semantics are
// a module invariant (the won/lost tier resolution and FX freeze hang off
// them), never an operator choice. Validated with the same rules as an
// authenticated pipeline change: createPipelineTx rejects what a runtime
// create would reject.
func (s *Store) SeedPipelineTx(ctx context.Context, tx pgx.Tx, name string, open []StageSeed) error {
	stages := make([]StageInput, 0, len(open)+2)
	for i, st := range open {
		stages = append(stages, StageInput{
			Name: st.Name, Position: i + 1, Semantic: "open", WinProbability: st.WinProbability,
		})
	}
	stages = append(
		stages,
		StageInput{Name: "Won", Position: len(open) + 1, Semantic: "won", WinProbability: 100},
		StageInput{Name: "Lost", Position: len(open) + 2, Semantic: "lost", WinProbability: 0},
	)
	_, err := createPipelineTx(ctx, tx, CreatePipelineInput{
		Name: name, IsDefault: true, Position: 0, Stages: stages,
	})
	return err
}

// StageSemantic resolves one live stage's semantic + pipeline — the
// admission gate's input for the advance_deal tier resolver: won/lost is
// a property of the TARGET STAGE's configuration, never of the request
// arguments (a renamed "Won" column still resolves 🟡).
func (s *Store) StageSemantic(ctx context.Context, stageID ids.UUID) (semantic string, pipelineID ids.UUID, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT semantic, pipeline_id FROM stage WHERE id = $1 AND archived_at IS NULL`,
			stageID).Scan(&semantic, &pipelineID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	return semantic, pipelineID, err
}
