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
	openapi_types "github.com/oapi-codegen/runtime/types"

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
	RemindAt   *time.Time
	AssigneeID *ids.UserID
	IsDone     *bool
	IfVersion  *int64
}

func (s *Store) UpdateActivity(ctx context.Context, id ids.ActivityID, in UpdateActivityInput) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The row lock makes the version compare and the coalesce update
		// below one race-free unit: without it two concurrent edits both
		// pass the compare and the loser silently overwrites the winner.
		if _, err := storekit.LockRow(ctx, tx, "activity", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
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
			  remind_at = coalesce($6, remind_at),
			  assignee_id = coalesce($7, assignee_id),
			  is_done = coalesce($8, is_done),
			  done_at = CASE
			    WHEN $8 IS TRUE AND NOT is_done THEN now()
			    WHEN $8 IS FALSE THEN NULL
			    ELSE done_at END
			WHERE id = $1`,
			id, in.Subject, in.Body, in.OccurredAt, in.DueAt, in.RemindAt, in.AssigneeID, in.IsDone); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "activity", id.UUID, nil, updateDelta(in))
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventActivityUpdated{
			ChangedFields: activityUpdatedChangedFields(in),
		}); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, err
}

func (s *Store) ArchiveActivity(ctx context.Context, id ids.ActivityID) (crmcontracts.Activity, error) {
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
		auditID, err := storekit.Audit(ctx, tx, "archive", "activity", id.UUID, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventActivityArchived{}); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

type RelinkActivityInput struct {
	EntityType string
	// note: the relink target is polymorphic (any entity kind, re-admitted
	// against the kind vocabulary below), so the id stays untyped (rule 6).
	EntityID              ids.UUID
	ReplaceExistingOfType bool
}

func (s *Store) RelinkActivity(ctx context.Context, id ids.ActivityID, in RelinkActivityInput) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return crmcontracts.Activity{}, err
	}
	column := linkColumn(in.EntityType)
	if column == "" {
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
			// Replace only the links this caller can SEE. An activity's own
			// visibility derives from its links, so an unscoped delete lets
			// someone who reached this activity through one link cut another —
			// dropping a team's sight of a record by rewriting an association
			// they were never shown.
			//
			// A link outside the caller's scope survives instead. For `project`
			// that leaves a residual worth naming rather than glossing: at most
			// one project link may exist, so the insert then hits the partial
			// index and refuses, and the difference between that refusal and a
			// success tells the caller a project link they cannot see is there.
			// One bit escapes, and it cannot be closed from here — hiding the
			// link's existence and enforcing one-per-activity are the same
			// question asked twice. Its CONTENT stays hidden, which is what the
			// scope is for, and losing the link outright would be worse.
			var args []any
			arg := func(v any) int { args = append(args, v); return len(args) }
			idPos, typePos := arg(id), arg(in.EntityType)
			scope, err := auth.ScopeClauseFor(ctx, in.EntityType, "t", arg)
			if err != nil {
				return err
			}
			visible := "true"
			if scope != "" {
				visible = scope
			}
			if _, err := tx.Exec(ctx, storekit.SQLf(`
				DELETE FROM activity_link
				WHERE activity_id = $%d AND entity_type = $%d
				  AND EXISTS (SELECT 1 FROM %s t WHERE t.id = activity_link.%s AND %s)`,
				idPos, typePos, in.EntityType, column, visible), args...); err != nil {
				return err
			}
		}
		// A relink to a PERSON is a human saying "this conversation was
		// actually with someone else", so the participant row that names the
		// old contact is now wrong (ACT-DDL-3). Repointing it here keeps the
		// participants and the links telling the same story; leaving it would
		// credit the old contact — and whichever colleague spoke to them —
		// with a conversation the correction just moved away, permanently,
		// because nothing else revisits that row.
		//
		// Only when the relink REPLACED the previous person link. An
		// additional link means the activity concerns both, and both were in
		// it as far as anything here knows.
		if in.EntityType == linkEntityPerson && in.ReplaceExistingOfType {
			if _, err := tx.Exec(ctx, `
				UPDATE activity_participant
				   SET person_id = $2
				 WHERE activity_id = $1 AND person_id IS NOT NULL
				   AND NOT EXISTS (
				       SELECT 1 FROM activity_participant other
				        WHERE other.activity_id = $1 AND other.person_id = $2)`,
				id, in.EntityID); err != nil {
				return err
			}
		}
		// Idempotent: replaying the same association is a no-op, and a
		// no-op writes no audit noise.
		tag, err := tx.Exec(ctx, storekit.SQLf(`
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, %s)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
			ON CONFLICT (activity_id, entity_type, `+linkIDCoalesce+`) DO NOTHING`, column),
			id, in.EntityType, in.EntityID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			auditID, err := storekit.Audit(ctx, tx, "activity_relink", "activity", id.UUID, nil, map[string]any{
				"entity_type": in.EntityType, "entity_id": in.EntityID, "replaced": in.ReplaceExistingOfType,
			})
			if err != nil {
				return err
			}
			if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventActivityUpdated{
				ChangedFields: relinkedChangedFields(in.EntityType, in.EntityID),
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
	if in.RemindAt != nil {
		delta["remind_at"] = *in.RemindAt
	}
	if in.AssigneeID != nil {
		delta["assignee_id"] = *in.AssigneeID
	}
	if in.IsDone != nil {
		delta["is_done"] = *in.IsDone
	}
	return delta
}

// activityUpdatedChangedFields is UpdateActivity's typed sibling of
// updateDelta (which still feeds the audit_log row unchanged): the same
// touched/untouched decisions, projected onto activity.updated's BOUNDED
// changed_fields struct rather than an open map. body carries a presence
// flag, never the content — bodies can be large and are never echoed onto
// the wire.
func activityUpdatedChangedFields(in UpdateActivityInput) crmcontracts.PublicEventActivityChangedFields {
	var fields crmcontracts.PublicEventActivityChangedFields
	if in.Subject != nil {
		fields.Subject = in.Subject
	}
	if in.Body != nil {
		bodyTouched := true
		fields.Body = &bodyTouched
	}
	if in.OccurredAt != nil {
		fields.OccurredAt = in.OccurredAt
	}
	if in.DueAt != nil {
		fields.DueAt = in.DueAt
	}
	if in.RemindAt != nil {
		fields.RemindAt = in.RemindAt
	}
	if in.AssigneeID != nil {
		assignee := openapi_types.UUID(in.AssigneeID.UUID)
		fields.AssigneeId = &assignee
	}
	if in.IsDone != nil {
		fields.IsDone = in.IsDone
	}
	return fields
}

// relinkedChangedFields is RelinkActivity's activity.updated builder: the
// relink is an association change, not a field patch, so changed_fields
// carries only the typed relinked target.
func relinkedChangedFields(entityType string, entityID ids.UUID) crmcontracts.PublicEventActivityChangedFields {
	return crmcontracts.PublicEventActivityChangedFields{
		Relinked: &crmcontracts.PublicEventActivityRelinkedRef{
			EntityType: entityType,
			EntityId:   openapi_types.UUID(entityID),
		},
	}
}
