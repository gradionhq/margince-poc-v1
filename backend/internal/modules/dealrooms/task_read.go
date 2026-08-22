// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"errors"
	"fmt"
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

// The table a to-do lives in, named for the storekit calls that patch, lock and
// audit it. The RBAC gate is deliberately the ROOM's — keeping a shared to-do
// list is part of running the room, so it takes the room's grant rather than one
// of its own, exactly as the roster does.
const taskObject = "deal_room_task"

// taskColumns is the projection every task read returns, in the order scanTask
// consumes it.
const taskColumns = `t.id, t.room_id, t.side, t.title, t.position,
	t.done_at, t.done_by_participant_id, t.done_by_user_id,
	t.source, t.captured_by, t.version, t.created_at, t.updated_at, t.archived_at`

// ListTasks returns a room's shared to-do list in the order both sides see it.
func (s *Store) ListTasks(ctx context.Context, roomID ids.DealRoomID) ([]crmcontracts.DealRoomTask, storekit.Page, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	var out []crmcontracts.DealRoomTask
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Reading the room first IS the scope gate: it applies the parent deal's
		// visibility clause, so a list cannot be read past a room the caller
		// cannot see.
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		var err error
		out, err = taskRows(ctx, tx, roomID)
		return err
	})
	// The list is small and bounded by what two parties owe each other, so it
	// answers whole rather than paged. The envelope still carries a page object
	// because every list response in this contract does.
	return out, storekit.Page{}, err
}

func taskRows(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) ([]crmcontracts.DealRoomTask, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }

	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM deal_room_task t
		  WHERE t.room_id = $%d AND t.archived_at IS NULL
		  ORDER BY t.position, t.created_at, t.id`,
		taskColumns, arg(roomID)), args...)
	if err != nil {
		return nil, fmt.Errorf("list deal room tasks: %w", err)
	}
	defer rows.Close()

	var out []crmcontracts.DealRoomTask
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room tasks: %w", err)
	}
	return out, nil
}

// readTask returns one live task in a room. A task belonging to another room is
// absent rather than refused, so a caller cannot use a cross-room id to learn
// that one exists.
func readTask(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, taskID ids.DealRoomTaskID) (crmcontracts.DealRoomTask, error) {
	return readTaskIn(ctx, tx, roomID, taskID, " AND t.archived_at IS NULL")
}

// readArchivedTask returns a task the caller just archived, which readTask
// deliberately cannot see. The archive response has to come from the row rather
// than from the pre-update struct with a timestamp pasted on: the version and
// updated_at are written by a trigger, so a hand-built answer reports a version
// the row does not have and the caller's next If-Match fails on it.
func readArchivedTask(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, taskID ids.DealRoomTaskID) (crmcontracts.DealRoomTask, error) {
	return readTaskIn(ctx, tx, roomID, taskID, "")
}

func readTaskIn(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, taskID ids.DealRoomTaskID, liveOnly string) (crmcontracts.DealRoomTask, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	roomPos, taskPos := arg(roomID), arg(taskID)

	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM deal_room_task t
		  WHERE t.room_id = $%d AND t.id = $%d`+liveOnly,
		taskColumns, roomPos, taskPos), args...)

	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoomTask{}, apperrors.ErrNotFound
	}
	return task, err
}

func scanTask(row rowScanner) (crmcontracts.DealRoomTask, error) {
	var (
		out           crmcontracts.DealRoomTask
		side          string
		doneAt        *time.Time
		doneByPartier *openapi_types.UUID
		doneByUser    *openapi_types.UUID
		capturedBy    string
	)
	if err := row.Scan(&out.Id, &out.RoomId, &side, &out.Title, &out.Position,
		&doneAt, &doneByPartier, &doneByUser,
		&out.Source, &capturedBy, &out.Version,
		&out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt); err != nil {
		return crmcontracts.DealRoomTask{}, err
	}
	out.Side = side
	out.DoneAt = doneAt
	out.DoneByParticipantId = doneByPartier
	out.DoneByUserId = doneByUser
	// `done` is derived rather than stored: the completion CHECK ties done_at to
	// exactly one actor, so the timestamp is the single fact a reader needs, and
	// a separate boolean column could disagree with it.
	out.Done = doneAt != nil
	out.CapturedBy = &capturedBy
	return out, nil
}
