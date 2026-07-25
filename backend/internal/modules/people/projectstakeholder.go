// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The project-stakeholder store paths. Both are expressed on top of the
// generic relationship writes so the edge's rules — endpoint visibility,
// the anchor's write grant, the audit+outbox shape — have exactly one
// implementation rather than a second one that drifts.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// projectStakeholderSource is the provenance of an edge created through
// the project surface: a human attached it, as opposed to an import or a
// capture run.
const projectStakeholderSource = "ui"

// SetProjectStakeholderInput is one idempotent attach: the same person on
// the same project twice is a role correction, not a duplicate.
type SetProjectStakeholderInput struct {
	ProjectID ids.ProjectID
	PersonID  ids.PersonID
	Role      string
}

// SetProjectStakeholder attaches a person to a project, or re-roles the
// edge that already exists. The uniqueness index (uq_rel_project_stakeholder)
// is what makes "already attached" detectable rather than duplicated; the
// lookup runs inside the same transaction as the write, so a concurrent
// attach loses on the index rather than on a stale read.
func (s *Store) SetProjectStakeholder(ctx context.Context, in SetProjectStakeholderInput) (relationshipRow, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionCreate); err != nil {
		return relationshipRow{}, err
	}
	// The edge annotates its anchor: without the project's write grant, an
	// edge would be an RBAC side door onto it.
	if err := auth.Require(ctx, "project", principal.ActionUpdate); err != nil {
		return relationshipRow{}, err
	}

	var existingID *ids.UUID
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var found ids.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM relationship
			WHERE kind = $1 AND project_id = $2 AND person_id = $3 AND archived_at IS NULL`,
			projectStakeholderKind, in.ProjectID, in.PersonID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up existing project stakeholder: %w", err)
		}
		existingID = &found
		return nil
	})
	if err != nil {
		return relationshipRow{}, err
	}

	if existingID != nil {
		return s.UpdateRelationship(ctx, *existingID, UpdateRelationshipInput{Role: &in.Role})
	}
	return s.CreateRelationship(ctx, CreateRelationshipInput{
		Kind:      projectStakeholderKind,
		PersonID:  &in.PersonID,
		ProjectID: &in.ProjectID,
		Role:      &in.Role,
		// The edge is created by a human acting on the project surface;
		// captured_by is stamped from the principal by the write shape.
		Source: projectStakeholderSource,
	})
}

// RemoveProjectStakeholder archives the edge between a project and a
// person. Detaching someone is not deleting them: the edge is archived so
// the record of their involvement survives the change.
func (s *Store) RemoveProjectStakeholder(ctx context.Context, projectID ids.ProjectID, personID ids.PersonID) error {
	if err := auth.Require(ctx, "relationship", principal.ActionDelete); err != nil {
		return err
	}
	var edgeID ids.UUID
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The visibility of the edge itself is re-checked by
		// ArchiveRelationship; this read only resolves which edge is meant.
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		kindPos, projectPos, personPos := arg(projectStakeholderKind), arg(projectID), arg(personID)
		scope, err := relationshipEndpointScope(ctx, "r", arg)
		if err != nil {
			return err
		}
		sql := storekit.SQLf(`
			SELECT r.id FROM relationship r
			WHERE r.kind = $%d AND r.project_id = $%d AND r.person_id = $%d AND r.archived_at IS NULL`,
			kindPos, projectPos, personPos)
		if scope != "" {
			sql += " AND " + scope
		}
		if err := tx.QueryRow(ctx, sql, args...).Scan(&edgeID); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("resolve project stakeholder edge: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, err = s.ArchiveRelationship(ctx, edgeID)
	return err
}
