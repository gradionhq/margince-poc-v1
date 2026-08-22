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

// GetRoom reads one Deal Room.
func (s *Store) GetRoom(ctx context.Context, id ids.DealRoomID) (crmcontracts.DealRoom, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	var out crmcontracts.DealRoom
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readRoom(ctx, tx, id)
		return err
	})
	return out, err
}

// readRoom returns one room the caller may see, or ErrNotFound.
//
// The scope clause is applied INSIDE the query rather than as a separate
// visibility probe: a room whose deal the caller cannot see must be absent, not
// merely refused, and a two-step read leaks the difference through timing and
// through whichever error arrives first.
func readRoom(ctx context.Context, tx pgx.Tx, id ids.DealRoomID) (crmcontracts.DealRoom, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	scope, err := dealScopeClause(ctx, arg)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}

	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM deal_room r
		   JOIN deal d ON d.id = r.deal_id
		  WHERE r.id = $%d AND %s`,
		roomColumns, idPos, scope), args...)

	out, err := scanRoom(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoom{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.DealRoom{}, fmt.Errorf("read deal room: %w", err)
	}
	return out, nil
}

// dealScopeClause is the row-scope predicate for the parent deal, already
// aliased to the join above. It returns "TRUE" rather than "" for an unbounded
// caller so the clause always composes into valid SQL.
func dealScopeClause(ctx context.Context, arg func(any) int) (string, error) {
	scope, err := auth.ScopeClauseFor(ctx, dealTable, "d", arg)
	if err != nil {
		return "", err
	}
	if scope == "" {
		return "TRUE", nil
	}
	return scope, nil
}

// rowScanner is the one method pgx.Row and pgx.Rows agree on, so a single
// scanRoom serves both the single read and the list page.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRoom(row rowScanner) (crmcontracts.DealRoom, error) {
	var (
		out          crmcontracts.DealRoom
		id, dealID   ids.UUID
		steward      *ids.UUID
		capturedBy   string
		version      int64
		releaseCount int
		state        string
	)
	if err := row.Scan(&id, &dealID, &out.Title, &out.WelcomeMessage, &state,
		&steward, &out.ExpiresAt, &out.PublishedAt, &out.ClosedAt,
		&out.Source, &capturedBy, &version, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt,
		&releaseCount); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	out.Id = openapi_types.UUID(id)
	out.DealId = openapi_types.UUID(dealID)
	out.State = crmcontracts.DealRoomState(state)
	out.CapturedBy = &capturedBy
	out.Version = &version
	out.ReleaseCount = &releaseCount
	if steward != nil {
		u := openapi_types.UUID(*steward)
		out.StewardUserId = &u
	}
	return out, nil
}

// roomKey is the keyset sort key for a page of rooms.
func roomKey(r crmcontracts.DealRoom) (time.Time, ids.UUID) {
	return r.CreatedAt, ids.UUID(r.Id)
}
