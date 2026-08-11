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
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
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
// is what makes "already attached" detectable rather than duplicated.
//
// The lookup and the write are separate gated store calls, so two concurrent
// attaches can both read "no edge" and both try to create one. The index
// settles that, and the loser treats its own conflict as the answer it was
// looking for: re-read the edge that won and correct its role. Returning the
// raw uniqueness error instead would make an idempotent PUT fail purely on
// timing.
func (s *Store) SetProjectStakeholder(ctx context.Context, in SetProjectStakeholderInput) (relationshipRow, error) {
	// A stakeholder seat names a PERSON; without one there is no seat. Required
	// by the contract, and true only here: the zero UUID would reach the edge
	// lookup and answer not-found for a person the caller never named.
	if err := httperr.RequireBodyID("person_id", in.PersonID.UUID); err != nil {
		return relationshipRow{}, err
	}
	if err := auth.Require(ctx, "relationship", principal.ActionCreate); err != nil {
		return relationshipRow{}, err
	}
	// The edge annotates its anchor: without the project's write grant, an
	// edge would be an RBAC side door onto it.
	if err := auth.Require(ctx, projectObjectName, principal.ActionUpdate); err != nil {
		return relationshipRow{}, err
	}

	existingID, found, err := s.projectStakeholderEdge(ctx, in.ProjectID, in.PersonID)
	if err != nil {
		return relationshipRow{}, err
	}
	if found {
		return s.UpdateRelationship(ctx, existingID, UpdateRelationshipInput{Role: &in.Role})
	}
	row, err := s.CreateRelationship(ctx, CreateRelationshipInput{
		Kind:      ProjectStakeholderKind,
		PersonID:  &in.PersonID,
		ProjectID: &in.ProjectID,
		Role:      &in.Role,
		// The edge is created by a human acting on the project surface;
		// captured_by is stamped from the principal by the write shape.
		Source: projectStakeholderSource,
	})
	var conflict *RelationshipConflictError
	if errors.As(err, &conflict) && conflict.Constraint == projectStakeholderUnique {
		// Someone attached the same person between our read and our write.
		// That is the state this call wanted, so adopt their edge and apply
		// the role rather than failing a request that asked for exactly this.
		winnerID, stillThere, lookupErr := s.projectStakeholderEdge(ctx, in.ProjectID, in.PersonID)
		if lookupErr != nil {
			return relationshipRow{}, lookupErr
		}
		if !stillThere {
			// The winner was archived in the instant between its insert and
			// this read. The roster is empty again, so the original request
			// is simply true once more: create it.
			return s.CreateRelationship(ctx, CreateRelationshipInput{
				Kind: ProjectStakeholderKind, PersonID: &in.PersonID,
				ProjectID: &in.ProjectID, Role: &in.Role, Source: projectStakeholderSource,
			})
		}
		return s.UpdateRelationship(ctx, winnerID, UpdateRelationshipInput{Role: &in.Role})
	}
	return row, err
}

// projectStakeholderEdge resolves the live edge between a project and a
// person, reporting separately whether there is one. Both the first lookup
// and the uniqueness-race re-read go through here, so the two cannot drift
// apart on what "the edge" means.
//
// Absent is not an error: on the way in it means "attach", and on the race
// path it means the winner was archived under us. Only a broken read is.
func (s *Store) projectStakeholderEdge(ctx context.Context, projectID ids.ProjectID, personID ids.PersonID) (ids.UUID, bool, error) {
	var edge ids.UUID
	found := false
	err := s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id FROM relationship
			WHERE kind = $1 AND project_id = $2 AND person_id = $3 AND archived_at IS NULL`,
			ProjectStakeholderKind, projectID, personID).Scan(&edge)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up project stakeholder edge: %w", err)
		}
		found = true
		return nil
	})
	return edge, found, err
}

// RemoveProjectStakeholder archives the edge between a project and a
// person. Detaching someone is not deleting them: the edge is archived so
// the record of their involvement survives the change.
func (s *Store) RemoveProjectStakeholder(ctx context.Context, projectID ids.ProjectID, personID ids.PersonID) error {
	if err := auth.Require(ctx, "relationship", principal.ActionDelete); err != nil {
		return err
	}
	// The same anchor grant attach demands: detaching a stakeholder changes
	// who the project says is involved, so a principal that cannot write the
	// project cannot rewrite its roster from the edge side either.
	if err := auth.Require(ctx, projectObjectName, principal.ActionUpdate); err != nil {
		return err
	}
	var edgeID ids.UUID
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The visibility of the edge itself is re-checked by
		// ArchiveRelationship; this read only resolves which edge is meant.
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		kindPos, projectPos, personPos := arg(ProjectStakeholderKind), arg(projectID), arg(personID)
		scope, err := auth.RelationshipEndpointScope(ctx, "r", arg)
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

// PersonNames names the people in a set, under the caller's own row scope.
//
// WHY IT EXISTS AT ALL. A stakeholder seat is an edge, and an edge answers ids:
// "who to call at the client" served as a UUID is a question restated rather
// than answered. Every other nested list on the surfaces that read this one
// carries a display name beside its id, and the seat was the one that did not.
//
// WHY IT IS SAFE, and why it is gated ANYWAY. Its only caller reads the seats
// through ListRelationships, whose endpoint conjunction already required the
// PERSON end to be visible — so every id reaching here belongs to a record the
// caller may read. The gate below is therefore not what makes the current call
// safe; it is what keeps the NEXT caller safe, since a name read that trusted
// its argument would be a side door onto any id somebody could guess.
//
// A person the caller cannot see is simply absent from the answer rather than
// erroring: this names what it can, and the caller renders the id for the rest.
func (s *Store) PersonNames(ctx context.Context, people []ids.PersonID) (map[ids.UUID]string, error) {
	if err := auth.Require(ctx, entityPerson, principal.ActionRead); err != nil {
		return nil, err
	}
	names := map[ids.UUID]string{}
	if len(people) == 0 {
		return names, nil
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		args := []any{people}
		arg := func(v any) int { args = append(args, v); return len(args) }
		scope, err := auth.ScopeClauseFor(ctx, entityPerson, "p", arg)
		if err != nil {
			return err
		}
		q := `SELECT p.id, p.full_name FROM person p
			WHERE p.id = ANY($1) AND p.archived_at IS NULL`
		if scope != "" {
			q += " AND " + scope
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			names[id] = name
		}
		return rows.Err()
	})
	return names, err
}
