// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Team administration. A team is what resolves `row_scope: team` — who may
// EDIT whose records, since customer identity is workspace-readable — and it
// is a share subject, so every change here moves somebody's write authority
// from the next request on. Admin-only, and each change is one transaction:
// the row, its audit row and team.changed on the identity stream.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// maxTeamName mirrors the contract's maxLength.
const maxTeamName = 120

// Team is one team row as the admin surface returns it.
type Team struct {
	ID         ids.UUID
	Name       string
	ArchivedAt *time.Time
}

// CreateTeam makes a team. A name already in use answers ErrConflict — two
// teams with one name would be two answers to "which team is DACH Sales".
func (s *Service) CreateTeam(ctx context.Context, actor Identity, name string) (Team, error) {
	if !actor.hasRole(roleAdmin) {
		return Team{}, apperrors.ErrPermissionDenied
	}
	name, err := validTeamName(name)
	if err != nil {
		return Team{}, err
	}
	ctx = actorCtx(ctx, actor)
	var out Team
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO team (name) VALUES ($1) RETURNING id, name`, name).
			Scan(&out.ID, &out.Name)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: a team named %q already exists", apperrors.ErrConflict, name)
		}
		if err != nil {
			return err
		}
		return s.recordTeamChange(ctx, tx, actor, out.ID, nil, "created", nil, map[string]any{"name": name})
	})
	return out, err
}

// UpdateTeamInput is one rename and/or archive flip; nil leaves a field alone.
type UpdateTeamInput struct {
	Name     *string
	Archived *bool
}

// UpdateTeam renames, archives or restores a team. Archiving keeps the rows
// and the memberships; an archived team stops resolving scope and shares
// because every reader of team_membership joins a live team.
func (s *Service) UpdateTeam(ctx context.Context, actor Identity, id ids.UUID, in UpdateTeamInput) (Team, error) {
	if !actor.hasRole(roleAdmin) {
		return Team{}, apperrors.ErrPermissionDenied
	}
	var name *string
	if in.Name != nil {
		valid, err := validTeamName(*in.Name)
		if err != nil {
			return Team{}, err
		}
		name = &valid
	}
	ctx = actorCtx(ctx, actor)
	var out Team
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var before Team
		if err := tx.QueryRow(ctx, `SELECT id, name, archived_at FROM team WHERE id = $1 FOR UPDATE`, id).
			Scan(&before.ID, &before.Name, &before.ArchivedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return err
		}
		out = before
		if name != nil && *name != before.Name {
			err := tx.QueryRow(ctx, `UPDATE team SET name = $2 WHERE id = $1 RETURNING name`, id, *name).Scan(&out.Name)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return fmt.Errorf("%w: a team named %q already exists", apperrors.ErrConflict, *name)
			}
			if err != nil {
				return err
			}
			if err := s.recordTeamChange(ctx, tx, actor, id, nil, "renamed",
				map[string]any{"name": before.Name}, map[string]any{"name": out.Name}); err != nil {
				return err
			}
		}
		if in.Archived != nil && *in.Archived != (before.ArchivedAt != nil) {
			change, set := "restored", `archived_at = NULL`
			if *in.Archived {
				change, set = "archived", `archived_at = now()`
			}
			if err := tx.QueryRow(ctx, `UPDATE team SET `+set+` WHERE id = $1 RETURNING archived_at`, id).Scan(&out.ArchivedAt); err != nil {
				return err
			}
			if err := s.recordTeamChange(ctx, tx, actor, id, nil, change,
				map[string]any{"archived": before.ArchivedAt != nil}, map[string]any{"archived": *in.Archived}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// SetTeamMember puts a member on a team (on=true) or takes them off. Both are
// idempotent: the state the admin asked for is the state, and a change that
// changes nothing writes no audit noise. An agent seat holds no team.
func (s *Service) SetTeamMember(ctx context.Context, actor Identity, teamID, userID ids.UUID, on bool) error {
	if !actor.hasRole(roleAdmin) {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team WHERE id = $1 AND archived_at IS NULL)`, teamID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperrors.ErrNotFound
		}
		var isAgent bool
		err := tx.QueryRow(ctx, `SELECT is_agent FROM app_user WHERE id = $1 AND archived_at IS NULL`, userID).Scan(&isAgent)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if isAgent {
			return errAgentSeatHoldsNoRole
		}
		var tag pgconn.CommandTag
		change := "member_removed"
		if on {
			change = "member_added"
			tag, err = tx.Exec(ctx, `INSERT INTO team_membership (team_id, user_id) VALUES ($1, $2)
				ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, userID)
		} else {
			tag, err = tx.Exec(ctx, `DELETE FROM team_membership WHERE team_id = $1 AND user_id = $2`, teamID, userID)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return s.recordTeamChange(ctx, tx, actor, teamID, &userID, change,
			map[string]any{"member": userID, "on": !on}, map[string]any{"member": userID, "on": on})
	})
}

// recordTeamChange is the write shape's second half for every team change:
// the audit row on the team and team.changed on the identity stream.
func (s *Service) recordTeamChange(ctx context.Context, tx pgx.Tx, actor Identity, teamID ids.UUID, userID *ids.UUID, change string, before, after map[string]any) error {
	action := "update"
	switch change {
	case "created":
		action = "create"
	case "archived":
		action = "archive"
	case "restored":
		action = "restore"
	}
	auditID, err := storekit.Audit(ctx, tx, action, "team", teamID, before, after)
	if err != nil {
		return err
	}
	payload := crmcontracts.PublicEventTeamChanged{
		TeamId: openapi_types.UUID(teamID),
		Change: crmcontracts.PublicEventTeamChangedChange(change),
		By:     openapi_types.UUID(actor.UserID.UUID),
	}
	if userID != nil {
		u := openapi_types.UUID(*userID)
		payload.UserId = &u
	}
	return storekit.EmitEvent(ctx, tx, auditID, teamID, payload)
}

// validTeamName trims and bounds a team name.
func validTeamName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxTeamName {
		return "", &values.ParseError{Field: "name", Code: "invalid_team_name",
			Message: fmt.Sprintf("a team name is 1 to %d characters", maxTeamName)}
	}
	return name, nil
}
