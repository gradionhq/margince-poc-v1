// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The removal half of the bounded stage-configuration surface
// (DEAL-WIRE-7 / UC-ADMIN-04 step 6). Archive, never delete: a
// deal_stage_history row references the stage a deal moved out of with
// ON DELETE RESTRICT, so a removed stage has to stay on disk for the
// history to stay readable.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// namedBlockingDeals caps how many occupying deals the refusal spells
// out. The count is always exact; the list is the actionable part, and a
// stage holding hundreds of deals is answered by "move them", not by a
// wire body the admin has to scroll.
const namedBlockingDeals = 10

// BlockingDeal is one live deal standing in the way of a stage removal.
type BlockingDeal struct {
	ID   ids.DealID
	Name string
}

// StageOccupiedError refuses a removal that would strand deals
// (UC-ADMIN-04 F1b). It names them because "move the deals first" is
// only actionable if the admin is told which ones.
//
// A MessageFault, not a FieldFault: the caller sent a well-formed
// removal of the stage they meant, and nothing in the request is theirs
// to correct — what refuses is the workspace's own state. Naming a field
// would hand the caller an input to fix that the operation does not have.
type StageOccupiedError struct {
	Count int
	Deals []BlockingDeal
}

func (e *StageOccupiedError) Error() string {
	return fmt.Sprintf("stage holds %d live deal(s)", e.Count)
}

// MessageFault carries the refusal's verdict wherever the error travels —
// the REST mapper and the datasource seam alike read it, so neither has
// to keep its own copy of this sentence.
func (e *StageOccupiedError) MessageFault() (code, message string) {
	names := make([]string, 0, len(e.Deals))
	for _, d := range e.Deals {
		names = append(names, d.Name)
	}
	message = fmt.Sprintf("%d deal(s) still sit on this stage: %s", e.Count, strings.Join(names, ", "))
	// The named list is capped, so a refusal over a busy stage must not
	// read as the whole truth.
	if e.Count > len(e.Deals) {
		message += fmt.Sprintf(" (and %d more)", e.Count-len(e.Deals))
	}
	return "stage_occupied", message + ". Move them to another stage first."
}

// TerminalStageError refuses removal of a won/lost stage: add and remove
// operate on non-terminal stages only (UC-ADMIN-04 step 7), because the
// close semantics and the FX freeze resolve through that pair. A
// MessageFault for the same reason as StageOccupiedError.
type TerminalStageError struct {
	Semantic string
}

func (e *TerminalStageError) Error() string {
	return "a " + e.Semantic + " stage cannot be removed"
}

// MessageFault carries this refusal's verdict for the same reason
// StageOccupiedError's does.
func (e *TerminalStageError) MessageFault() (code, message string) {
	return "terminal_stage_not_removable",
		"the " + e.Semantic + " stage is what closes a deal in this pipeline and cannot be removed"
}

// ArchiveStage removes a stage from its pipeline. The surviving stages
// shift down so position stays contiguous, which is a pipeline-level
// fact and rides ONE pipeline.updated — the same rule UpdateStage's
// reorder branch follows.
func (s *Store) ArchiveStage(ctx context.Context, id ids.StageID, ifVersion *int64) error {
	if err := auth.Require(ctx, "pipeline", principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The lock makes the version read, the occupancy check and the
		// archive one race-free unit: a deal advancing onto this stage
		// concurrently either lands before the lock (and is then seen by
		// the occupancy check) or waits for the archive to commit and
		// fails its own live-stage lookup.
		if _, err := storekit.LockRow(ctx, tx, "stage", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		st, err := lockedStageForRemoval(ctx, tx, id, ifVersion)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stage SET archived_at = $2 WHERE id = $1`, id, time.Now().UTC()); err != nil {
			return fmt.Errorf("archive stage: %w", err)
		}
		moved, err := closeStageGap(ctx, tx, st.pipelineID, st.position)
		if err != nil {
			return err
		}
		return emitStageArchived(ctx, tx, id, st.pipelineID, moved)
	})
}

// removableStage is the locked stage's state the removal decides on.
type removableStage struct {
	pipelineID ids.PipelineID
	position   int
}

// lockedStageForRemoval reads the locked stage and answers every refusal
// that does not need the row to be touched: version skew, the terminal
// pair, and the deals still standing on it.
func lockedStageForRemoval(
	ctx context.Context, tx pgx.Tx, id ids.StageID, ifVersion *int64,
) (removableStage, error) {
	var st removableStage
	var version int64
	var semantic string
	err := tx.QueryRow(ctx,
		`SELECT version, pipeline_id, position, semantic FROM stage WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&version, &st.pipelineID, &st.position, &semantic)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, apperrors.ErrNotFound
	}
	if err != nil {
		return st, fmt.Errorf("read stage before archive: %w", err)
	}
	if ifVersion != nil && *ifVersion != version {
		return st, apperrors.ErrVersionSkew
	}
	if StageSemantic(semantic) != SemanticOpen {
		return st, &TerminalStageError{Semantic: semantic}
	}
	return st, refuseIfOccupied(ctx, tx, id)
}

// refuseIfOccupied answers the occupancy refusal, or nil when the stage
// holds nothing. Live deals only: an archived deal cannot be moved off
// the stage, so refusing on one would leave the admin with no way
// forward — and its FK keeps pointing at a row archiving leaves in place.
func refuseIfOccupied(ctx context.Context, tx pgx.Tx, id ids.StageID) error {
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM deal WHERE stage_id = $1 AND archived_at IS NULL`, id).Scan(&count); err != nil {
		return fmt.Errorf("count deals on stage: %w", err)
	}
	if count == 0 {
		return nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id, name FROM deal WHERE stage_id = $1 AND archived_at IS NULL
		 ORDER BY created_at LIMIT $2`, id, namedBlockingDeals)
	if err != nil {
		return fmt.Errorf("name deals on stage: %w", err)
	}
	defer rows.Close()
	named := make([]BlockingDeal, 0, count)
	for rows.Next() {
		var d BlockingDeal
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return fmt.Errorf("scan deal on stage: %w", err)
		}
		named = append(named, d)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read deals on stage: %w", err)
	}
	return &StageOccupiedError{Count: count, Deals: named}
}

// closeStageGap pulls the stages above the removed one down by one so
// positions stay 1..n, and reports the moves for the reorder event.
//
// Ascending, one row at a time, on purpose: uq_stage_position is a
// per-row check, and a single set-based `position - 1` would depend on
// PostgreSQL visiting the rows in an order that keeps every intermediate
// state unique — which it does not promise. Walking upward, each row's
// target was vacated by the archive or by its predecessor's move. A
// pipeline holds a handful of stages, so the loop is bounded by the
// bounded-config surface itself.
//
// The read takes FOR UPDATE because these rows are read and then written:
// without it, a concurrent removal or reorder in the same pipeline could
// move a stage between this SELECT and its UPDATE, and the position
// written here would be computed from a row state that no longer holds.
// Locking upward by position is also why two concurrent removals cannot
// deadlock — neither can hold a stage the other needs without already
// holding every stage below it.
func closeStageGap(
	ctx context.Context, tx pgx.Tx, pipelineID ids.PipelineID, removed int,
) (map[string]any, error) {
	rows, err := tx.Query(ctx,
		`SELECT id FROM stage
		 WHERE pipeline_id = $1 AND archived_at IS NULL AND position > $2
		 ORDER BY position FOR UPDATE`, pipelineID, removed)
	if err != nil {
		return nil, fmt.Errorf("read stages above the removed one: %w", err)
	}
	var above []ids.StageID
	for rows.Next() {
		var id ids.StageID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan stage above the removed one: %w", err)
		}
		above = append(above, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stages above the removed one: %w", err)
	}
	moved := make(map[string]any, len(above))
	for i, id := range above {
		position := removed + i
		if _, err := tx.Exec(ctx,
			`UPDATE stage SET position = $2 WHERE id = $1`, id, position); err != nil {
			return nil, fmt.Errorf("close the gap the removed stage left: %w", err)
		}
		moved[id.String()] = position
	}
	return moved, nil
}

// emitStageArchived writes the audit row and the facts it links: the
// stage's own archival, plus the reorder as ONE pipeline.updated when
// the gap-close actually moved something (a removal at the end of the
// list moves nothing, and an empty delta is not a reorder).
func emitStageArchived(
	ctx context.Context, tx pgx.Tx, id ids.StageID, pipelineID ids.PipelineID, moved map[string]any,
) error {
	auditID, err := storekit.Audit(ctx, tx, "archive", "stage", id.UUID, nil, map[string]any{
		"pipeline_id": pipelineID,
	})
	if err != nil {
		return fmt.Errorf("audit stage archive: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventStageArchived{
		PipelineId: openapi_types.UUID(pipelineID.UUID),
	}); err != nil {
		return fmt.Errorf("emit stage.archived: %w", err)
	}
	if len(moved) == 0 {
		return nil
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, pipelineID.UUID, crmcontracts.PublicEventPipelineUpdated{
		ChangedFields: map[string]any{"stage_positions": moved},
	}); err != nil {
		return fmt.Errorf("emit pipeline reorder after stage archive: %w", err)
	}
	return nil
}
