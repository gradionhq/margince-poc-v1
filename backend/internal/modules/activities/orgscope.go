// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Which activities belong to which record — the timeline list's filter, and
// the account walk every other reader of the account's timeline shares.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// OrgLinkedActivityExists is the ONE spelling of "this activity reaches the
// account", for a query that aliases activity as a.
//
// A message or a task belongs to a company through any of three links — its own,
// its deal's, or the contact it is about — and every reader of the account's
// timeline needs the same walk: the timeline list itself, the company view's
// next-steps section, and the two suggestion reads. Spelling it once is what
// keeps them from drifting apart: a fourth link added to the model reaches every
// reader, or none of them.
//
// It lives in this module rather than next to the company view because a module
// may not import compose, and the timeline list is a reader too — the drift this
// replaces was exactly that, a flat organization_id match in the list against
// the three-arm walk in the view.
//
// The predicate answers WHICH activities belong to the account and nothing else.
// WHO may read one is auth.ActivityScopeClause, which every caller composes
// alongside this.
//
// orgPos is the bind position carrying the organization id; the caller registers
// it once and every arm reads the same one.
func OrgLinkedActivityExists(orgPos int) string {
	return activityReachesOrg(sprintf("$%d", orgPos))
}

// OrgLinkedActivityExistsAny is the same walk over a SET of organizations, for
// a caller that binds an id array rather than one id.
//
// The hierarchy roll-up needs it. Its 30-day count used to match
// activity_link.organization_id alone, which asked a narrower question than the
// timeline the number is displayed above: capture files mail against the PERSON
// it was with, so an account's busiest correspondence carries no organization
// link at all and went uncounted. One walk, two bind shapes — a fourth link
// added to the model still reaches both.
//
// orgsPos is the bind position carrying the organization id array.
func OrgLinkedActivityExistsAny(orgsPos int) string {
	return activityReachesOrg(sprintf("ANY($%d)", orgsPos))
}

// activityReachesOrg is the walk itself. operand is what each arm compares its
// organization id against — a single bind, or ANY(array) — so the three links
// are written once and neither caller can drift from the other.
func activityReachesOrg(operand string) string {
	return sprintf(`EXISTS (
		    SELECT 1 FROM activity_link l
		    LEFT JOIN deal d ON d.id = l.deal_id
		    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		      AND r.ended_at IS NULL AND r.archived_at IS NULL
		    WHERE l.activity_id = a.id
		      AND (l.organization_id = %[1]s OR d.organization_id = %[1]s OR r.organization_id = %[1]s))`,
		operand)
}

// openTaskAssigneeClause narrows the timeline to the OPEN tasks one person
// holds — the queue read the contract declares ("Open tasks for an
// assignee"), spelled as the predicate the partial index behind it is built
// on (idx_activity_tasks: workspace_id, assignee_id, due_at WHERE kind='task'
// AND NOT is_done AND archived_at IS NULL).
//
// Done-ness belongs to the filter rather than to a dial of its own, and that
// is the whole point: no parameter answers it, so binding assignee_id as a
// plain column match would hand back every task the person ever closed under
// a name the contract says means the open ones. A wider answer wearing the
// declared answer's shape is the failure this filter exists to close, not a
// convenience to preserve.
//
// `kind` is stated rather than implied. The `activity_task_fields` CHECK
// already keeps assignee_id NULL on every other kind, so it narrows nothing —
// it is what lets the planner match the partial index.
func openTaskAssigneeClause(assignee *ids.UserID, arg func(any) int) string {
	if assignee == nil {
		return ""
	}
	return sprintf("a.assignee_id = $%d AND a.kind = 'task' AND NOT a.is_done", arg(*assignee))
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
	if clause := openTaskAssigneeClause(in.AssigneeID, arg); clause != "" {
		where = append(where, clause)
	}
	if in.EntityType != nil && in.EntityID != nil {
		// The SAME vocabulary the write uses. A second list here drifted from
		// linkTargets and silently dropped two kinds: an activity could be
		// linked to a lead or a project and then be unfindable by filtering on
		// the very link that was just written.
		column := linkColumn(*in.EntityType)
		if column == "" {
			return "", nil, nil, &InvalidLinkTypeError{EntityType: *in.EntityType}
		}
		if *in.EntityType == string(datasource.RecordOrganization) {
			// An account's timeline is wider than its direct links: mail is
			// filed against the PERSON it was with, so a flat organization_id
			// match hides every message the company actually exchanged.
			// OrgLinkedActivityExists is the walk the company view's other
			// readers already use. EXISTS rather than a join, so an activity
			// reachable through two links stays one row and the keyset cursor
			// below keeps ordering over a stable set.
			where = append(where, OrgLinkedActivityExists(arg(*in.EntityID)))
		} else {
			join = ` JOIN activity_link al ON al.activity_id = a.id`
			where = append(where, sprintf("al.entity_type = $%d", arg(*in.EntityType)))
			where = append(where, sprintf("al.%s = $%d", column, arg(*in.EntityID)))
		}
	}
	if in.ThreadKey != nil && *in.ThreadKey != "" {
		where = append(where, sprintf("a.thread_key = $%d", arg(*in.ThreadKey)))
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
