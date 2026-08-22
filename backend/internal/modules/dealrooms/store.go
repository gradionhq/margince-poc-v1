// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// The object name this module gates on. A room is not row-scoped in its own
// right — it carries no owner_id, and its visibility is entirely its deal's.
const roomObject = "deal_room"

// The parent table every room read scopes through. A caller who cannot see the
// deal cannot see its room, so `deal` — not `deal_room` — is the table the
// row-scope clause names.
const dealTable = "deal"

// fieldRoomID names the room in an audit image. Every participant write records
// which room it happened in, so the three writers must agree on the spelling.
const fieldRoomID = "room_id"

// columnTitle is the room's headline, named once because the mapping refuses an
// empty one, the audit image records it and the release snapshot freezes it —
// three writers that must agree on the spelling.
const columnTitle = "title"

// Store owns this module's tables; every write rides the storekit audit+outbox
// shape in one transaction.
type Store struct {
	// db binds the workspace this store runs for.
	db *database.DB
}

// NewStore opens this module's store on a handle already bound to the
// workspace it serves.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
}

// roomColumns is the projection every room read returns, in the order
// scanRoom consumes it.
const roomColumns = `r.id, r.deal_id, r.title, r.welcome_message, r.state,
	r.steward_user_id, r.expires_at, r.published_at, r.closed_at,
	r.source, r.captured_by, r.version, r.created_at, r.updated_at, r.archived_at,
	(SELECT count(*) FROM deal_room_release rel WHERE rel.room_id = r.id)`
