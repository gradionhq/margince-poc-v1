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
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
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
	// Relinking moves an activity ONTO a record; without an entity_id there is
	// nowhere to move it. Required by the contract, and true only here: the zero
	// UUID reaches the link-target gate and answers not-found for a record the
	// caller never named.
	if err := httperr.RequireBodyID("entity_id", in.EntityID); err != nil {
		return crmcontracts.Activity{}, err
	}
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
		var displaced []ids.UUID
		if in.ReplaceExistingOfType {
			var err error
			displaced, err = deleteVisibleLinksOfType(ctx, tx, id, in.EntityType, column)
			if err != nil {
				return err
			}
		}
		if in.EntityType == linkEntityPerson && in.ReplaceExistingOfType && len(displaced) > 0 {
			if err := repointDisplacedParticipants(ctx, tx, id, in.EntityID, displaced); err != nil {
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
			// Touch the activity ROW itself, not just its link table: a
			// staged approval pins activity.version (versionTables includes
			// objectActivity), and that pin is the only thing standing
			// between an approved "send this body on this conversation" and
			// the conversation being silently repointed to someone else
			// before the approval is redeemed (F-001). Without this, a
			// relink that changes who the activity reaches never moves the
			// version the pin re-checks, so the stale approval keeps
			// redeeming as if nothing had changed. The trigger
			// (set_updated_at_bump_version, 0008_activity.up.sql) does the
			// actual bump; this only has to be a genuine UPDATE of the row.
			if _, err := tx.Exec(ctx, `UPDATE activity SET updated_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
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

// deleteVisibleLinksOfType drops the activity's links of one entity type and
// answers the person ids that delete actually displaced. Those ids come from
// the delete ITSELF. Inferring them instead — "whoever is a participant but no
// longer linked" — sweeps up participants that were never linked in the first
// place, and repoints conversations the correction never mentioned.
//
// Only the links this caller can SEE are replaced. An activity's own
// visibility derives from its links, so an unscoped delete lets someone who
// reached this activity through one link cut another — dropping a team's sight
// of a record by rewriting an association they were never shown.
//
// A link outside the caller's scope survives instead. For `project` that
// leaves a residual worth naming rather than glossing: at most one project
// link may exist, so the insert then hits the partial index and refuses, and
// the difference between that refusal and a success tells the caller a project
// link they cannot see is there. One bit escapes, and it cannot be closed from
// here — hiding the link's existence and enforcing one-per-activity are the
// same question asked twice. Its CONTENT stays hidden, which is what the scope
// is for, and losing the link outright would be worse.
func deleteVisibleLinksOfType(ctx context.Context, tx pgx.Tx, id ids.ActivityID, entityType, column string) ([]ids.UUID, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos, typePos := arg(id), arg(entityType)
	scope, err := auth.ScopeClauseFor(ctx, entityType, "t", arg)
	if err != nil {
		return nil, err
	}
	visible := "true"
	if scope != "" {
		visible = scope
	}
	rows, err := tx.Query(ctx, storekit.SQLf(`
		DELETE FROM activity_link
		WHERE activity_id = $%d AND entity_type = $%d
		  AND EXISTS (SELECT 1 FROM %s t WHERE t.id = activity_link.%s AND %s)
		RETURNING person_id`,
		idPos, typePos, entityType, column, visible), args...)
	if err != nil {
		return nil, err
	}
	// Every id this delete actually removed. A link row of another
	// entity type returns NULL here and contributes nothing.
	displaced, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ids.UUID, error) {
		var pid *ids.UUID
		if err := r.Scan(&pid); err != nil {
			return ids.Nil, err
		}
		if pid == nil {
			return ids.Nil, nil
		}
		return *pid, nil
	})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(displaced, func(personID ids.UUID) bool { return personID == ids.Nil }), nil
}

// repointDisplacedParticipants moves the displaced contacts' participant rows
// onto the relink target. A relink to a PERSON is a human saying "this
// conversation was actually with someone else", so the participant row naming
// the old contact is now wrong (ACT-DDL-3). Repointing it keeps the
// participants and the links telling one story.
//
// The DISPLACED person carries the row scope too. The relink already gated the
// new target; without this the old one is rewritten sight unseen, so a caller
// could repoint a participant naming a contact they cannot read — including an
// owner-private captured one. The link delete scopes for the same reason; this
// is its participant twin.
//
// KNOWN GAP, stated rather than papered over: the graph consumer derives its
// affected (user, person) pairs from the participant rows, and by the time it
// runs they name the NEW contact — so the OLD edge is not recomputed and keeps
// counting an interaction that no longer points at it. The nightly rebuild
// clears it, which bounds the staleness to the same 24h the window counts
// already carry, but it is a bound and not a fix.
//
// The fix is the additive `relinked_from` reference ADR-0078 specifies on the
// activity.updated relink payload: the consumer needs the displaced id, and
// this module cannot recompute the edge itself because search is a sibling.
// That is a public-event contract change and belongs in its own slice.
func repointDisplacedParticipants(ctx context.Context, tx pgx.Tx, id ids.ActivityID, target ids.UUID, displaced []ids.UUID) error {
	var pargs []any
	parg := func(v any) int { pargs = append(pargs, v); return len(pargs) }
	idPos, targetPos, displacedPos := parg(id), parg(target), parg(displaced)
	visible, err := auth.ScopeClauseFor(ctx, linkEntityPerson, "op", parg)
	if err != nil {
		return err
	}
	if visible == "" {
		// An unbounded caller narrows nothing.
		visible = "true"
	}
	if _, err := tx.Exec(ctx, storekit.SQLf(`
		UPDATE activity_participant ap
		   SET person_id = $%d
		 WHERE ap.activity_id = $%d
		   -- Exactly the people the link delete removed, and no
		   -- others. A participant can name somebody who was never
		   -- linked at all, and inferring the displaced set from "no
		   -- longer linked" would rewrite them too.
		   AND ap.person_id = ANY($%d::uuid[])
		   AND ap.person_id <> $%d
		   AND EXISTS (SELECT 1 FROM person op WHERE op.id = ap.person_id AND (`+visible+`))
		   AND NOT EXISTS (
		       SELECT 1 FROM activity_participant other
		        WHERE other.activity_id = ap.activity_id AND other.person_id = $%d)`,
		targetPos, idPos, displacedPos, targetPos, targetPos), pargs...); err != nil {
		return err
	}
	return nil
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
