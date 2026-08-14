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

// lockLiveStageTarget ends the two lookups that resolve the live stage a
// write is about to bind a deal to — create's birth stage and advance's
// target. It is what makes the occupancy count below a GUARD rather than a
// hint.
//
// As a plain read those lookups take no lock, so a deal could resolve a
// live stage, the removal could then count zero deals and archive it, and
// the deal's own write would still land: the FK on deal.stage_id checks
// that the row EXISTS, and archiving is precisely the operation that leaves
// it existing. The result is a live deal on a removed stage — invisible to
// every board read, which all filter on live stages.
//
// FOR KEY SHARE is the weakest lock that conflicts with the removal's FOR
// UPDATE, so a rename or a probability edit still runs alongside a deal
// moving. Whichever side takes it first, the outcome is right: the removal
// waits and then counts the deal, or the deal's lookup waits and is
// re-evaluated against the committed archived_at and finds nothing live.
const lockLiveStageTarget = ` FOR KEY SHARE`

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

// ArchiveStage removes a stage from its pipeline. The survivors are
// renumbered so positions stay contiguous, which is a pipeline-level fact
// and rides ONE pipeline.updated — the same rule UpdateStage's reorder
// branch follows — and none is published when nothing actually moved.
func (s *Store) ArchiveStage(ctx context.Context, id ids.StageID, ifVersion *int64) error {
	if err := auth.Require(ctx, "pipeline", principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The pipeline row is the serialization point for every write
		// that reshapes its stage list — see lockStageConfig. Taken
		// before the stage's own lock, and by the reorder path too, so
		// an archive and a reorder racing on one pipeline queue instead
		// of deadlocking on each other's stage rows.
		pipelineID, err := lockStageConfig(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := storekit.LockRow(ctx, tx, "stage", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		if err := refuseUnremovableStage(ctx, tx, id, ifVersion); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stage SET archived_at = $2 WHERE id = $1`, id, time.Now().UTC()); err != nil {
			return fmt.Errorf("archive stage: %w", err)
		}
		moved, err := renumberStages(ctx, tx, pipelineID)
		if err != nil {
			return err
		}
		return emitStageArchived(ctx, tx, id, pipelineID, moved)
	})
}

// lockStageConfig takes the stage's PIPELINE row for update and answers
// its id — the one serialization point for reshaping a pipeline's stage
// list.
//
// It exists because the removal and the reorder each lock stage rows in an
// order the other does not follow: an archive holds the stage it is
// removing and then walks upward, while a reorder holds the stage it is
// moving and waits on the unique index for the slot the archive just
// vacated. That is an AB-BA cycle, and PostgreSQL answers it by aborting
// one side — a 500 on a legitimate admin action. One row taken first by
// both paths removes the cycle rather than diagnosing it.
func lockStageConfig(ctx context.Context, tx pgx.Tx, id ids.StageID) (ids.PipelineID, error) {
	var pipelineID ids.PipelineID
	err := tx.QueryRow(ctx,
		`SELECT pipeline_id FROM stage WHERE id = $1 AND archived_at IS NULL`, id).Scan(&pipelineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pipelineID, apperrors.ErrNotFound
	}
	if err != nil {
		return pipelineID, fmt.Errorf("read the stage's pipeline: %w", err)
	}
	if _, err := storekit.LockRow(ctx, tx, "pipeline", pipelineID.UUID, storekit.LiveOnly); err != nil {
		return pipelineID, err
	}
	return pipelineID, nil
}

// refuseUnremovableStage answers every refusal the locked stage can be
// judged on before it is touched: version skew, the terminal pair, and
// the deals still standing on it.
func refuseUnremovableStage(ctx context.Context, tx pgx.Tx, id ids.StageID, ifVersion *int64) error {
	var version int64
	var semantic string
	err := tx.QueryRow(ctx,
		`SELECT version, semantic FROM stage WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&version, &semantic)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read stage before archive: %w", err)
	}
	if ifVersion != nil && *ifVersion != version {
		return apperrors.ErrVersionSkew
	}
	if StageSemantic(semantic) != SemanticOpen {
		return &TerminalStageError{Semantic: semantic}
	}
	return refuseIfOccupied(ctx, tx, id)
}

// refuseIfOccupied answers the occupancy refusal, or nil when the stage
// holds nothing. Live deals only: an archived deal cannot be moved off
// the stage, so refusing on one would leave the admin with no way
// forward — and its FK keeps pointing at a row archiving leaves in place.
//
// The COUNT is unscoped and the NAMES are row-scoped, and the split is
// deliberate. Occupancy is a fact about the stage: a removal that skipped
// the deals its caller cannot see would strand exactly the rows the guard
// exists to protect. The names are records, and a record handed back
// carries the row-scope gate like any other read — so a caller whose scope
// hides a blocking deal is told the true count and names only what they
// may see, with the cap's "and N more" covering the rest.
func refuseIfOccupied(ctx context.Context, tx pgx.Tx, id ids.StageID) error {
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM deal WHERE stage_id = $1 AND archived_at IS NULL`, id).Scan(&count); err != nil {
		return fmt.Errorf("count deals on stage: %w", err)
	}
	if count == 0 {
		return nil
	}
	args := []any{id, namedBlockingDeals}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := auth.ScopeClauseFor(ctx, "deal", "", arg)
	if err != nil {
		return err
	}
	if scope != "" {
		scope = " AND " + scope
	}
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT id, name FROM deal WHERE stage_id = $1 AND archived_at IS NULL%s
		 ORDER BY created_at LIMIT $2`, scope), args...)
	if err != nil {
		return fmt.Errorf("name deals on stage: %w", err)
	}
	defer rows.Close()
	// The query is capped, so the count is the wrong capacity: a stage
	// holding thousands of deals would size this slice for all of them
	// and fill ten.
	named := make([]BlockingDeal, 0, min(count, namedBlockingDeals))
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

// renumberStages restates the pipeline's surviving stages as positions
// 1..n in their existing order, and reports the rows that actually moved
// for the reorder event.
//
// It renumbers the WHOLE list, not just the stages above the removed one,
// because contiguity is a postcondition of the removal and not an
// invariant the schema holds: uq_stage_position enforces uniqueness only,
// and both createStage and updateStage take the position they are handed.
// Shifting only the tail would leave a pipeline that was already gapped
// gapped, so the removal's own promise would hold on a seeded pipeline and
// quietly not on a hand-configured one. Rows already at their rank are
// left alone, so the ordinary case still touches only the tail.
//
// Ascending, one row at a time, on purpose: uq_stage_position is a per-row
// check, and one set-based statement would depend on PostgreSQL visiting
// the rows in an order that keeps every intermediate state unique — which
// it does not promise. Ascending, the i-th row's target is i+1, which no
// row still holds: the rows below it have already moved down, and a row
// above it sits at a position strictly greater than its own index. A
// pipeline holds a handful of stages, so the loop is bounded by the
// bounded-config surface itself.
//
// FOR UPDATE because these rows are read and then written; the pipeline
// lock the caller already holds is what keeps a concurrent reorder out.
func renumberStages(ctx context.Context, tx pgx.Tx, pipelineID ids.PipelineID) (map[string]any, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, position FROM stage
		 WHERE pipeline_id = $1 AND archived_at IS NULL
		 ORDER BY position FOR UPDATE`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("read the surviving stages: %w", err)
	}
	type placed struct {
		id       ids.StageID
		position int
	}
	var surviving []placed
	for rows.Next() {
		var s placed
		if err := rows.Scan(&s.id, &s.position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan a surviving stage: %w", err)
		}
		surviving = append(surviving, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the surviving stages: %w", err)
	}
	moved := map[string]any{}
	for i, s := range surviving {
		rank := i + 1
		if s.position == rank {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stage SET position = $2 WHERE id = $1`, s.id, rank); err != nil {
			if storekit.IsUniqueViolation(err) {
				return nil, apperrors.ErrConflict
			}
			return nil, fmt.Errorf("renumber the surviving stages: %w", err)
		}
		moved[s.id.String()] = rank
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
