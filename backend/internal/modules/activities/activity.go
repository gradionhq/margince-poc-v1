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

// fieldKind names the activity kind in the write shape's audit and outbox
// payloads (the one spelling of the payload key).
const fieldKind = "kind"

// activityCapturedPayload builds the activity.captured event for the
// direct-log path (this package's only emit site of the event's two) — it
// never names a source_system, which is exclusive to the capture
// auto-create path (capture/sink.go's own local builder).
func activityCapturedPayload(kind string) crmcontracts.PublicEventActivityCaptured {
	return crmcontracts.PublicEventActivityCaptured{Kind: kind}
}

// ActivityLinkInput ties one activity to a person, organization or deal.
type ActivityLinkInput struct {
	EntityType string // person | organization | deal
	// note: the link target is polymorphic (activity_link is the canonical
	// (entity_type, entity_id) seam), so the id stays untyped (rule 6).
	EntityID ids.UUID
}

type LogActivityInput struct {
	Kind         string
	Subject      *string
	Body         *string
	OccurredAt   *time.Time
	Direction    *string
	DueAt        *time.Time
	RemindAt     *time.Time
	AssigneeID   *ids.UserID
	HostUserID   *ids.UserID
	SourceSystem *string
	SourceID     *string
	// ThreadKey files this activity under a conversation. Empty stores NULL.
	// It is written at insert time or not at all: the (source_system,
	// source_id) upsert both capture and this path key on does nothing when
	// the row already exists, so neither leg can revise the other's value.
	ThreadKey string
	// CounterpartyEmail is the address this message was with, normalized —
	// the column capture's correspondence-positive gate (ADR-0072 §1) reads.
	// CounterpartyOutboundAttested says the workspace itself sent to that
	// address; it is affirmative intent toward them, and it is what spares
	// their later mail from suppression. Both obey the same write-once rule
	// as ThreadKey, for the same reason.
	CounterpartyEmail            string
	CounterpartyOutboundAttested bool
	Links                        []ActivityLinkInput
	Source                       string
}

// LogActivity writes the activity + links and maintains
// deal.last_activity_at (data-model §7: kept current on write, driving
// the stalled flag). Idempotent on (source_system, source_id): replaying
// a capture returns the existing activity.
func (s *Store) LogActivity(ctx context.Context, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	var out crmcontracts.Activity
	created := true
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, created, err = logActivityInTx(ctx, tx, in)
		return err
	})
	return out, created, err
}

// LogActivityTx is LogActivity's transaction-accepting variant (the C5
// shared-tx shape): a caller that must commit an activity note atomically
// with a sibling module's own write (the extraction accept-write's deal
// update, compose/extractionaccept.go) drives it inside the ONE
// transaction it already opened, so a note failure rolls the sibling
// write back too, instead of letting LogActivity open (and commit) a
// second transaction of its own.
func (s *Store) LogActivityTx(ctx context.Context, tx pgx.Tx, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	return logActivityInTx(ctx, tx, in)
}

// logActivityInTx is LogActivity's transactional body, shared by the
// store-opened (LogActivity) and caller-opened (LogActivityTx) entry
// points.
func logActivityInTx(ctx context.Context, tx pgx.Tx, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	occurredAt := time.Now().UTC()
	if in.OccurredAt != nil {
		occurredAt = in.OccurredAt.UTC()
	}
	wsID := workspaceID(ctx)

	replay, err := replayedActivity(ctx, tx, in)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	if replay != nil {
		return *replay, false, nil
	}

	id := ids.New[ids.ActivityKind]()
	_, err = tx.Exec(ctx,
		`INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, direction,
		                       due_at, remind_at, assignee_id, host_user_id, source_system, source_id, source, captured_by,
		                       thread_key, counterparty_email, counterparty_outbound_attested)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''),
		         NULLIF($17, ''), $18)`,
		id, wsID, in.Kind, in.Subject, in.Body, occurredAt, in.Direction,
		in.DueAt, in.RemindAt, in.AssigneeID, in.HostUserID, in.SourceSystem, in.SourceID, in.Source, by,
		in.ThreadKey, in.CounterpartyEmail, in.CounterpartyOutboundAttested)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.Activity{}, false, apperrors.ErrConflict
		}
		return crmcontracts.Activity{}, false, err
	}

	if err := insertActivityLinks(ctx, tx, wsID, id, in.Links, occurredAt); err != nil {
		return crmcontracts.Activity{}, false, err
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "activity", id.UUID, nil, map[string]any{fieldKind: in.Kind, "subject": in.Subject})
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	// activity.captured is the first-class verb — emitted instead of a
	// generic activity.created, never in addition (events.md §1).
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, activityCapturedPayload(in.Kind)); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	out, err := readActivity(ctx, tx, id, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	return out, true, nil
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
	var existing ids.ActivityID
	err := tx.QueryRow(ctx,
		`SELECT id FROM activity WHERE source_system = $1 AND source_id = $2`,
		*in.SourceSystem, *in.SourceID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The row exists — the SELECT above just found it — so the only way
	// readActivity's own row-scope gate can answer ErrNotFound here is that
	// the key belongs to someone out of scope.
	out, err := readActivity(ctx, tx, existing, storekit.IncludeArchived)
	if errors.Is(err, apperrors.ErrNotFound) {
		return nil, apperrors.ErrConflict
	}
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
func insertActivityLinks(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, activityID ids.ActivityID, links []ActivityLinkInput, occurredAt time.Time) error {
	for _, link := range links {
		column := linkColumn(link.EntityType)
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
	return "activity link entity_type " + e.EntityType + " is not " + linkVocabulary()
}

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

type ListActivitiesInput struct {
	Cursor     *string
	Limit      *int
	Kind       *string
	EntityType *string
	// note: EntityType+EntityID is the polymorphic activity_link filter —
	// the target is ANY entity kind, so the id stays untyped (rule 6).
	EntityID *ids.UUID
	// Query is the contract's `q`: a substring match over the subject and
	// body a human would recognize the item by.
	Query           *string
	IncludeArchived bool
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
	limit := storekit.ClampLimit(in.Limit)
	join, where, args, err := listActivitiesFilter(ctx, in)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	rows, err := tx.Query(ctx,
		`SELECT `+activityColumns+` FROM activity a`+join+` WHERE `+strings.Join(where, " AND ")+
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

// listActivitiesFilter builds the timeline query's join, WHERE terms and
// bind arguments from one list input.
func listActivitiesFilter(ctx context.Context, in ListActivitiesInput) (join string, where []string, args []any, err error) {
	where = []string{"1=1"}
	args = []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	// The timeline carries the workspace's most sensitive free-text, so
	// it is scoped through the linked records.
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return "", nil, nil, err
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
	if in.EntityType != nil && in.EntityID != nil {
		join = ` JOIN activity_link al ON al.activity_id = a.id`
		where = append(where, sprintf("al.entity_type = $%d", arg(*in.EntityType)))
		// The SAME vocabulary the write uses. A second list here drifted from
		// linkTargets and silently dropped two kinds: an activity could be
		// linked to a lead or a project and then be unfindable by filtering on
		// the very link that was just written.
		column := linkColumn(*in.EntityType)
		if column == "" {
			return "", nil, nil, &InvalidLinkTypeError{EntityType: *in.EntityType}
		}
		where = append(where, sprintf("al.%s = $%d", column, arg(*in.EntityID)))
	}
	if in.Query != nil && *in.Query != "" {
		// subject + body are the two human-readable columns a person would
		// recognize an item by. The wildcard is escaped, so a caller typing %
		// searches for a percent sign rather than matching everything.
		pos := arg("%" + storekit.EscapeLike(*in.Query) + "%")
		where = append(where, sprintf("(a.subject ILIKE $%d ESCAPE '\\' OR a.body ILIKE $%d ESCAPE '\\')", pos, pos))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, decodeErr := storekit.DecodeCursor(*in.Cursor)
		if decodeErr != nil {
			return "", nil, nil, decodeErr
		}
		where = append(where, sprintf("(a.occurred_at, a.id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}
	return join, where, args, nil
}

const activityColumns = `a.id, a.workspace_id, a.kind, a.subject, a.body, a.occurred_at, a.direction,
	a.due_at, a.remind_at, a.assignee_id, a.is_done, a.done_at, a.duration_seconds, a.meeting_status,
	a.source_system, a.source_id, a.source, a.captured_by, a.version, a.created_at, a.updated_at, a.archived_at`

// readActivity is the module's ONE single-row activity read, and it
// carries the row scope itself. An activity has no owner_id and RLS binds
// only the workspace, so its scope exists solely as the link-walk in
// auth.ActivityScopeClause — a probe a call site can forget, and three
// lifecycle mutators did. Anything that returns a record is a read, so the
// gate lives here: an out-of-scope id reads as ErrNotFound, the same answer
// a missing row gives, whether the caller is getting, updating, archiving
// or relinking.
func readActivity(ctx context.Context, tx pgx.Tx, id ids.ActivityID, archived storekit.ArchivedFilter) (crmcontracts.Activity, error) {
	if err := auth.EnsureActivityVisible(ctx, tx, id.UUID); err != nil {
		return crmcontracts.Activity{}, err
	}
	q := `SELECT ` + activityColumns + ` FROM activity a WHERE a.id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND a.archived_at IS NULL`
	}
	a, err := scanActivity(tx.QueryRow(ctx, q, id))
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
	var id, wsID ids.UUID
	var assigneeID *ids.UUID
	var kind string
	var direction, meetingStatus *string
	var version int64

	err := row.Scan(&id, &wsID, &kind, &a.Subject, &a.Body, &a.OccurredAt, &direction,
		&a.DueAt, &a.RemindAt, &assigneeID, &a.IsDone, &a.DoneAt, &a.DurationSeconds, &meetingStatus,
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
