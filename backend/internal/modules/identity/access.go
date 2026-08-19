// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The access preview: what a seat with a role and teams sees and may do,
// computed from the evaluated policy — the same role documents, field masks
// and read classes the gates read — so the invite screen and the member page
// show the truth rather than a second interpretation of the role.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Access is the evaluated policy for one role-and-teams combination.
type Access struct {
	Role        string
	Permissions principal.Permissions
	Teams       []teamRow
}

// PreviewAccess evaluates a role and a team set that do not belong to anyone
// yet — the invite screen's question. Admin-only: the matrix is the
// installation's configuration.
func (s *Service) PreviewAccess(ctx context.Context, actor Identity, role string, teamIDs []ids.UUID) (Access, error) {
	if !actor.hasRole(roleAdmin) {
		return Access{}, apperrors.ErrPermissionDenied
	}
	teamIDs, err := validTeamIDs(teamIDs)
	if err != nil {
		return Access{}, err
	}
	var out Access
	err = s.db.Tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = accessFor(ctx, tx, []string{role}, teamIDs)
		return err
	})
	return out, err
}

// UserAccess evaluates an existing member's roles and teams as they stand.
func (s *Service) UserAccess(ctx context.Context, actor Identity, userID ids.UserID) (Access, error) {
	if !actor.hasRole(roleAdmin) {
		return Access{}, apperrors.ErrPermissionDenied
	}
	var out Access
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND archived_at IS NULL)`, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperrors.ErrNotFound
		}
		roles, teams, _, err := loadGrants(ctx, tx, userID)
		if err != nil {
			return err
		}
		teamIDs := make([]ids.UUID, 0, len(teams))
		for _, t := range teams {
			teamIDs = append(teamIDs, t.UUID)
		}
		out, err = accessFor(ctx, tx, roles, teamIDs)
		return err
	})
	return out, err
}

// accessFor is the one evaluation both doors share: the role documents
// merged the way login merges them, the field masks the loader loads, and
// the teams resolved to rows.
func accessFor(ctx context.Context, tx pgx.Tx, roleKeys []string, teamIDs []ids.UUID) (Access, error) {
	byRole := map[string]policy.Document{}
	for _, key := range roleKeys {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT permissions FROM role WHERE key = $1 AND archived_at IS NULL`, key).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return Access{}, errUnknownRole
		}
		if err != nil {
			return Access{}, err
		}
		doc, err := policy.Parse(raw)
		if err != nil {
			return Access{}, err
		}
		byRole[key] = doc
	}
	perms := policy.Merge(byRole)
	masks, err := loadFieldMasks(ctx, tx, roleKeys)
	if err != nil {
		return Access{}, err
	}
	perms.FieldMasks = masks
	teams, err := teamsByID(ctx, tx, teamIDs)
	if err != nil {
		return Access{}, err
	}
	role := ""
	if len(roleKeys) == 1 {
		role = roleKeys[0]
	}
	return Access{Role: role, Permissions: perms, Teams: teams}, nil
}

// teamsByID resolves the named teams; one that does not exist answers
// not-found rather than being dropped, so a preview never shows a team set
// the invite would then refuse.
func teamsByID(ctx context.Context, tx pgx.Tx, teamIDs []ids.UUID) ([]teamRow, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.name,
		       (SELECT count(*) FROM team_membership m JOIN app_user u ON u.id = m.user_id AND u.status = 'active' AND u.archived_at IS NULL
		         WHERE m.team_id = t.id),
		       t.created_at
		  FROM team t WHERE t.id = ANY($1) AND t.archived_at IS NULL ORDER BY t.name`, teamIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []teamRow
	for rows.Next() {
		var t teamRow
		if err := rows.Scan(&t.ID, &t.Name, &t.MemberCount, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(teamIDs) {
		return nil, apperrors.ErrNotFound
	}
	return out, nil
}
