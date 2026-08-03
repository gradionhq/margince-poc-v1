// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project partial-update path. `phase` is deliberately absent from the
// input: a transition moves through AdvanceProjectPhase so the row change
// and its history row are written from one transaction, and the move emits
// project.phase_changed rather than a diff a consumer has to interpret.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// UpdateProjectInput is one project partial update: every field optional.
type UpdateProjectInput struct {
	Name          *string
	Key           *string
	OwnerID       *ids.UserID
	Description   *string
	StartedAt     *time.Time
	TargetEndDate *time.Time
	EndedAt       *time.Time
	IfVersion     *int64
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (storekit customcolumns).
	CustomFields map[string]any
}

// UpdateProject applies a partial update. It cannot move the phase: that
// would write a history row, and only AdvanceProjectPhase does.
func (s *Store) UpdateProject(ctx context.Context, id ids.ProjectID, in UpdateProjectInput) (crmcontracts.Project, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Project{}, err
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return crmcontracts.Project{}, err
	}
	var out crmcontracts.Project
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, projectObject, id.UUID); err != nil {
			return err
		}
		// current reads WITH active columns so the patch's audit
		// before-image carries the honest pre-update cf values.
		current, err := readProject(ctx, tx, id, storekit.LiveOnly, active)
		if err != nil {
			return fmt.Errorf("read project before update: %w", err)
		}

		if err := ensureRenamedKeyFree(ctx, tx, current, in); err != nil {
			return err
		}

		p := projectUpdatePatch(current, in)
		storekit.SetCustomFieldPatch(p, active, in.CustomFields, current.AdditionalProperties)
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, projectObject, id.UUID, in.IfVersion); err != nil {
			if conflict := projectKeyConflict(err, in.Key); conflict != nil {
				return conflict
			}
			if constraint, ok := storekit.CheckViolation(err); ok {
				return projectCheckError(constraint, submittedDateField(in.StartedAt, in.TargetEndDate, in.EndedAt))
			}
			return fmt.Errorf("apply project patch: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "update", projectObject, id.UUID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit project update: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventProjectUpdated{
			ChangedFields: p.After(),
		}); err != nil {
			return fmt.Errorf("emit project.updated: %w", err)
		}
		if out, err = readProject(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read updated project: %w", err)
		}
		return nil
	})
	return out, err
}

// ensureRenamedKeyFree runs the collision pre-check only when the update
// actually moves the key. Re-sending the key a project already holds is not
// a rename, and treating it as one would have every no-op PATCH conflict
// with the project itself.
func ensureRenamedKeyFree(ctx context.Context, tx pgx.Tx, current crmcontracts.Project, in UpdateProjectInput) error {
	if in.Key == nil {
		return nil
	}
	if current.Key != nil && strings.EqualFold(*current.Key, *in.Key) {
		return nil
	}
	return ensureProjectKeyFree(ctx, tx, in.Key)
}

// projectUpdatePatch builds the column patch. Every FK it can set points
// at app_user, which carries no row scope of its own — any workspace
// member may own a project — so the composite FK is the whole check; the
// anchor company is not re-pointable here, because moving a project to
// another company would silently orphan the deals that inherited it.
func projectUpdatePatch(current crmcontracts.Project, in UpdateProjectInput) *storekit.Patch {
	p := storekit.NewPatch()
	if in.Name != nil {
		p.Set("name", current.Name, *in.Name)
	}
	if in.Key != nil {
		p.Set("key", current.Key, *in.Key)
	}
	if in.OwnerID != nil {
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	if in.Description != nil {
		p.Set("description", current.Description, *in.Description)
	}
	if in.StartedAt != nil {
		p.Set("started_at", current.StartedAt, *in.StartedAt)
	}
	if in.TargetEndDate != nil {
		p.Set("target_end_date", current.TargetEndDate, *in.TargetEndDate)
	}
	if in.EndedAt != nil {
		p.Set("ended_at", current.EndedAt, *in.EndedAt)
	}
	return p
}
