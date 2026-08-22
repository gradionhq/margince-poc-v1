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
		// Reading the row is not the licence to change it: customer identity
		// is workspace-readable, so the write arm is what keeps a colleague's
		// correspondence theirs.
		if err := auth.EnsureActivityWritable(ctx, tx, id.UUID); err != nil {
			return err
		}
		current, err := readActivity(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if in.IfVersion != nil && current.Version != nil && int64(*current.Version) != *in.IfVersion {
			return apperrors.ErrVersionSkew
		}
		if err := renormalizeTranscriptPatch(current, &in); err != nil {
			return err
		}
		if err := ensureAssigneeExists(ctx, tx, in.AssigneeID); err != nil {
			return err
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

// renormalizeTranscriptPatch re-runs ADR-0058's normalizer on a body PATCH
// when the target row is transcript-marked. A transcript's normalized form
// is only ever produced on ingest (LogActivityInputFrom) — without this, a
// PATCH could leave a transcript-marked row holding un-normalized text (raw
// CRLFs, trailing whitespace), which is exactly the row the
// activity/transcript retention selector and any future line citation both
// assume is already canonical.
func renormalizeTranscriptPatch(current crmcontracts.Activity, in *UpdateActivityInput) error {
	if in.Body == nil || current.SourceSystem == nil || *current.SourceSystem != transcriptSourceSystem {
		return nil
	}
	normalized, err := normalizeTranscript(*in.Body)
	if err != nil {
		return err
	}
	in.Body = &normalized
	return nil
}

// ensureAssigneeExists checks a client-supplied user reference before it
// lands: the FK checks existence, RLS the tenancy. Nil means the patch
// doesn't touch the assignee, which is not this function's to gate.
func ensureAssigneeExists(ctx context.Context, tx pgx.Tx, assigneeID *ids.UserID) error {
	if assigneeID == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND status = 'active' AND archived_at IS NULL)`,
		*assigneeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return apperrors.ErrNotFound
	}
	return nil
}

// RefuseArchiveActivity answers every authority refusal ArchiveActivity would
// answer with, and writes nothing.
//
// It exists so a confirm-first archive is refused BEFORE a human is asked
// rather than after they have answered: the two probes below are the whole of
// what the archive requires of a caller, and asking them here is what keeps a
// staged approval from being spent on a call the store was always going to
// refuse. Deliberately NO version probe — a version that is right at staging
// can be wrong by the time the human answers, so the pin is the write's
// business and never a reason to refuse a staging.
func (s *Store) RefuseArchiveActivity(ctx context.Context, id ids.ActivityID) error {
	if err := auth.Require(ctx, "activity", principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return auth.EnsureActivityWritable(ctx, tx, id.UUID)
	})
}

// ArchiveActivity retires one activity, conditioned on ifVersion wherever the
// caller's authority named a version.
//
// The write rides storekit's guarded patch rather than a bare UPDATE, for the
// reason ApplyGuarded's own doc gives — *an unguarded update is not
// expressible* — which the archive verb was quietly the exception to. With a
// pin it is the optimistic CAS, so an archive a human released against version
// 4 lands on version 4 or answers skew; without one it takes the row lock, so
// it is never LESS guarded than the bare statement it replaces. The gone and
// already-archived cases keep answering ErrNotFound, which is the same
// existence-hiding answer as before.
func (s *Store) ArchiveActivity(ctx context.Context, id ids.ActivityID, ifVersion *int64) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionDelete); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureActivityWritable(ctx, tx, id.UUID); err != nil {
			return err
		}
		p := storekit.NewPatch()
		p.Set("archived_at", nil, time.Now().UTC())
		if err := p.ApplyGuarded(ctx, tx, "activity", id.UUID, ifVersion); err != nil {
			return err
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
	column, err := admitRelink(ctx, in)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := relinkAdmittedRow(ctx, tx, id, in, column); err != nil {
			return err
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

// RelinkActivityInTx is the single relink on the CALLER's transaction: the
// same admission, the same target probe and the same guarded row write as
// RelinkActivity, for a caller that must commit the link together with a
// record of its own — the confirm of a project attribution candidate closes
// the candidate in the transaction that writes the link, so neither can be
// read without the other. It answers whether a link was written; replaying an
// association the activity already carries is a no-op.
func RelinkActivityInTx(ctx context.Context, tx pgx.Tx, id ids.ActivityID, in RelinkActivityInput) (bool, error) {
	column, err := admitRelink(ctx, in)
	if err != nil {
		return false, err
	}
	return relinkAdmittedRow(ctx, tx, id, in, column)
}

// LockActivityLive takes the row lock on one live activity for the rest of the
// caller's transaction, for a caller that must read something about the
// activity and then relink it as ONE state — the attribution confirm reads
// where the message is filed before filing it. The lock belongs to this
// module because the row does; a gone or archived activity answers the same
// not-found every live read gives.
func LockActivityLive(ctx context.Context, tx pgx.Tx, id ids.ActivityID) error {
	_, err := storekit.LockRow(ctx, tx, "activity", id.UUID, storekit.LiveOnly)
	return err
}

// relinkAdmittedRow is the transactional half both doors share: the target
// probe, then the guarded row write. The admission stays outside it so the
// single door can refuse a malformed request before it opens a transaction.
func relinkAdmittedRow(ctx context.Context, tx pgx.Tx, id ids.ActivityID, in RelinkActivityInput, column string) (bool, error) {
	// The relink target is a client-supplied reference (H1).
	if err := auth.EnsureLinkTarget(ctx, tx, in.EntityType, in.EntityID); err != nil {
		return false, err
	}
	return relinkActivityRow(ctx, tx, id, in, column)
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
// A link outside the caller's scope survives instead, and for `project` that
// used to leave a residual: at most one project link may exist, so the insert
// then hit the partial index and refused, and the difference between that
// refusal and a success told the caller a project link they could not see was
// there. One bit escaped, and hiding a link's existence while enforcing
// one-per-activity looked like the same question asked twice.
//
// It is closed, and closed on both halves rather than narrowed. A project
// carries no own/team arm (platform/auth tableclass.go) and no capture privacy
// either — migration 1787320003 narrowed its visibility CHECK to 'workspace',
// because nothing auto-creates a project and an owner-private one was a state
// no writer could reach. So no project link can be invisible to a caller
// holding the object grant: the delete reaches every one, and the move
// succeeds. The 23505 path below still stands for the caller who asks to
// associate rather than move.
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
		delta[fieldSubject] = *in.Subject
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
