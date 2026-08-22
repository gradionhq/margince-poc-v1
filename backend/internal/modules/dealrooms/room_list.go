// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ListRoomsInput narrows a page of rooms.
type ListRoomsInput struct {
	DealID *ids.DealID
	State  *string
	// ParticipantEmail narrows to rooms this address holds a live seat in.
	// Lowercased by the mapping, like every address this module compares.
	ParticipantEmail *string
	IncludeArchived  bool
	Limit            *int
	Cursor           *string
}

// ListRooms pages the Deal Rooms whose deals the caller can see.
func (s *Store) ListRooms(ctx context.Context, in ListRoomsInput) ([]crmcontracts.DealRoom, storekit.Page, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	var (
		out  []crmcontracts.DealRoom
		page storekit.Page
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, page, err = roomPage(ctx, tx, in)
		return err
	})
	return out, page, err
}

func roomPage(ctx context.Context, tx pgx.Tx, in ListRoomsInput) ([]crmcontracts.DealRoom, storekit.Page, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := dealScopeClause(ctx, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where := []string{scope}
	if !in.IncludeArchived {
		where = append(where, "r.archived_at IS NULL")
	}
	if in.DealID != nil {
		where = append(where, storekit.SQLf("r.deal_id = $%d", arg(in.DealID)))
	}
	if in.State != nil {
		where = append(where, storekit.SQLf("r.state = $%d", arg(*in.State)))
	}
	if in.ParticipantEmail != nil {
		where = append(where, storekit.SQLf(
			`EXISTS (SELECT 1 FROM deal_room_participant p
			          WHERE p.room_id = r.id AND p.email = $%d AND p.revoked_at IS NULL AND NOT p.preview)`,
			arg(*in.ParticipantEmail)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		decoded, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(r.created_at, r.id) < ($%d, $%d)",
			arg(decoded.CreatedAt), arg(decoded.ID)))
	}

	size := storekit.ClampLimit(in.Limit)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM deal_room r JOIN deal d ON d.id = r.deal_id
		  WHERE %s ORDER BY r.created_at DESC, r.id DESC LIMIT %d`,
		roomColumns, strings.Join(where, " AND "), size+1), args...)
	if err != nil {
		return nil, storekit.Page{}, fmt.Errorf("list deal rooms: %w", err)
	}
	defer rows.Close()

	out := make([]crmcontracts.DealRoom, 0, size)
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, storekit.Page{}, fmt.Errorf("scan deal room: %w", err)
		}
		out = append(out, room)
	}
	if err := rows.Err(); err != nil {
		return nil, storekit.Page{}, fmt.Errorf("read deal rooms: %w", err)
	}

	var page storekit.Page
	if len(out) > size {
		out = out[:size]
		createdAt, id := roomKey(out[len(out)-1])
		page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(createdAt, id)}
	}
	return out, page, nil
}

// ArchiveRoom ends the room. Buyer access goes with it; the releases stay, so
// what a buyer was shown remains answerable after the room itself is gone.
func (s *Store) ArchiveRoom(ctx context.Context, id ids.DealRoomID, ifVersion *int64) (crmcontracts.DealRoom, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionDelete); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	var out crmcontracts.DealRoom
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readRoom(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, current); err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			out = current
			return nil
		}
		if err := archiveRoomTx(ctx, tx, current, ifVersion); err != nil {
			return err
		}
		out, err = readRoom(ctx, tx, id)
		return err
	})
	return out, err
}

func archiveRoomTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, ifVersion *int64) error {
	id := ids.UUID(room.Id)
	p := storekit.NewPatch()
	// state and archived_at move together: the schema's CHECK requires them to
	// agree, so setting one alone would be refused by the database rather than
	// leaving a row that reads live to the state machine and archived to every
	// list query.
	p.Set("state", string(room.State), stateArchived)
	p.Set("archived_at", nil, time.Now().UTC())

	if err := p.ApplyGuarded(ctx, tx, roomObject, id, ifVersion); err != nil {
		return fmt.Errorf("archive deal room: %w", err)
	}
	// Revoking sessions is what makes archiving mean anything to a buyer: the
	// room row alone is invisible to them, and a live session would keep
	// answering with the last release until it expired on its own.
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_session SET revoked_at = now()
		  WHERE room_id = $1 AND revoked_at IS NULL`, id); err != nil {
		return fmt.Errorf("revoke deal room sessions: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "archive", roomObject, id, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit deal room archive: %w", err)
	}
	archived := crmcontracts.PublicEventDealRoomArchived{DealId: room.DealId}
	if err := storekit.EmitEvent(ctx, tx, auditID, id, archived); err != nil {
		return fmt.Errorf("emit deal_room.archived: %w", err)
	}
	return nil
}
