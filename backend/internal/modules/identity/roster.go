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
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListUsersInput narrows and pages the roster; Q is a case-insensitive
// match over display_name/email (nil or empty = the whole roster).
type ListUsersInput struct {
	Q      *string
	Cursor *string
	Limit  *int
	// IncludeInactive widens the roster to deactivated/suspended members —
	// the admin management view; the default active-only roster serves the
	// share/assignee pickers. The server gates the widened view to admins.
	IncludeInactive bool
	// WithRoles reads each member's role keys. Admin-only, and the reason the
	// read is optional at all: the pickers every other member uses this roster
	// for never render a role, so they should not pay per row to fetch one.
	// The wire mapping withholds the keys independently — this makes the
	// non-admin page not even carry them out of the database.
	WithRoles bool
}

type userRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Email       string
	DisplayName string
	Status      string
	IsAgent     bool
	// Roles are the member's assigned system role keys. NIL means the read did
	// not ask for them; EMPTY means it did and the member holds none. Keeping
	// those apart is what stops a caller that forgot the flag from reporting
	// "holds no role" — a false statement about someone's privileges — and it
	// is why the SQL returns NULL rather than '{}' on the unread path.
	Roles     []string
	CreatedAt time.Time
}

// roleKeys aggregates the member's assigned role keys, sorted so a member
// holding more than one reads the same way on every request. Only an admin's
// response carries them, and the picker reads every other member makes would
// throw them away, so $1 gates the lookup: the ELSE arm makes the whole
// aggregate unevaluated work the row never does. It correlates to the UNALIASED
// app_user of the enclosing SELECT — every userColumns query spells it that
// way; an alias there would silently break the correlation.
const roleKeys = `CASE WHEN $1::boolean THEN
	  (SELECT COALESCE(array_agg(r.key ORDER BY r.key), '{}')
	     FROM role_assignment ra JOIN role r ON r.id = ra.role_id
	     WHERE ra.user_id = app_user.id)
	  ELSE NULL::text[] END`

const userColumns = `id, workspace_id, email, display_name, status, is_agent, ` + roleKeys + `, created_at`

// $1 is the "read role keys?" flag on every user query below, so the aggregate
// stays inside ONE fixed query string instead of two the caller picks between.
// The untaken CASE arm's subquery is not EXECUTED (EXPLAIN ANALYZE with the
// flag bound false shows no SubPlan running), so a non-admin page pays no
// per-row role lookup — it still carries the arm in its plan. NULL (not read)
// is deliberately not '{}' (read, holds none) — see userRow.Roles.
const listUsersQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL AND status = 'active'
	  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3))
	ORDER BY created_at, id
	LIMIT $4`

const listUsersFilteredQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL AND status = 'active'
	  AND (display_name ILIKE $2 OR email ILIKE $2)
	  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4))
	ORDER BY created_at, id
	LIMIT $5`

// The admin management roster: every non-archived member regardless of status,
// so a deactivated member is visible to reactivate.
const listUsersAllQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL
	  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3))
	ORDER BY created_at, id
	LIMIT $4`

const listUsersAllFilteredQuery = `
	SELECT ` + userColumns + `
	FROM app_user
	WHERE archived_at IS NULL
	  AND (display_name ILIKE $2 OR email ILIKE $2)
	  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4))
	ORDER BY created_at, id
	LIMIT $5`

func scanUser(r pgx.Row) (userRow, error) {
	var u userRow
	err := r.Scan(&u.ID, &u.WorkspaceID, &u.Email, &u.DisplayName, &u.Status, &u.IsAgent, &u.Roles, &u.CreatedAt)
	return u, err
}

const getUserQuery = `SELECT ` + userColumns + ` FROM app_user WHERE id = $2 AND archived_at IS NULL`

// GetUser reads one member by id regardless of status (RLS-scoped to the
// caller's workspace) — the read every admin write returns after a mutation, so
// it always asks for the role keys. ErrNotFound when absent or archived.
func (s *Service) GetUser(ctx context.Context, userID ids.UserID) (userRow, error) {
	var u userRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, scanErr := scanUser(tx.QueryRow(ctx, getUserQuery, true, userID))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if scanErr != nil {
			return scanErr
		}
		u = row
		return nil
	})
	return u, err
}

// ListUsers returns one keyset page of the caller's workspace's active
// members (row-scoped by RLS), optionally filtered by in.Q.
func (s *Service) ListUsers(ctx context.Context, in ListUsersInput) ([]userRow, storekit.Page, error) {
	plain, filtered := listUsersQuery, listUsersFilteredQuery
	if in.IncludeInactive {
		plain, filtered = listUsersAllQuery, listUsersAllFilteredQuery
	}
	return listRosterPage(ctx, s.pool, in.Q, in.Cursor, in.Limit, rosterQuery[userRow]{
		plain:     plain,
		filtered:  filtered,
		leadArgs:  []any{in.WithRoles},
		scan:      scanUser,
		cursorKey: func(u userRow) (time.Time, ids.UUID) { return u.CreatedAt, u.ID },
	})
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
	return listRosterPage(ctx, s.pool, in.Q, in.Cursor, in.Limit, rosterQuery[teamRow]{
		plain:     listTeamsQuery,
		filtered:  listTeamsFilteredQuery,
		scan:      scanTeam,
		cursorKey: func(tm teamRow) (time.Time, ids.UUID) { return tm.CreatedAt, tm.ID },
	})
}

// rosterCursor is the decoded keyset position both roster lists page from: the
// house (created_at, id) tuple, nil when the caller sent no cursor — the
// `::timestamptz IS NULL` branch then matches every row. Its bind NUMBER differs
// per list (the user queries spend $1 on the role-key flag), which is exactly
// why this says which branch rather than which parameter.
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

// rosterQuery bundles the per-row-type plumbing listRosterPage needs so the
// shared pager takes one spec instead of four positional callbacks: the two
// fixed query strings (unfiltered + q-filtered), the row scanner, and the
// keyset-cursor extractor.
type rosterQuery[T userRow | teamRow] struct {
	plain    string
	filtered string
	// leadArgs bind BEFORE the pager's own cursor/limit args, so a row type
	// that needs its own parameter — the user roster's "read role keys?" flag —
	// declares it here and takes $1, leaving the pager's numbering identical
	// for a list that declares none.
	leadArgs  []any
	scan      func(pgx.Row) (T, error)
	cursorKey func(T) (time.Time, ids.UUID)
}

// listRosterPage is the one shared shape both roster lists (users, teams)
// run: decode the cursor, run the q-filtered or unfiltered fixed query
// (never a concatenated WHERE), and truncate the (limit+1)-row window into
// a page + continuation cursor. Generic over the row type so ListUsers and
// ListTeams share this instead of carrying two copies of the same plumbing.
func listRosterPage[T userRow | teamRow](
	ctx context.Context, pool *pgxpool.Pool,
	q, cursor *string, limitIn *int,
	spec rosterQuery[T],
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
			args := append(append([]any{}, spec.leadArgs...), "%"+*q+"%", after.createdAt, after.id, limit+1)
			rows, err = tx.Query(ctx, spec.filtered, args...)
		} else {
			args := append(append([]any{}, spec.leadArgs...), after.createdAt, after.id, limit+1)
			rows, err = tx.Query(ctx, spec.plain, args...)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := spec.scan(rows)
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
	createdAt, id := spec.cursorKey(pageRows[len(pageRows)-1])
	return pageRows, storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(createdAt, id)}, nil
}
