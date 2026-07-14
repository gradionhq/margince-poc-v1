// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The workspace roster reads (A52 sharing needs a subject picker + name
// resolution): flat, row-scoped member/team lists, active seats only.
// Keyset-paginated like every other list surface (storekit.EncodeCursor/
// DecodeCursor + ClampLimit) — the roster can grow past one page, unlike
// ListRecordGrants. The q filter and the unfiltered read are two fixed,
// explicitly parameterized query strings (never a concatenated WHERE) so
// no request-shaped input reaches the SQL as anything but a bind value.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListUsersInput narrows and pages the roster; Q is a case-insensitive
// match over display_name/email (nil or empty = the whole roster).
type ListUsersInput struct {
	Q      *string
	Cursor *string
	Limit  *int
}

type userRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Email       string
	DisplayName string
	Status      string
	IsAgent     bool
	CreatedAt   time.Time
}

const userColumns = `id, workspace_id, email, display_name, status, is_agent, created_at`

const listUsersQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL AND status = 'active'
	  AND ($1::timestamptz IS NULL OR (created_at, id) > ($1, $2))
	ORDER BY created_at, id
	LIMIT $3`

const listUsersFilteredQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL AND status = 'active'
	  AND (display_name ILIKE $1 OR email ILIKE $1)
	  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3))
	ORDER BY created_at, id
	LIMIT $4`

func scanUser(r pgx.Row) (userRow, error) {
	var u userRow
	err := r.Scan(&u.ID, &u.WorkspaceID, &u.Email, &u.DisplayName, &u.Status, &u.IsAgent, &u.CreatedAt)
	return u, err
}

// ListUsers returns one keyset page of the caller's workspace's active
// members (row-scoped by RLS), optionally filtered by in.Q.
func (s *Service) ListUsers(ctx context.Context, in ListUsersInput) ([]userRow, storekit.Page, error) {
	return listRosterPage(ctx, s.pool, in.Q, in.Cursor, in.Limit,
		listUsersQuery, listUsersFilteredQuery, scanUser,
		func(u userRow) (time.Time, ids.UUID) { return u.CreatedAt, u.ID })
}

// ListTeamsInput narrows and pages the team list; Q is a case-insensitive
// match over the team name (nil or empty = every team).
type ListTeamsInput struct {
	Q      *string
	Cursor *string
	Limit  *int
}

type teamRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Name        string
	MemberCount int
	CreatedAt   time.Time
}

// teamColumns is the roster team SELECT list: the active-member count
// joins app_user so a suspended/deactivated seat's membership row never
// inflates the count (mirrors the active-only gate on ListUsers).
const teamColumns = `t.id, t.workspace_id, t.name, COUNT(u.id) AS member_count, t.created_at`

const teamFromJoin = `
	FROM team t
	LEFT JOIN team_membership tm ON tm.team_id = t.id
	LEFT JOIN app_user u ON u.id = tm.user_id AND u.status = 'active' AND u.archived_at IS NULL`

const listTeamsQuery = `
	SELECT ` + teamColumns + teamFromJoin + `
	WHERE t.archived_at IS NULL
	  AND ($1::timestamptz IS NULL OR (t.created_at, t.id) > ($1, $2))
	GROUP BY t.id
	ORDER BY t.created_at, t.id
	LIMIT $3`

const listTeamsFilteredQuery = `
	SELECT ` + teamColumns + teamFromJoin + `
	WHERE t.archived_at IS NULL AND t.name ILIKE $1
	  AND ($2::timestamptz IS NULL OR (t.created_at, t.id) > ($2, $3))
	GROUP BY t.id
	ORDER BY t.created_at, t.id
	LIMIT $4`

func scanTeam(r pgx.Row) (teamRow, error) {
	var tm teamRow
	err := r.Scan(&tm.ID, &tm.WorkspaceID, &tm.Name, &tm.MemberCount, &tm.CreatedAt)
	return tm, err
}

// ListTeams returns one keyset page of the caller's workspace's active
// teams (row-scoped by RLS) with each team's active-membership count,
// optionally filtered by in.Q.
func (s *Service) ListTeams(ctx context.Context, in ListTeamsInput) ([]teamRow, storekit.Page, error) {
	return listRosterPage(ctx, s.pool, in.Q, in.Cursor, in.Limit,
		listTeamsQuery, listTeamsFilteredQuery, scanTeam,
		func(tm teamRow) (time.Time, ids.UUID) { return tm.CreatedAt, tm.ID })
}

// rosterCursor is the decoded keyset position both roster lists page from:
// the house (created_at, id) tuple, nil when the caller sent no cursor (the
// $1::timestamptz IS NULL branch in both queries then matches every row).
type rosterCursor struct {
	createdAt *time.Time
	id        *ids.UUID
}

func decodeRosterCursor(token *string) (rosterCursor, error) {
	if token == nil || *token == "" {
		return rosterCursor{}, nil
	}
	c, err := storekit.DecodeCursor(*token)
	if err != nil {
		return rosterCursor{}, err
	}
	createdAt, id := c.CreatedAt, c.ID
	return rosterCursor{createdAt: &createdAt, id: &id}, nil
}

// listRosterPage is the one shared shape both roster lists (users, teams)
// run: decode the cursor, run the q-filtered or unfiltered fixed query
// (never a concatenated WHERE), and truncate the (limit+1)-row window into
// a page + continuation cursor. Generic over the row type so ListUsers and
// ListTeams share this instead of carrying two copies of the same plumbing.
func listRosterPage[T userRow | teamRow](
	ctx context.Context, pool *pgxpool.Pool,
	q, cursor *string, limitIn *int,
	plainQuery, filteredQuery string,
	scan func(pgx.Row) (T, error),
	cursorKey func(T) (time.Time, ids.UUID),
) ([]T, storekit.Page, error) {
	limit := storekit.ClampLimit(limitIn)
	after, err := decodeRosterCursor(cursor)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	var pageRows []T
	err = database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if q != nil && *q != "" {
			rows, err = tx.Query(ctx, filteredQuery, "%"+*q+"%", after.createdAt, after.id, limit+1)
		} else {
			rows, err = tx.Query(ctx, plainQuery, after.createdAt, after.id, limit+1)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scan(rows)
			if err != nil {
				return err
			}
			pageRows = append(pageRows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if len(pageRows) <= limit {
		return pageRows, storekit.Page{}, nil
	}
	pageRows = pageRows[:limit]
	createdAt, id := cursorKey(pageRows[len(pageRows)-1])
	return pageRows, storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(createdAt, id)}, nil
}
