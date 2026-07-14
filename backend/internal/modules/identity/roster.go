// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The workspace roster reads (A52 sharing needs a subject picker + name
// resolution): flat, row-scoped member/team lists. Read-only; no write
// shape. Mirrors ListRecordGrants — all active rows, no keyset (the
// roster is small, and the contract's page envelope stays empty).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListUsersInput narrows the roster; Q is a case-insensitive match over
// display_name/email (nil or empty = the whole roster).
type ListUsersInput struct{ Q *string }

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

func scanUser(r pgx.Row) (userRow, error) {
	var u userRow
	err := r.Scan(&u.ID, &u.WorkspaceID, &u.Email, &u.DisplayName, &u.Status, &u.IsAgent, &u.CreatedAt)
	return u, err
}

// ListUsers returns every active member of the caller's workspace
// (row-scoped by RLS), optionally filtered by in.Q.
func (s *Service) ListUsers(ctx context.Context, in ListUsersInput) ([]userRow, error) {
	var out []userRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := "archived_at IS NULL"
		if in.Q != nil && *in.Q != "" {
			p := arg("%" + *in.Q + "%")
			where += storekit.SQLf(" AND (display_name ILIKE $%d OR email ILIKE $%d)", p, p)
		}
		rows, err := tx.Query(ctx,
			"SELECT "+userColumns+" FROM app_user WHERE "+where+" ORDER BY created_at, id", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// ListTeamsInput narrows the team list; Q is a case-insensitive match over
// the team name (nil or empty = every team).
type ListTeamsInput struct{ Q *string }

type teamRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Name        string
	MemberCount int
	CreatedAt   time.Time
}

func scanTeam(r pgx.Row) (teamRow, error) {
	var tm teamRow
	err := r.Scan(&tm.ID, &tm.WorkspaceID, &tm.Name, &tm.MemberCount, &tm.CreatedAt)
	return tm, err
}

// ListTeams returns every active team in the caller's workspace
// (row-scoped by RLS) with its active-membership count, optionally
// filtered by in.Q.
func (s *Service) ListTeams(ctx context.Context, in ListTeamsInput) ([]teamRow, error) {
	var out []teamRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := "t.archived_at IS NULL"
		if in.Q != nil && *in.Q != "" {
			where += storekit.SQLf(" AND t.name ILIKE $%d", arg("%"+*in.Q+"%"))
		}
		rows, err := tx.Query(ctx,
			`SELECT t.id, t.workspace_id, t.name, COUNT(tm.id) AS member_count, t.created_at
			 FROM team t LEFT JOIN team_membership tm ON tm.team_id = t.id
			 WHERE `+where+`
			 GROUP BY t.id ORDER BY t.created_at, t.id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			tm, err := scanTeam(rows)
			if err != nil {
				return err
			}
			out = append(out, tm)
		}
		return rows.Err()
	})
	return out, err
}
