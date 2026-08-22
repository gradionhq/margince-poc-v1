// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Reading the timeline: one activity, a page of them, the columns each read
// selects, and the links hung off the rows. The write side lives in
// activity.go — they were one file until it crossed the 500-line cap, and the
// seam between "record what happened" and "show what was recorded" is where
// the file wanted to come apart anyway.

import (
	"context"
	"errors"
	"strings"
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

// GetActivity reads one activity by id, opening its own transaction.
//
// Row scope and the held-row exclusion both live in readActivity, so every
// caller on this path gets them whether it opens the transaction or not.
func (s *Store) GetActivity(ctx context.Context, id ids.ActivityID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return crmcontracts.Activity{}, err
	}
	var out crmcontracts.Activity
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readActivity(ctx, tx, id, archived)
		return err
	})
	return out, err
}

// ListActivitiesInput narrows a timeline read: the page, the kind, the
// transport, and the record the activities hang off.
type ListActivitiesInput struct {
	Cursor *string
	Limit  *int
	Kind   *string
	// ChannelProvider narrows to messages carried by ONE transport. Since the
	// kind stopped naming the transport (ADR-0107/A158) this is the only way to
	// ask the question `kind=telegram` used to answer, and it is a separate dial
	// from Kind rather than a second spelling of it: the two compose, so "every
	// message on telegram" and "every message" are both askable.
	ChannelProvider *string
	EntityType      *string
	// note: EntityType+EntityID is the polymorphic activity_link filter —
	// the target is ANY entity kind, so the id stays untyped (rule 6).
	EntityID *ids.UUID
	// Query is the contract's `q`: a substring match over the subject and
	// body a human would recognize the item by.
	Query *string
	// ThreadKey narrows to ONE provider conversation. The company timeline
	// groups client-side over the page it holds; a group the page cut off
	// completes itself through this rather than by widening the page for every
	// account that has no long thread.
	ThreadKey       *string
	IncludeArchived bool
	// AssigneeID is the work queue's narrowing: the OPEN tasks one person
	// holds, which is what the contract declares the parameter to mean and
	// what the partial index behind it is built on. Done-ness is part of
	// that question rather than a second dial — see openTaskAssigneeClause.
	AssigneeID *ids.UserID
	// WithinProjectID narrows to one body of work, EXCLUDING what belongs to
	// another project and keeping what belongs to none.
	//
	// It is not a second spelling of EntityType="project"+EntityID, and the
	// difference is the whole point. That pair asks "what is filed under this
	// project"; this asks "what is on this account, minus the other
	// engagement" — the anchor stays the person or company, and the general
	// correspondence that carries no project at all stays with it. A reader
	// preparing for an ERP meeting still wants the relationship's history;
	// they do not want the datacentre migration.
	WithinProjectID *ids.ProjectID
	// OccurredAfter / OccurredBefore bound the timeline to a range: the
	// lower end inclusive, the upper end exclusive, so a calendar day is
	// [day 00:00, next day 00:00) with no double-counting at midnight.
	OccurredAfter  *time.Time
	OccurredBefore *time.Time
}

// ListActivities is the timeline read: newest first, optionally scoped to
// one entity through activity_link (the indexed 360-view join).
func (s *Store) ListActivities(ctx context.Context, in ListActivitiesInput) ([]crmcontracts.Activity, storekit.Page, error) {
	var activities []crmcontracts.Activity
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		activities, page, err = ListActivitiesTx(ctx, tx, in)
		return err
	})
	return activities, page, err
}

// ListActivitiesTx is ListActivities for a caller that already opened a
// transaction — the composite record read, whose timeline section must
// describe the same instant as its sibling sections. Same gate, same
// ordering; only the transaction is borrowed.
func ListActivitiesTx(ctx context.Context, tx pgx.Tx, in ListActivitiesInput) ([]crmcontracts.Activity, storekit.Page, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	// The record the timeline was narrowed TO is gated before it is filtered
	// on. The scope below is an ANY-LINK rule, so an activity linked to both a
	// visible person and a lead the caller may not read passes it — and
	// filtering on that lead's id would then answer "this lead exists, and here
	// is what happened on it" to someone with no right to either fact.
	if err := ensureNarrowingTargetVisible(ctx, tx, in.EntityType, in.EntityID); err != nil {
		return nil, storekit.Page{}, err
	}
	if in.WithinProjectID != nil {
		if err := RequireProjectScope(ctx, tx, *in.WithinProjectID); err != nil {
			return nil, storekit.Page{}, err
		}
	}
	limit := storekit.ClampLimit(in.Limit)
	join, where, content, args, err := listActivitiesFilter(ctx, in)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	rows, err := tx.Query(ctx,
		`SELECT `+activityColumns(content)+` FROM activity a`+join+` WHERE `+strings.Join(where, " AND ")+
			sprintf(` ORDER BY a.occurred_at DESC, a.id DESC LIMIT %d`, limit+1),
		args...)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	// Collected rather than streamed: attachLinks runs a second query on
	// this same transaction, which needs the cursor already closed.
	activities, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Activity, error) {
		return scanActivity(row)
	})
	if err != nil {
		return nil, storekit.Page{}, err
	}
	var page storekit.Page
	if len(activities) > limit {
		activities = activities[:limit]
		last := activities[len(activities)-1]
		page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.OccurredAt, ids.UUID(last.Id))}
	}
	if err := attachLinks(ctx, tx, activities); err != nil {
		return nil, storekit.Page{}, err
	}
	if activities == nil {
		activities = []crmcontracts.Activity{}
	}
	return activities, page, nil
}

// activityColumns is the select list every activity read scans, closed by
// the caller's audience test as `content_available` — the one column that
// decides whether scanActivity hands back the row's content or only its
// markers. contentArm is the predicate auth.ActivityAudienceArm rendered for
// this query's arguments.
func activityColumns(contentArm string) string {
	return `a.id, a.kind, a.channel_provider, a.subject, a.body, a.occurred_at, a.direction,
	a.due_at, a.remind_at, a.assignee_id, a.is_done, a.done_at, a.duration_seconds, a.meeting_status,
	a.source_system, a.source_id, a.source, a.captured_by, a.version, a.created_at, a.updated_at, a.archived_at,
	a.thread_key, a.capture_label, a.bulk_mail_attested, a.audience, (` + contentArm + `) AS content_available`
}

// readActivity is the module's ONE single-row activity read, and it
// carries the row scope itself. An activity has no owner_id and the
// workspace predicate bounds only the tenant, so its scope exists solely
// as the link-walk in
// auth.ActivityContentClause — a probe a call site can forget, and three
// lifecycle mutators did. Anything that returns a record is a read, so the
// gate lives here: an out-of-scope id reads as ErrNotFound, the same answer
// a missing row gives, whether the caller is getting, updating, archiving
// or relinking.
func readActivity(ctx context.Context, tx pgx.Tx, id ids.ActivityID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	// DISCOVER-gated, like the list: a row the caller may know about is
	// read, and the audience decides per row whether its content comes
	// along (content_state). A caller who needs the content itself — a
	// send, an attachment, a transcript — asks readActivityContent.
	if err := auth.EnsureActivityVisible(ctx, tx, id.UUID); err != nil {
		return crmcontracts.Activity{}, err
	}
	args := []any{id}
	arg := func(v any) int { args = append(args, v); return len(args) }
	content, err := auth.ActivityAudienceArm(ctx, "a", arg)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	q := `SELECT ` + activityColumns(content) + ` FROM activity a WHERE a.id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND a.archived_at IS NULL`
	}
	a, err := scanActivity(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Activity{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	one := []crmcontracts.Activity{a}
	if err := attachLinks(ctx, tx, one); err != nil {
		return crmcontracts.Activity{}, err
	}
	return one[0], nil
}

// attachLinks fills the contract's links[] on a page of activities in ONE
// query — the column the timeline's "via" chips and the per-person filter
// read. Batched rather than per-row because the timeline reads a page at a
// time.
//
// Each link row carries its OWN row-scope check, which the activity's does
// not subsume. Activity visibility is an ANY-link rule: one visible person
// makes the whole activity readable. Projecting every link row back would
// then disclose the ids of the other records it touches — a colleague's
// deal on the same thread — to a caller who cannot read them. A link whose
// target is out of scope is dropped, so links[] answers "what this is about,
// as far as you can see".
func attachLinks(ctx context.Context, tx pgx.Tx, activities []crmcontracts.Activity) error {
	if len(activities) == 0 {
		return nil
	}
	activityIDs := make([]ids.UUID, len(activities))
	byID := make(map[ids.UUID]int, len(activities))
	for i, a := range activities {
		activityIDs[i] = ids.UUID(a.Id)
		byID[ids.UUID(a.Id)] = i
	}
	args := []any{activityIDs}
	arg := func(v any) int { args = append(args, v); return len(args) }
	visible, err := auth.LinkTargetVisibleClause(ctx, "al", arg)
	if err != nil {
		return err
	}
	if visible == "" {
		visible = "TRUE"
	}
	rows, err := tx.Query(ctx, `
		SELECT al.id, al.activity_id, al.entity_type, `+linkIDCoalesceQualified("al")+`
		FROM activity_link al
		WHERE al.activity_id = ANY($1) AND `+visible+`
		ORDER BY al.activity_id, al.entity_type, al.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var linkID, activityID, entityID ids.UUID
		var entityType string
		if err := rows.Scan(&linkID, &activityID, &entityType, &entityID); err != nil {
			return err
		}
		i, ok := byID[activityID]
		if !ok {
			continue
		}
		link := crmcontracts.ActivityLink{
			Id:         (*openapi_types.UUID)(&linkID),
			ActivityId: (*openapi_types.UUID)(&activityID),
			EntityType: crmcontracts.ActivityLinkEntityType(entityType),
			EntityId:   openapi_types.UUID(entityID),
		}
		if activities[i].Links == nil {
			activities[i].Links = &[]crmcontracts.ActivityLink{}
		}
		*activities[i].Links = append(*activities[i].Links, link)
	}
	return rows.Err()
}

func scanActivity(row pgx.Row) (crmcontracts.Activity, error) {
	var a crmcontracts.Activity
	var id ids.UUID
	var assigneeID *ids.UUID
	var kind string
	var channelProvider, direction, meetingStatus, threadKey, captureLabel *string
	var bulkMailAttested bool
	var version int64

	var audience string
	var contentAvailable bool

	err := row.Scan(&id, &kind, &channelProvider, &a.Subject, &a.Body, &a.OccurredAt, &direction,
		&a.DueAt, &a.RemindAt, &assigneeID, &a.IsDone, &a.DoneAt, &a.DurationSeconds, &meetingStatus,
		&a.SourceSystem, &a.SourceId, &a.Source, &a.CapturedBy, &version, &a.CreatedAt, &a.UpdatedAt, &a.ArchivedAt,
		&threadKey, &captureLabel, &bulkMailAttested, &audience, &contentAvailable)
	if err != nil {
		return a, err
	}
	aud := crmcontracts.ActivityAudience(audience)
	a.Audience = &aud
	state := crmcontracts.ActivityContentStateAvailable
	if !contentAvailable {
		// Withheld: the row is discoverable, its content is not the caller's.
		// Everything that carries what was said — or identifies the message
		// at the provider — goes; the markers stay.
		state = crmcontracts.ActivityContentStateWithheld
		a.Subject, a.Body, a.SourceId = nil, nil, nil
		threadKey, captureLabel = nil, nil
	}
	a.ContentState = &state

	a.Id = openapi_types.UUID(id)
	a.AssigneeId = uuidPtr(assigneeID)
	a.Kind = crmcontracts.ActivityKind(kind)
	a.ChannelProvider = channelProvider
	if direction != nil {
		d := crmcontracts.ActivityDirection(*direction)
		a.Direction = &d
	}
	if meetingStatus != nil {
		m := crmcontracts.ActivityMeetingStatus(*meetingStatus)
		a.MeetingStatus = &m
	}
	a.ThreadKey = threadKey
	if captureLabel != nil {
		label := crmcontracts.ActivityCaptureLabel(*captureLabel)
		a.CaptureLabel = &label
	}
	a.BulkMailAttested = &bulkMailAttested
	a.Version = &version
	return a, nil
}
