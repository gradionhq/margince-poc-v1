// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const releaseColumns = `id, room_id, release_no, snapshot, release_note,
	published_by, published_at, source, captured_by, created_at`

// ListReleases pages a room's releases, newest first.
func (s *Store) ListReleases(ctx context.Context, roomID ids.DealRoomID, limit *int, cursor *string) ([]crmcontracts.DealRoomRelease, storekit.Page, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}

	var (
		out  []crmcontracts.DealRoomRelease
		page storekit.Page
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Reading the room first is the scope gate: it applies the parent
		// deal's visibility clause, so a room the caller cannot see returns
		// not-found here rather than an empty page that implies it exists.
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		var err error
		out, page, err = releasePage(ctx, tx, roomID, limit, cursor)
		return err
	})
	return out, page, err
}

func releasePage(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, limit *int, cursor *string) ([]crmcontracts.DealRoomRelease, storekit.Page, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := []string{storekit.SQLf(`room_id = $%d`, arg(roomID))}

	if cursor != nil && *cursor != "" {
		decoded, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		// Newest first, so the page walks BACKWARDS through the keyset.
		where = append(where, storekit.SQLf(`(created_at, id) < ($%d, $%d)`,
			arg(decoded.CreatedAt), arg(decoded.ID)))
	}

	size := storekit.ClampLimit(limit)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM deal_room_release WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`,
		releaseColumns, strings.Join(where, " AND "), size+1), args...)
	if err != nil {
		return nil, storekit.Page{}, fmt.Errorf("list deal room releases: %w", err)
	}
	defer rows.Close()

	out := make([]crmcontracts.DealRoomRelease, 0, size)
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, storekit.Page{}, fmt.Errorf("scan deal room release: %w", err)
		}
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, storekit.Page{}, fmt.Errorf("read deal room releases: %w", err)
	}

	var page storekit.Page
	if len(out) > size {
		out = out[:size]
		last := out[len(out)-1]
		page = storekit.Page{
			HasMore:    true,
			NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id)),
		}
	}
	return out, page, nil
}

func scanRelease(row rowScanner) (crmcontracts.DealRoomRelease, error) {
	var (
		out         crmcontracts.DealRoomRelease
		id, roomID  ids.UUID
		publishedBy *ids.UUID
		snapshot    []byte
		capturedBy  string
	)
	if err := row.Scan(&id, &roomID, &out.ReleaseNo, &snapshot, &out.ReleaseNote,
		&publishedBy, &out.PublishedAt, &out.Source, &capturedBy, &out.CreatedAt); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	out.Id = openapi_types.UUID(id)
	out.RoomId = openapi_types.UUID(roomID)
	out.CapturedBy = &capturedBy
	if err := json.Unmarshal(snapshot, &out.Snapshot); err != nil {
		return crmcontracts.DealRoomRelease{}, fmt.Errorf("decode release snapshot: %w", err)
	}
	if publishedBy != nil {
		u := openapi_types.UUID(*publishedBy)
		out.PublishedBy = &u
	}
	return out, nil
}
