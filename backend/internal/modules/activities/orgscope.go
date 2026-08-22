// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Which activities belong to which record — the timeline list's filter, and
// the account walk every other reader of the account's timeline shares.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// WHO may read one is auth.ActivityContentClause, which every caller composes
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
// The arms live apart from the two shapes built on them because that is where
// drift would happen — an arm gaining a condition in one spelling and not the
// other — while the shapes differ for a reason that will not go away
// (OrgReachSet says what it is).
//
// The deal arm deliberately does not exclude archived or lost deals: a set
// stricter than the predicate would show a message on the timeline whose
// account never gets a signal about it.
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
//
// Known limit, and it matters more to a producer than to a reader: the
// employment arm asks who a contact works for NOW, not who they worked for
// when the message was sent. Mail exchanged with someone at a previous job
// therefore reaches whoever employs them today. A timeline showing it is
// arguably being helpful; a signal FILED against that account is a claim
// nobody made. Bounding the arm by relationship.started_at is the fix, and it
// is not available yet — people.plantEmploymentEdge writes no start date, so
// the bound would resolve nothing (see the follow-up issue). Until then the
// extractor's one-account rule carries most of the weight, since a contact
// with two live employers makes their conversations ambiguous and skipped.
func OrgReachSet() string {
	return sprintf(`SELECT DISTINCT l.activity_id, o.org_id AS organization_id
		    %s
		    CROSS JOIN LATERAL (VALUES (l.organization_id), (d.organization_id),
		                              (r.organization_id)) AS o(org_id)
		    WHERE o.org_id IS NOT NULL`, orgArms)
}

// ActivityWithinProject is the ONE spelling of "this activity belongs to the
// body of work being asked about", for a query that aliases activity as a.
//
// The rule (D7): filed under THIS project, or filed under none. Not "not filed
// elsewhere" — those two readings differ on an activity linked to this project
// AND another, which this one keeps and that one drops.
// `uq_activity_link_project` makes that row unreachable today, but the rule is
// what the predicate owes its reader, so it is spelled as the rule rather than
// inferred from an index a later migration could relax without ever touching
// this file.
//
// KEEPING THE UNFILED ROWS IS THE POINT. Attribution is optional, so most
// correspondence on an account carries no project at all: a plain equality test
// would drop the general relationship history and describe an account as though
// nobody had ever spoken to it.
//
// It must be a subquery over the activity's links rather than a test on a link
// row already joined — `activity_link_shape` admits exactly ONE target per row,
// so a person-link row carries a NULL project_id by construction and a
// predicate on it would be true everywhere and narrow nothing. Both arms probe
// `uq_activity_link_project`, which is keyed on activity_id.
//
// search.projectScope.clause is the same predicate for the context walk. The
// two are deliberate copies, not an oversight: a module never imports a sibling
// (ADR-0054), and this rule is about SUBJECT MATTER rather than authority, so
// platform/auth — where the activity scope clauses both modules do share
// already live — is the wrong home for it. Change one, change both.
//
// projectPos is the bind position carrying the project id.
func ActivityWithinProject(projectPos int) string {
	return sprintf(`(
			EXISTS (
			    SELECT 1 FROM activity_link scoped
			    WHERE scoped.activity_id = a.id AND scoped.project_id = $%[1]d)
			OR NOT EXISTS (
			    SELECT 1 FROM activity_link filed
			    WHERE filed.activity_id = a.id AND filed.project_id IS NOT NULL))`,
		projectPos)
}

// RequireProjectScope is the authority check every read narrowed BY a project
// owes before it filters on one. Filtering by a record is a read of it: the
// scoped page answers "these activities are filed under this project", which
// a caller with no project grant may not learn, and a caller outside its row
// scope may not learn it exists. Object denial is a 403; an invisible,
// archived or missing project is the same existence-hiding 404 a direct read
// gives.
//
// The LIVE probe, not the plain one: EnsureVisible lets an unbounded caller
// through without touching the table, so a scope naming a project nobody ever
// created would answer a full page as though the filter had matched, and an
// archived project is no longer a body of work a page can be narrowed to.
func RequireProjectScope(ctx context.Context, tx pgx.Tx, projectID ids.ProjectID) error {
	if err := auth.Require(ctx, string(datasource.RecordProject), principal.ActionRead); err != nil {
		return err
	}
	return auth.EnsureVisibleLive(ctx, tx, string(datasource.RecordProject), projectID.UUID)
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
// bind arguments from one list input, plus the per-row audience test the
// SELECT projects as content_state.
func listActivitiesFilter(ctx context.Context, in ListActivitiesInput) (join string, where []string, content string, args []any, err error) {
	where = []string{"1=1"}
	args = []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	// The timeline is DISCOVER-gated: a row reachable through a record the
	// caller may read is listed, and whether its content comes with it is
	// the audience's call, answered per row (content_state). The free text
	// of a limited conversation is blanked by the scan, never selected into
	// the response.
	gate := auth.ActivityDiscoverClause
	if filtersOnContent(in) {
		// A filter over the subject, the body or the thread key is a READ
		// of them: a withheld row that matched would tell the caller what it
		// says through has_more and the page boundary. Such a list is
		// content-gated, so a limited row is simply not there.
		gate = auth.ActivityContentClause
	}
	scope, err := gate(ctx, "a", arg)
	if err != nil {
		return "", nil, "", nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	content, err = auth.ActivityAudienceArm(ctx, "a", arg)
	if err != nil {
		return "", nil, "", nil, err
	}
	if !in.IncludeArchived {
		where = append(where, "a.archived_at IS NULL")
	}
	if in.Kind != nil {
		where = append(where, sprintf("a.kind = $%d", arg(*in.Kind)))
	}
	if in.ChannelProvider != nil {
		where = append(where, sprintf("a.channel_provider = $%d", arg(*in.ChannelProvider)))
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
			return "", nil, "", nil, &InvalidLinkTypeError{EntityType: *in.EntityType}
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
	if in.WithinProjectID != nil {
		where = append(where, ActivityWithinProject(arg(*in.WithinProjectID)))
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
	if in.OccurredAfter != nil {
		where = append(where, sprintf("a.occurred_at >= $%d", arg(*in.OccurredAfter)))
	}
	if in.OccurredBefore != nil {
		where = append(where, sprintf("a.occurred_at < $%d", arg(*in.OccurredBefore)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, decodeErr := storekit.DecodeCursor(*in.Cursor)
		if decodeErr != nil {
			return "", nil, "", nil, decodeErr
		}
		where = append(where, sprintf("(a.occurred_at, a.id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}
	return join, where, content, args, nil
}

// filtersOnContent reports whether the list narrows on fields a withheld row
// does not disclose.
func filtersOnContent(in ListActivitiesInput) bool {
	return (in.ThreadKey != nil && *in.ThreadKey != "") || (in.Query != nil && *in.Query != "")
}
