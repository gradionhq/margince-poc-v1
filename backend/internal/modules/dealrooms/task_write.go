// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CreateTaskInput is the validated shape both transports create a to-do from.
type CreateTaskInput struct {
	Side     string
	Title    string
	Position int
	Source   string
}

// CreateTask adds an item to a room's shared to-do list.
//
// Adding an item changes what the buyer is asked to do, so it is editorial: it
// reaches them at the next publish rather than immediately, and a room that can
// no longer reach a buyer refuses it.
func (s *Store) CreateTask(ctx context.Context, roomID ids.DealRoomID, in CreateTaskInput) (crmcontracts.DealRoomTask, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionCreate); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	var out crmcontracts.DealRoomTask
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = createTaskTx(ctx, tx, roomID, in, by)
		return err
	})
	return out, err
}

func createTaskTx(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, in CreateTaskInput, by string) (crmcontracts.DealRoomTask, error) {
	room, err := readRoom(ctx, tx, roomID)
	if err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	if err := ensureDealWritable(ctx, tx, room); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	if !publishable(string(room.State)) {
		return crmcontracts.DealRoomTask{}, notTaskEditable(string(room.State))
	}

	id := ids.New[ids.DealRoomTaskKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_task (id, room_id, side, title, position, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, roomID, in.Side, in.Title, in.Position, in.Source, by); err != nil {
		return crmcontracts.DealRoomTask{}, fmt.Errorf("insert deal room task: %w", err)
	}

	if _, err := storekit.Audit(ctx, tx, "create", taskObject, id.UUID, nil,
		map[string]any{fieldRoomID: roomID.UUID, "side": in.Side, columnTitle: in.Title}); err != nil {
		return crmcontracts.DealRoomTask{}, fmt.Errorf("audit deal room task create: %w", err)
	}
	return readTask(ctx, tx, roomID, id)
}

// UpdateTaskInput is the validated patch both transports apply to a to-do. Every
// field is optional; an omitted one is left unchanged.
type UpdateTaskInput struct {
	Side      *string
	Title     *string
	Position  *int
	Done      *bool
	IfVersion *int64
}

// UpdateTask rewords, reassigns, reorders or ticks off one item.
//
// The two kinds of change freeze differently, which is the whole reason this
// takes one path rather than two: DEFINITION is editorial and reaches a buyer at
// the next publish, while COMPLETION is live collaboration state both sides
// toggle without republishing. Both are refused once the room can no longer
// reach a buyer, because a closed room's list is a record rather than work.
func (s *Store) UpdateTask(ctx context.Context, roomID ids.DealRoomID, taskID ids.DealRoomTaskID, in UpdateTaskInput) (crmcontracts.DealRoomTask, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	var out crmcontracts.DealRoomTask
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		if !publishable(string(room.State)) {
			return notTaskEditable(string(room.State))
		}
		// Locked before the decision is read, so two people ticking the same item
		// at once cannot both read it open and both stamp themselves as the one
		// who did it.
		if _, err := storekit.LockRow(ctx, tx, taskObject, taskID.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := readTask(ctx, tx, roomID, taskID)
		if err != nil {
			return err
		}
		if err := applyTaskPatch(ctx, tx, room, current, taskID, in); err != nil {
			return err
		}
		out, err = readTask(ctx, tx, roomID, taskID)
		return err
	})
	return out, err
}

func applyTaskPatch(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, current crmcontracts.DealRoomTask, taskID ids.DealRoomTaskID, in UpdateTaskInput) error {
	p := storekit.NewPatch()
	if in.Side != nil {
		p.Set("side", current.Side, *in.Side)
	}
	if in.Title != nil {
		p.Set(columnTitle, current.Title, *in.Title)
	}
	if in.Position != nil {
		p.Set("position", current.Position, *in.Position)
	}
	if in.Done != nil && *in.Done != current.Done {
		if err := setCompletion(ctx, p, *in.Done, current); err != nil {
			return err
		}
	}
	if p.Empty() {
		return nil
	}
	if err := p.ApplyGuarded(ctx, tx, taskObject, taskID.UUID, in.IfVersion); err != nil {
		return err
	}

	auditID, err := storekit.Audit(ctx, tx, "update", taskObject, taskID.UUID, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit deal room task update: %w", err)
	}
	// Only the completion is announced. A reworded or reordered item is editorial
	// and reaches a buyer through the release that publishes it, so announcing it
	// here would report a change nobody outside the seller's tab can yet see.
	if in.Done == nil || *in.Done == current.Done {
		return nil
	}
	changed := crmcontracts.PublicEventDealRoomTaskCompletionChanged{
		DealId: room.DealId,
		TaskId: openapi_types.UUID(taskID.UUID),
		Side:   current.Side,
		Done:   *in.Done,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(room.Id), changed); err != nil {
		return fmt.Errorf("emit deal_room.task_completion_changed: %w", err)
	}
	return nil
}

// setCompletion folds the completion columns into the patch the caller is
// building, so one UPDATE carries both a rewording and a tick.
func setCompletion(ctx context.Context, p *storekit.Patch, done bool, current crmcontracts.DealRoomTask) error {
	completion, err := completionPatch(ctx, done, current)
	if err != nil {
		return err
	}
	for column, after := range completion.After() {
		p.Set(column, completion.Before()[column], after)
	}
	return nil
}

// completionPatch stamps or clears the three columns the completion CHECK ties
// together. They move as one: a half-set completion — done, by nobody — is a row
// the constraint refuses and a reader could not interpret anyway.
func completionPatch(ctx context.Context, done bool, current crmcontracts.DealRoomTask) (*storekit.Patch, error) {
	p := storekit.NewPatch()
	if !done {
		p.Set("done_at", current.DoneAt, nil)
		p.Set("done_by_user_id", current.DoneByUserId, nil)
		p.Set("done_by_participant_id", current.DoneByParticipantId, nil)
		return p, nil
	}
	// Whoever is ticking it off is on the seller's side: the buyer's own half of
	// this list is served by the public edge, which is not built yet, so the
	// participant column stays null on every completion this path writes.
	actor, err := completingUser(ctx)
	if err != nil {
		return nil, err
	}
	p.Set("done_at", nil, time.Now().UTC())
	p.Set("done_by_user_id", current.DoneByUserId, actor)
	p.Set("done_by_participant_id", current.DoneByParticipantId, nil)
	return p, nil
}

// completingUser names the human ticking an item off. It refuses rather than
// returning nothing, because the completion CHECK requires a done row to name an
// actor: a principal carrying no user id would otherwise reach the database as a
// constraint violation, which surfaces to the caller as a 500 naming a table.
func completingUser(ctx context.Context) (ids.UUID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UUID{}, errCompletionNeedsActor
	}
	return p.UserID, nil
}

// ArchiveTask takes an item off the list without deleting it, so an item that
// was completed stays attributed to whoever did it.
func (s *Store) ArchiveTask(ctx context.Context, roomID ids.DealRoomID, taskID ids.DealRoomTaskID, ifVersion *int64) (crmcontracts.DealRoomTask, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionDelete); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	var out crmcontracts.DealRoomTask
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		if !publishable(string(room.State)) {
			return notTaskEditable(string(room.State))
		}
		current, err := readTask(ctx, tx, roomID, taskID)
		if err != nil {
			return err
		}
		archivedAt := time.Now().UTC()
		p := storekit.NewPatch()
		p.Set("archived_at", nil, archivedAt)
		if err := p.ApplyGuarded(ctx, tx, taskObject, taskID.UUID, ifVersion); err != nil {
			return fmt.Errorf("archive deal room task: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", taskObject, taskID.UUID,
			map[string]any{columnTitle: current.Title}, p.After()); err != nil {
			return fmt.Errorf("audit deal room task archive: %w", err)
		}
		// Returned from the row already read plus the stamp just written, because
		// readTask deliberately answers only for live rows — re-reading here
		// would report the item this call just archived as absent.
		out = current
		out.ArchivedAt = &archivedAt
		return nil
	})
	return out, err
}
