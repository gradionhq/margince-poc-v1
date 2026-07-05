// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The activity lifecycle beyond capture: update (completing a task is
// the everyday case), archive (visibility change — the 🟡 floor on the
// agent surface), and relink (moving a captured email onto the right
// deal WITHOUT touching its provenance — an association event, not a
// re-capture).

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type UpdateActivityInput struct {
	Subject    *string
	Body       *string
	OccurredAt *time.Time
	DueAt      *time.Time
	AssigneeID *ids.UUID
	IsDone     *bool
	IfVersion  *int64
}

func (s *Store) UpdateActivity(ctx context.Context, id ids.UUID, in UpdateActivityInput) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readActivity(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if in.IfVersion != nil && current.Version != nil && int64(*current.Version) != *in.IfVersion {
			return apperrors.ErrVersionSkew
		}
		if in.AssigneeID != nil {
			// A client-supplied user reference is still a reference; the
			// FK checks existence, RLS the tenancy.
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND status = 'active' AND archived_at IS NULL)`,
				*in.AssigneeID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return apperrors.ErrNotFound
			}
		}
		// done_at travels WITH is_done (the activity_done_at CHECK):
		// completion stamps the moment, reopening clears it.
		if _, err := tx.Exec(ctx, `
			UPDATE activity SET
			  subject = coalesce($2, subject),
			  body = coalesce($3, body),
			  occurred_at = coalesce($4, occurred_at),
			  due_at = coalesce($5, due_at),
			  assignee_id = coalesce($6, assignee_id),
			  is_done = coalesce($7, is_done),
			  done_at = CASE
			    WHEN $7 IS TRUE AND NOT is_done THEN now()
			    WHEN $7 IS FALSE THEN NULL
			    ELSE done_at END
			WHERE id = $1`,
			id, in.Subject, in.Body, in.OccurredAt, in.DueAt, in.AssigneeID, in.IsDone); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "activity", id, nil, updateDelta(in))
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "activity.updated", "activity", id, map[string]any{
			"delta": updateDelta(in),
		}); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, err
}

func (s *Store) ArchiveActivity(ctx context.Context, id ids.UUID) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionDelete); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readActivity(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE activity SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		auditID, err := storekit.Audit(ctx, tx, "archive", "activity", id, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "activity.archived", "activity", id, nil); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

type RelinkActivityInput struct {
	EntityType            string
	EntityID              ids.UUID
	ReplaceExistingOfType bool
}

func (s *Store) RelinkActivity(ctx context.Context, id ids.UUID, in RelinkActivityInput) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return crmcontracts.Activity{}, err
	}
	column, ok := map[string]string{
		"person": "person_id", "organization": "organization_id", "deal": "deal_id",
	}[in.EntityType]
	if !ok {
		// The contract admits 'lead' here, but the activity_link schema
		// carries no lead column (filed as feedback) — refused honestly.
		return crmcontracts.Activity{}, &InvalidLinkTypeError{EntityType: in.EntityType}
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readActivity(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		// The relink target is a client-supplied reference (H1).
		if err := auth.EnsureLinkTarget(ctx, tx, in.EntityType, in.EntityID); err != nil {
			return err
		}
		if in.ReplaceExistingOfType {
			if _, err := tx.Exec(ctx,
				`DELETE FROM activity_link WHERE activity_id = $1 AND entity_type = $2`,
				id, in.EntityType); err != nil {
				return err
			}
		}
		// Idempotent: replaying the same association is a no-op, and a
		// no-op writes no audit noise.
		tag, err := tx.Exec(ctx, storekit.SQLf(`
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, %s)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
			ON CONFLICT (activity_id, entity_type, coalesce(person_id, organization_id, deal_id)) DO NOTHING`, column),
			id, in.EntityType, in.EntityID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			auditID, err := storekit.Audit(ctx, tx, "activity_relink", "activity", id, nil, map[string]any{
				"entity_type": in.EntityType, "entity_id": in.EntityID, "replaced": in.ReplaceExistingOfType,
			})
			if err != nil {
				return err
			}
			if err := storekit.Emit(ctx, tx, auditID, "activity.updated", "activity", id, map[string]any{
				"delta": map[string]any{"relinked": map[string]any{"entity_type": in.EntityType, "entity_id": in.EntityID}},
			}); err != nil {
				return err
			}
		}
		var err2 error
		out, err2 = readActivity(ctx, tx, id, storekit.LiveOnly)
		return err2
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Activity{}, apperrors.ErrNotFound
	}
	return out, err
}

func updateDelta(in UpdateActivityInput) map[string]any {
	delta := map[string]any{}
	if in.Subject != nil {
		delta["subject"] = *in.Subject
	}
	if in.Body != nil {
		delta["body"] = true // presence, not content — bodies can be large
	}
	if in.OccurredAt != nil {
		delta["occurred_at"] = *in.OccurredAt
	}
	if in.DueAt != nil {
		delta["due_at"] = *in.DueAt
	}
	if in.AssigneeID != nil {
		delta["assignee_id"] = *in.AssigneeID
	}
	if in.IsDone != nil {
		delta["is_done"] = *in.IsDone
	}
	return delta
}
