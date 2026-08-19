// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Who may read one activity's content. The row scope decides who may learn an
// activity EXISTS (the link walk, platform/auth); the audience set here decides
// who reads what was said. It is per message by design — limiting one email
// says nothing about its thread siblings or the people on it — and it is a
// human's call about correspondence they have write authority over.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AudienceMember names one user or team admitted to a `selected` audience.
type AudienceMember struct {
	SubjectType string // "user" | "team"
	SubjectID   ids.UUID
}

// SetAudienceInput is one audience write: the new audience and, for
// `selected`, the full member set that replaces the previous one.
type SetAudienceInput struct {
	Audience  string
	Members   []AudienceMember
	IfVersion *int64
}

// maxAudienceMembers bounds one `selected` audience — the contract's maxItems.
const maxAudienceMembers = 200

// SetAudience limits (or re-opens) who may read the activity's content.
// Human-only: an agent acting for a human never narrows or widens what the
// human's colleagues read. The caller needs write authority over the row,
// which is the author, the assignee or host, or a linked record that is
// theirs to change. The audience column, the member rows, the audit row and
// the activity.updated event commit together; a row held under a retention
// obligation is refused by the restriction guard (423).
func (s *Store) SetAudience(ctx context.Context, id ids.ActivityID, in SetAudienceInput) (crmcontracts.Activity, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return crmcontracts.Activity{}, err
	}
	if !crmcontracts.ActivityAudience(in.Audience).Valid() {
		return crmcontracts.Activity{}, &InvalidAudienceError{Audience: in.Audience}
	}
	members, err := audienceMembersFor(in)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "activity", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		if err := auth.EnsureActivityWritable(ctx, tx, id.UUID); err != nil {
			return err
		}
		if err := ensureVersion(ctx, tx, id, in.IfVersion); err != nil {
			return err
		}
		if err := ensureAudienceSubjectsExist(ctx, tx, members); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE activity SET audience = $2 WHERE id = $1`, id, in.Audience); err != nil {
			return err
		}
		if err := replaceAudienceMembers(ctx, tx, id, members); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "activity", id.UUID, nil, map[string]any{
			"audience": in.Audience, "member_count": len(members),
		})
		if err != nil {
			return err
		}
		audience := crmcontracts.PublicEventActivityChangedFieldsAudience(in.Audience)
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventActivityUpdated{
			ChangedFields: crmcontracts.PublicEventActivityChangedFields{Audience: &audience},
		}); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, err
}

// audienceMembersFor validates the member set: read only for `selected`,
// bounded, and every subject type a word the table admits.
func audienceMembersFor(in SetAudienceInput) ([]AudienceMember, error) {
	if in.Audience != string(crmcontracts.ActivityAudienceSelected) {
		return nil, nil
	}
	if len(in.Members) > maxAudienceMembers {
		return nil, &InvalidAudienceError{Audience: in.Audience, Reason: "too many members"}
	}
	for _, m := range in.Members {
		if m.SubjectType != "user" && m.SubjectType != "team" {
			return nil, &InvalidAudienceError{Audience: in.Audience, Reason: "unknown subject_type " + m.SubjectType}
		}
		if m.SubjectID == ids.Nil {
			return nil, &InvalidAudienceError{Audience: in.Audience, Reason: "a member needs a subject_id"}
		}
	}
	return in.Members, nil
}

// ensureVersion is the If-Match compare, against the row the caller just
// locked; a mismatch is version skew, which the wire answers as 409.
func ensureVersion(ctx context.Context, tx pgx.Tx, id ids.ActivityID, ifVersion *int64) error {
	if ifVersion == nil {
		return nil
	}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT version FROM activity WHERE id = $1 AND archived_at IS NULL`, id).Scan(&current); err != nil {
		return err
	}
	if current != *ifVersion {
		return apperrors.ErrVersionSkew
	}
	return nil
}

// ensureAudienceSubjectsExist refuses a member that names no live user or
// team of this workspace: the table carries no FK for its polymorphic subject,
// so the check is here, and a guessed id answers like a malformed one.
func ensureAudienceSubjectsExist(ctx context.Context, tx pgx.Tx, members []AudienceMember) error {
	for _, m := range members {
		var exists bool
		var q string
		if m.SubjectType == "user" {
			q = `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND archived_at IS NULL)`
		} else {
			q = `SELECT EXISTS (SELECT 1 FROM team WHERE id = $1)`
		}
		if err := tx.QueryRow(ctx, q, m.SubjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return &InvalidAudienceError{Audience: "selected", Reason: fmt.Sprintf("%s %s does not exist", m.SubjectType, m.SubjectID)}
		}
	}
	return nil
}

// replaceAudienceMembers writes the full member set, replacing whatever a
// previous `selected` audience named. An audience other than `selected`
// leaves no member rows behind.
func replaceAudienceMembers(ctx context.Context, tx pgx.Tx, id ids.ActivityID, members []AudienceMember) error {
	if _, err := tx.Exec(ctx, `DELETE FROM activity_audience_member WHERE activity_id = $1`, id); err != nil {
		return err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("activities: no principal bound for the audience write")
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_audience_member (activity_id, subject_type, subject_id, created_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (activity_id, subject_type, subject_id) DO NOTHING`,
			id, m.SubjectType, m.SubjectID, actor.ID); err != nil {
			return err
		}
	}
	return nil
}

// readActivityContent is readActivity for a caller about to USE the content —
// reply to it, transcribe it, send on its thread: the audience gate runs as a
// probe first, so a limited conversation answers ErrNotFound rather than a
// row with its text blanked.
func readActivityContent(ctx context.Context, tx pgx.Tx, id ids.ActivityID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	if err := auth.EnsureActivityContentVisible(ctx, tx, id.UUID); err != nil {
		return crmcontracts.Activity{}, err
	}
	return readActivity(ctx, tx, id, archived)
}

// GetActivityContent is GetActivity for a caller about to USE the content —
// draft a reply, quote the thread — rather than show the row: a limited
// conversation the caller may discover answers ErrNotFound here, never a row
// with its text blanked that a model would then write a reply to.
func (s *Store) GetActivityContent(ctx context.Context, id ids.ActivityID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readActivityContent(ctx, tx, id, archived)
		return err
	})
	return out, err
}

// InvalidAudienceError is a malformed audience write: an unknown audience, a
// member set the contract does not admit, or a subject that does not exist.
type InvalidAudienceError struct {
	Audience string
	Reason   string
}

func (e *InvalidAudienceError) Error() string {
	if e.Reason == "" {
		return "activities: invalid audience " + e.Audience
	}
	return "activities: invalid audience write: " + e.Reason
}

// FieldFault answers the malformed audience write as a 422 on the audience
// field.
func (e *InvalidAudienceError) FieldFault() (field, code, message string) {
	return "audience", "invalid_audience", e.Error()
}
