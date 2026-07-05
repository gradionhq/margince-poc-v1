// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ActivityLinkInput ties one activity to a person, organization or deal.
type ActivityLinkInput struct {
	EntityType string // person | organization | deal
	EntityID   ids.UUID
}

type LogActivityInput struct {
	Kind         string
	Subject      *string
	Body         *string
	OccurredAt   *time.Time
	Direction    *string
	DueAt        *time.Time
	AssigneeID   *ids.UUID
	HostUserID   *ids.UUID
	SourceSystem *string
	SourceID     *string
	Links        []ActivityLinkInput
	Source       string
}

// LogActivity writes the activity + links and maintains
// deal.last_activity_at (data-model §7: kept current on write, driving
// the stalled flag). Idempotent on (source_system, source_id): replaying
// a capture returns the existing activity.
func (s *Store) LogActivity(ctx context.Context, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	occurredAt := time.Now().UTC()
	if in.OccurredAt != nil {
		occurredAt = in.OccurredAt.UTC()
	}

	var out crmcontracts.Activity
	created := true
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := storekit.MustWorkspace(ctx)

		replay, err := replayedActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if replay != nil {
			created, out = false, *replay
			return nil
		}

		id := ids.NewV7()
		_, err = tx.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, direction,
			                       due_at, assignee_id, host_user_id, source_system, source_id, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			id, wsID, in.Kind, in.Subject, in.Body, occurredAt, in.Direction,
			in.DueAt, in.AssigneeID, in.HostUserID, in.SourceSystem, in.SourceID, in.Source, by)
		if err != nil {
			if storekit.IsUniqueViolation(err) {
				return apperrors.ErrConflict
			}
			return err
		}

		if err := insertActivityLinks(ctx, tx, wsID, id, in.Links, occurredAt); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "activity", id, nil, map[string]any{"kind": in.Kind, "subject": in.Subject})
		if err != nil {
			return err
		}
		// activity.captured is the first-class verb — emitted instead of
		// a generic activity.created, never in addition (events.md §1).
		if err := storekit.Emit(ctx, tx, auditID, "activity.captured", "activity", id, map[string]any{"kind": in.Kind}); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, created, err
}

// replayedActivity resolves the (source_system, source_id) idempotency
// key: replaying a capture returns the existing activity. The replay
// path returns a record, so it is a read and carries the read's row
// scope: replaying someone else's external key must not hand over their
// activity. Out of scope answers the same 409 the unique-index race
// does — the key is taken, the record is not disclosed.
func replayedActivity(ctx context.Context, tx pgx.Tx, in LogActivityInput) (*crmcontracts.Activity, error) {
	if in.SourceSystem == nil || in.SourceID == nil {
		return nil, nil
	}
	var existing ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM activity WHERE source_system = $1 AND source_id = $2`,
		*in.SourceSystem, *in.SourceID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if verr := auth.EnsureActivityVisible(ctx, tx, existing); verr != nil {
		if errors.Is(verr, apperrors.ErrNotFound) {
			return nil, apperrors.ErrConflict
		}
		return nil, verr
	}
	out, err := readActivity(ctx, tx, existing, storekit.IncludeArchived)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// insertActivityLinks writes the polymorphic link rows and maintains
// deal.last_activity_at on deal links. The FK alone is not enough: it is
// checked as the table owner, bypassing RLS, so it would accept a
// guessed cross-tenant or out-of-scope UUID as a link target — every
// target passes the row-scope link check first.
func insertActivityLinks(ctx context.Context, tx pgx.Tx, wsID, activityID ids.UUID, links []ActivityLinkInput, occurredAt time.Time) error {
	for _, link := range links {
		column := map[string]string{
			"person": "person_id", "organization": "organization_id", "deal": "deal_id",
		}[link.EntityType]
		if column == "" {
			return &InvalidLinkTypeError{EntityType: link.EntityType}
		}
		if err := auth.EnsureLinkTarget(ctx, tx, link.EntityType, link.EntityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			sprintf(`INSERT INTO activity_link (workspace_id, activity_id, entity_type, %s) VALUES ($1, $2, $3, $4)`, column),
			wsID, activityID, link.EntityType, link.EntityID); err != nil {
			return err
		}
		if link.EntityType == "deal" {
			if _, err := tx.Exec(ctx,
				`UPDATE deal SET last_activity_at = greatest(coalesce(last_activity_at, $2), $2) WHERE id = $1`,
				link.EntityID, occurredAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// InvalidLinkTypeError maps to 422.
type InvalidLinkTypeError struct{ EntityType string }

func (e *InvalidLinkTypeError) Error() string {
	return "activity link entity_type " + e.EntityType + " is not person|organization|deal"
}

func (s *Store) GetActivity(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureActivityVisible(ctx, tx, id); err != nil {
			return err
		}
		out, err = readActivity(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListActivitiesInput struct {
	Cursor          *string
	Limit           *int
	Kind            *string
	EntityType      *string
	EntityID        *ids.UUID
	IncludeArchived bool
}

// ListActivities is the timeline read: newest first, optionally scoped to
// one entity through activity_link (the indexed 360-view join).
func (s *Store) ListActivities(ctx context.Context, in ListActivitiesInput) ([]crmcontracts.Activity, storekit.Page, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	// The timeline carries the workspace's most sensitive free-text, so
	// it is scoped through the linked records.
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "a.archived_at IS NULL")
	}
	if in.Kind != nil {
		where = append(where, sprintf("a.kind = $%d", arg(*in.Kind)))
	}
	join := ""
	if in.EntityType != nil && in.EntityID != nil {
		join = ` JOIN activity_link al ON al.activity_id = a.id`
		where = append(where, sprintf("al.entity_type = $%d", arg(*in.EntityType)))
		column := map[string]string{
			"person": "al.person_id", "organization": "al.organization_id", "deal": "al.deal_id",
		}[*in.EntityType]
		if column == "" {
			return nil, storekit.Page{}, &InvalidLinkTypeError{EntityType: *in.EntityType}
		}
		where = append(where, sprintf("%s = $%d", column, arg(*in.EntityID)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, sprintf("(a.occurred_at, a.id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var activities []crmcontracts.Activity
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+activityColumns+` FROM activity a`+join+` WHERE `+strings.Join(where, " AND ")+
				sprintf(` ORDER BY a.occurred_at DESC, a.id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanActivity(rows)
			if err != nil {
				return err
			}
			activities = append(activities, a)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(activities) > limit {
			activities = activities[:limit]
			last := activities[len(activities)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.OccurredAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if activities == nil {
		activities = []crmcontracts.Activity{}
	}
	return activities, page, err
}

const activityColumns = `a.id, a.workspace_id, a.kind, a.subject, a.body, a.occurred_at, a.direction,
	a.due_at, a.assignee_id, a.is_done, a.done_at, a.duration_seconds, a.meeting_status,
	a.source_system, a.source_id, a.source, a.captured_by, a.version, a.created_at, a.updated_at, a.archived_at`

func readActivity(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	q := `SELECT ` + activityColumns + ` FROM activity a WHERE a.id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND a.archived_at IS NULL`
	}
	a, err := scanActivity(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Activity{}, apperrors.ErrNotFound
	}
	return a, err
}

func scanActivity(row pgx.Row) (crmcontracts.Activity, error) {
	var a crmcontracts.Activity
	var id, wsID ids.UUID
	var assigneeID *ids.UUID
	var kind string
	var direction, meetingStatus *string
	var version int64

	err := row.Scan(&id, &wsID, &kind, &a.Subject, &a.Body, &a.OccurredAt, &direction,
		&a.DueAt, &assigneeID, &a.IsDone, &a.DoneAt, &a.DurationSeconds, &meetingStatus,
		&a.SourceSystem, &a.SourceId, &a.Source, &a.CapturedBy, &version, &a.CreatedAt, &a.UpdatedAt, &a.ArchivedAt)
	if err != nil {
		return a, err
	}

	a.Id = openapi_types.UUID(id)
	a.WorkspaceId = openapi_types.UUID(wsID)
	a.AssigneeId = uuidPtr(assigneeID)
	a.Kind = crmcontracts.ActivityKind(kind)
	if direction != nil {
		d := crmcontracts.ActivityDirection(*direction)
		a.Direction = &d
	}
	if meetingStatus != nil {
		m := crmcontracts.ActivityMeetingStatus(*meetingStatus)
		a.MeetingStatus = &m
	}
	a.Version = &version
	return a, nil
}
