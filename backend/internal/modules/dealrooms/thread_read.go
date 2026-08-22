// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// Reading the conversation. One projection serves BOTH sides: the buyer and
// the seller read the same threads in the same shape, with each author named
// as the side that wrote and the name that side goes by. The reads here take
// a room id and nothing else; the caller — seller store or session store —
// has already decided that the room is theirs to read.

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

const (
	threadObject   = "deal_room_thread"
	commentObject  = "deal_room_comment"
	decisionObject = "deal_room_decision"

	threadOpen     = "open"
	threadResolved = "resolved"

	decisionRequestChanges = "request_changes"
	decisionConfirmVersion = "confirm_version"
)

// authorColumns resolves a row's author to (side, name) in SQL, so every
// reader agrees on the spelling. A departed user still has a display name on
// app_user; a participant keeps theirs after revocation.
const authorColumns = `CASE WHEN %[1]s.author_participant_id IS NOT NULL THEN 'buyer' ELSE 'seller' END,
	coalesce(bp.full_name, su.display_name, '')`

const authorJoins = `LEFT JOIN deal_room_participant bp ON bp.id = %[1]s.author_participant_id
	LEFT JOIN app_user su ON su.id = %[1]s.author_user_id`

// threadRows reads a room's threads with their comments, oldest first.
func threadRows(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, documentID *ids.UUID) ([]crmcontracts.DealRoomThread, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := fmt.Sprintf("t.room_id = $%d", arg(roomID))
	if documentID != nil {
		where += fmt.Sprintf(" AND t.document_id = $%d", arg(*documentID))
	}
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT t.id, t.room_id, t.document_id, t.required_change, t.state, t.created_at, t.resolved_at, t.version, `+authorColumns+`
		   FROM deal_room_thread t `+authorJoins+`
		  WHERE %[2]s
		  ORDER BY t.created_at, t.id`, "t", where), args...)
	if err != nil {
		return nil, fmt.Errorf("list deal room threads: %w", err)
	}
	defer rows.Close()
	var out []crmcontracts.DealRoomThread
	byID := map[openapi_types.UUID]int{}
	for rows.Next() {
		th, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		byID[th.Id] = len(out)
		out = append(out, th)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room threads: %w", err)
	}
	if len(out) == 0 {
		return []crmcontracts.DealRoomThread{}, nil
	}
	if err := attachComments(ctx, tx, roomID, out, byID); err != nil {
		return nil, err
	}
	return out, nil
}

func scanThread(row rowScanner) (crmcontracts.DealRoomThread, error) {
	var th crmcontracts.DealRoomThread
	var resolvedAt *time.Time
	if err := row.Scan(&th.Id, &th.RoomId, &th.DocumentId, &th.RequiredChange, &th.State,
		&th.CreatedAt, &resolvedAt, &th.Version, &th.Author.Side, &th.Author.Name); err != nil {
		return crmcontracts.DealRoomThread{}, fmt.Errorf("scan deal room thread: %w", err)
	}
	th.ResolvedAt = resolvedAt
	th.Comments = []crmcontracts.DealRoomComment{}
	return th, nil
}

// attachComments fills every thread's comments in one query over the room.
func attachComments(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, threads []crmcontracts.DealRoomThread, byID map[openapi_types.UUID]int) error {
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT c.id, c.thread_id, c.body, c.created_at, `+authorColumns+`
		   FROM deal_room_comment c `+authorJoins+`
		  WHERE c.room_id = $1
		  ORDER BY c.created_at, c.id`, "c"), roomID)
	if err != nil {
		return fmt.Errorf("list deal room comments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c crmcontracts.DealRoomComment
		if err := rows.Scan(&c.Id, &c.ThreadId, &c.Body, &c.CreatedAt, &c.Author.Side, &c.Author.Name); err != nil {
			return fmt.Errorf("scan deal room comment: %w", err)
		}
		if i, ok := byID[c.ThreadId]; ok {
			threads[i].Comments = append(threads[i].Comments, c)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read deal room comments: %w", err)
	}
	return nil
}

// readThread returns one thread of a room with its comments. A thread of
// another room is absent, never refused.
func readThread(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, threadID ids.UUID) (crmcontracts.DealRoomThread, error) {
	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT t.id, t.room_id, t.document_id, t.required_change, t.state, t.created_at, t.resolved_at, t.version, `+authorColumns+`
		   FROM deal_room_thread t `+authorJoins+`
		  WHERE t.room_id = $1 AND t.id = $2`, "t"), roomID, threadID)
	th, err := scanThread(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoomThread{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	threads := []crmcontracts.DealRoomThread{th}
	if err := attachComments(ctx, tx, roomID, threads, map[openapi_types.UUID]int{th.Id: 0}); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	return threads[0], nil
}

// ListThreads is the seller's read.
func (s *Store) ListThreads(ctx context.Context, roomID ids.DealRoomID, documentID *ids.UUID) ([]crmcontracts.DealRoomThread, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.DealRoomThread
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		var err error
		out, err = threadRows(ctx, tx, roomID, documentID)
		return err
	})
	return out, err
}

// ListDecisions is the seller's read of what the buyer decided, newest first.
func (s *Store) ListDecisions(ctx context.Context, roomID ids.DealRoomID) ([]crmcontracts.DealRoomDecision, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.DealRoomDecision
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT d.id, d.room_id, d.document_id, d.attachment_id, d.participant_id, p.full_name, d.kind, d.note, d.created_at
			   FROM deal_room_decision d JOIN deal_room_participant p ON p.id = d.participant_id
			  WHERE d.room_id = $1 ORDER BY d.created_at DESC, d.id DESC`, roomID)
		if err != nil {
			return fmt.Errorf("list deal room decisions: %w", err)
		}
		defer rows.Close()
		out = []crmcontracts.DealRoomDecision{}
		for rows.Next() {
			d, err := scanDecision(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func scanDecision(row rowScanner) (crmcontracts.DealRoomDecision, error) {
	var d crmcontracts.DealRoomDecision
	var name string
	if err := row.Scan(&d.Id, &d.RoomId, &d.DocumentId, &d.AttachmentId, &d.ParticipantId, &name, &d.Kind, &d.Note, &d.CreatedAt); err != nil {
		return crmcontracts.DealRoomDecision{}, fmt.Errorf("scan deal room decision: %w", err)
	}
	d.ParticipantName = &name
	return d, nil
}

// openRequiredThreads counts what still blocks confirming a document.
func openRequiredThreads(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, documentID ids.UUID) (int, error) {
	var n int
	// FOR SHARE on the rows counted, so a resolve racing this confirmation
	// cannot commit between the count and the decision's insert.
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT 1 FROM deal_room_thread
		   WHERE room_id = $1 AND document_id = $2 AND required_change AND state = 'open'
		   FOR SHARE) blocking`, roomID, documentID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count blocking deal room threads: %w", err)
	}
	return n, nil
}
