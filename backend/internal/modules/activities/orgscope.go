// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Which activities belong to which record — the timeline list's filter, and
// the account walk every other reader of the account's timeline shares.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

// orgArms is the three links themselves — the account an activity is filed
// against, the account its deal belongs to, and the employer of the contact it
// is about.
//
// It is a separate constant because the walk is now asked two different
// questions. A predicate asks whether an activity reaches a KNOWN account; a
// producer asks which accounts an activity reaches at all, and needs them in
// its SELECT. The arms are where drift would actually happen — an arm gaining
// a condition in one spelling and not the other — so the arms are what is
// shared, and each question keeps its own shape around them.
//
// The deal arm deliberately does not exclude archived or lost deals: a
// fragment stricter than the predicate would show a message on the timeline
// whose account never gets a signal about it.
const orgArms = `FROM activity_link l
		    LEFT JOIN deal d ON d.id = l.deal_id
		    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		      AND r.ended_at IS NULL AND r.archived_at IS NULL`

// activityReachesOrg is the walk as a PREDICATE, for a query that aliases
// activity as a. operand is what each arm compares its organization id against
// — a single bind, or ANY(array).
//
// It stays an EXISTS rather than a join against OrgReachSet: EXISTS stops at
// the first arm that matches, and every one of this function's callers is a
// hot read.
func activityReachesOrg(operand string) string {
	return sprintf(`EXISTS (
		    SELECT 1 %s
		    WHERE l.activity_id = a.id
		      AND (l.organization_id = %[2]s OR d.organization_id = %[2]s OR r.organization_id = %[2]s))`,
		orgArms, operand)
}

// OrgReachSet is the same walk as a SET: the body of a derived table producing
// one (activity_id, organization_id) row per account an activity reaches.
//
// The predicate above answers "does this activity reach account X" and takes
// the account as a bind. A producer scanning the whole workspace has the
// opposite question — it holds an activity and needs the accounts — so it
// cannot use the predicate at all. Both are the same three arms.
//
// DISTINCT collapses an activity that reaches one account through several arms
// (its own link and its deal's, say) to one row, so a caller counting messages
// is not counting links. An activity that reaches TWO accounts is two rows on
// purpose: whether that is an ambiguity to refuse or a fact to file twice is
// the caller's ruling, not this fragment's.
//
// No entity_type filter: the activity_link_shape CHECK already guarantees
// exactly one of the three id columns is set per row, and the predicate omits
// it for the same reason.
//
// No workspace filter: activity_link, deal and relationship all carry FORCE
// row-level security, and every caller runs inside WithWorkspaceTx.
func OrgReachSet() string {
	return sprintf(`SELECT DISTINCT l.activity_id, o.org_id AS organization_id
		    %s
		    CROSS JOIN LATERAL (VALUES (l.organization_id), (d.organization_id),
		                              (r.organization_id)) AS o(org_id)
		    WHERE o.org_id IS NOT NULL`, orgArms)
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
