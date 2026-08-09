// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The activity anchor: preparing for a meeting by DEREFERENCING it.
//
// An activity is still a link, not a thing links hang off — graph.go's walk is
// unchanged and the graph keeps exactly the anchors it had. Naming an activity
// as the anchor asks a different question: which records is this event about?
// The event answers from its own links and participants, ONE of those records
// becomes the subject, and the ordinary record walk runs around that.
//
// The answer says what it chose and what it did not. `prepared_for` names the
// subject the walk used, `also_present` names every other record the event
// resolved to, and `unresolved_attendees` names the addresses that matched
// nobody — so an empty prep is actionable ("this event names nobody we hold,
// and here is who was on it") rather than silent.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// activitySubject is one record an event names, with the key that decides
// which of them the prep is built around.
type activitySubject struct {
	entityType string
	id         ids.UUID
	title      string
	// tier, named and role together are the precedence, most significant
	// first; id breaks the remaining ties so the choice is deterministic.
	tier  int
	named int
	role  int
}

// activityLinkArm is one record type an activity_link row can point at: the
// entity and the link column that reaches it.
type activityLinkArm struct {
	entity string
	column string
}

// title is the expression that renders this record type for a human, read off
// the module's one entity table rather than restated here — so a record named
// in a prep reads exactly as it reads in a search result and on an anchor
// profile. An entity with no branch has no title, which is a broken read the
// arm gate (TestEverySubjectLinkArmIsRanked) refuses before it can ship.
func (a activityLinkArm) title() string {
	for _, branch := range searchBranches {
		if branch.entity == a.entity {
			return branch.title
		}
	}
	return ""
}

// activityLinkArms is EVERY arm of activity_link, in no particular order —
// subjectTier decides the dereference precedence and relatedSectionOrder
// decides which of them the hop-2 walk reports.
//
// All five are here, and the completeness is load-bearing: the first draft of
// the dereference borrowed the hop-2 walk's shorter list and so dropped the
// lead arm (core 0038), which is exactly the discovery call a prep is most
// often for. TestEverySubjectLinkArmIsRanked holds this against the DDL's own
// enum rather than against a sibling list in Go.
var activityLinkArms = []activityLinkArm{
	{entity: string(datasource.EntityPerson), column: "person_id"},
	{entity: string(datasource.EntityOrganization), column: "organization_id"},
	{entity: string(datasource.EntityDeal), column: "deal_id"},
	{entity: string(datasource.EntityProject), column: "project_id"},
	{entity: string(datasource.EntityLead), column: "lead_id"},
}

// subjectTier orders record types by how much of the meeting they account for:
// the work first (a deal, then a project), then the account, then a single
// contact. A prep built around the deal answers what is at stake in the room;
// the same prep built around one attendee answers a smaller question.
//
// A lead comes last, below the person it may one day become: an event naming
// both has a promoted record to prepare against, and that is the one with a
// neighborhood. An event naming ONLY a lead still prepares against it — an
// honest "this is all we hold" beats naming nothing at all.
var subjectTier = map[string]int{
	string(datasource.EntityDeal):         0,
	string(datasource.EntityProject):      1,
	string(datasource.EntityOrganization): 2,
	string(datasource.EntityPerson):       3,
	string(datasource.EntityLead):         4,
}

// A link is something capture ASSERTED about the record; a participant is
// something it MATCHED from an address. An assertion outranks a match.
const (
	namedByLink        = 0
	namedByParticipant = 1
)

// participantRoleRank puts the party who convened the meeting — or sent the
// message — ahead of the ones who were invited to it.
var participantRoleRank = map[string]int{
	"organizer": 0, "from": 1, "to": 2, "cc": 3, "attendee": 4,
}

// unrankedRole is where a role the map does not name sorts: last among the
// participants, never ahead of a named one. The role vocabulary is a CHECK
// constraint (0157_activity_participant.up.sql) that may gain a member without
// this map hearing about it, and the ordering stays total when it does.
var unrankedRole = len(participantRoleRank)

// participantRoleOrder renders participantRoleRank as SQL, so the bounded
// participant window is CUT by role rather than by id.
//
// This exists because the two are not interchangeable at the boundary. The
// reads below stop at graphExpansionLimit rows like every other leg of the
// walk, and a fifty-attendee meeting whose organizer happens to hold a high id
// would have its organizer cut from the window before the Go-side precedence
// ever saw them — the prep would then be built around whichever attendee
// sorted first, which is the one outcome the organizer rule exists to prevent.
//
// It is rendered FROM the map rather than spelled a second time in SQL: two
// copies of an ordering are two chances for the window and the fold to
// disagree, and they would disagree silently.
func participantRoleOrder(alias string) string {
	var order strings.Builder
	order.WriteString("CASE " + alias + ".role")
	for _, role := range slices.Sorted(maps.Keys(participantRoleRank)) {
		fmt.Fprintf(&order, " WHEN '%s' THEN %d", role, participantRoleRank[role])
	}
	fmt.Fprintf(&order, " ELSE %d END", unrankedRole)
	return order.String()
}

// linkOnlyRole is the role slot a link carries. A link names no party, so it
// ranks ahead of every participant role within its tier — which is the same
// thing namedByLink already says, and saying it twice keeps the comparison a
// plain field-by-field one.
const linkOnlyRole = -1

// assembleActivityWithin builds the context for an activity anchor.
func (s *Store) assembleActivityWithin(ctx context.Context, tx pgx.Tx, activityID ids.UUID, maxItems int) ([]graphSection, error) {
	profile, err := activityProfile(ctx, tx, activityID)
	if err != nil {
		return nil, err
	}
	subjects, err := activitySubjects(ctx, tx, activityID)
	if err != nil {
		return nil, err
	}
	unresolved, err := unresolvedAttendees(ctx, tx, activityID)
	if err != nil {
		return nil, err
	}

	sections := []graphSection{{name: sectionProfile, items: []graphItem{profile}}}
	if len(subjects) > 0 {
		sections = append(sections, graphSection{
			name: "prepared_for", items: []graphItem{subjectItem(subjects[0])},
		})
		// max_items bounds this exactly as it bounds every other section, and
		// the order makes a cut survivable: the next-best subject first. The
		// contract states one per-section cap for the whole response, and a
		// section that quietly ignored it would be the surprise — a caller
		// preparing for a large meeting raises max_items rather than
		// discovering which sections opted out.
		if also := subjectItems(subjects[1:], maxItems); len(also) > 0 {
			sections = append(sections, graphSection{name: "also_present", items: also})
		}
	}
	if len(unresolved) > maxItems {
		unresolved = unresolved[:maxItems] // bounded like the rest; organizer first
	}
	if len(unresolved) > 0 {
		sections = append(sections, graphSection{name: "unresolved_attendees", items: unresolved})
	}
	if len(subjects) == 0 {
		return sections, nil
	}

	walk, err := s.assembleRecordWithin(ctx, tx, subjects[0].entityType, subjects[0].id, maxItems)
	if err != nil {
		return nil, err
	}
	// The event's own profile already opened the answer, and prepared_for
	// already names the subject; the subject's profile section would repeat it
	// under a heading that reads as the meeting's.
	for _, section := range walk {
		if section.name != sectionProfile {
			sections = append(sections, section)
		}
	}
	return sections, nil
}

// activityProfile is the existence and visibility gate for the whole assembly:
// an event the caller cannot see yields the same not-found any other anchor
// gives, never a leak of who was in someone else's meeting.
//
// EnsureActivityVisibleLive, not EnsureActivityVisible: this serves stored
// content, so an archived event must not answer and an unbounded actor does
// not skip the existence probe.
func activityProfile(ctx context.Context, tx pgx.Tx, activityID ids.UUID) (graphItem, error) {
	// Object RBAC before row scope: a caller with no read grant on activity at
	// all is denied the type (403), not handed the subset of events their row
	// scope would have admitted.
	if err := auth.Require(ctx, string(datasource.EntityActivity), principal.ActionRead); err != nil {
		return graphItem{}, err
	}
	if err := auth.EnsureActivityVisibleLive(ctx, tx, activityID); err != nil {
		return graphItem{}, err
	}
	var title, kind string
	var occurredAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT coalesce(subject, kind), kind, occurred_at
		  FROM activity WHERE id = $1 AND archived_at IS NULL`, activityID).
		Scan(&title, &kind, &occurredAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return graphItem{}, apperrors.ErrNotFound
		}
		return graphItem{}, fmt.Errorf("search: reading the event a prep is anchored on: %w", err)
	}
	// When it happens is half of what a prep is for, and the title alone does
	// not carry it — a subject line reads the same the day before and the week
	// after.
	return graphItem{
		entityType: string(datasource.EntityActivity), id: activityID,
		summary: fmt.Sprintf("%s — %s on %s", title, kind, occurredAt.UTC().Format(time.RFC3339)),
	}, nil
}

// activitySubjects resolves the records an event is about, best subject first.
//
// Every candidate is visibility-probed individually. The anchor's own scope is
// the ANY-link rule (auth.ActivityScopeClause), so a meeting linked to one deal
// the caller owns and one they do not is readable while the second deal is not:
// dereferencing widens context, never authority.
func activitySubjects(ctx context.Context, tx pgx.Tx, activityID ids.UUID) ([]activitySubject, error) {
	linked, err := linkedSubjects(ctx, tx, activityID)
	if err != nil {
		return nil, err
	}
	attending, err := participantSubjects(ctx, tx, activityID)
	if err != nil {
		return nil, err
	}
	return rankSubjects(ctx, tx, append(linked, attending...))
}

// linkedSubjects reads the records capture linked to the event, one bounded
// read per arm of activity_link.
func linkedSubjects(ctx context.Context, tx pgx.Tx, activityID ids.UUID) ([]activitySubject, error) {
	var out []activitySubject
	for _, arm := range activityLinkArms {
		// The title goes in unqualified, as anchorProfile spells it: the lead
		// arm's is an expression over several columns, and none of the title
		// columns collides with one on activity_link, so `t` is the only table
		// they can resolve against.
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT DISTINCT t.id, %s
			  FROM activity_link l JOIN %s t ON t.id = l.%s
			 WHERE l.activity_id = $1 AND l.%s IS NOT NULL AND t.archived_at IS NULL
			 ORDER BY t.id LIMIT %d`,
			arm.title(), arm.entity, arm.column, arm.column, graphExpansionLimit), activityID)
		if err != nil {
			return nil, fmt.Errorf("search: reading the records an event links to: %w", err)
		}
		for rows.Next() {
			subject := activitySubject{
				entityType: arm.entity, tier: subjectTier[arm.entity],
				named: namedByLink, role: linkOnlyRole,
			}
			if err := rows.Scan(&subject.id, &subject.title); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, subject)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// participantSubjects reads the people capture matched to the event's parties.
// There is no project or organization half of activity_participant — those
// reach a prep through activity_link like everything else.
func participantSubjects(ctx context.Context, tx pgx.Tx, activityID ids.UUID) ([]activitySubject, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.full_name, ap.role
		  FROM activity_participant ap JOIN person p ON p.id = ap.person_id
		 WHERE ap.activity_id = $1 AND p.archived_at IS NULL
		 ORDER BY `+participantRoleOrder("ap")+`, p.id LIMIT $2`, activityID, graphExpansionLimit)
	if err != nil {
		return nil, fmt.Errorf("search: reading the people on an event: %w", err)
	}
	defer rows.Close()
	var out []activitySubject
	for rows.Next() {
		subject := activitySubject{
			entityType: string(datasource.EntityPerson),
			tier:       subjectTier[string(datasource.EntityPerson)], named: namedByParticipant,
		}
		var role string
		if err := rows.Scan(&subject.id, &subject.title, &role); err != nil {
			return nil, err
		}
		rank, ok := participantRoleRank[role]
		if !ok {
			rank = unrankedRole
		}
		subject.role = rank
		out = append(out, subject)
	}
	return out, rows.Err()
}

// rankSubjects drops what the caller may not see, folds the duplicates, and
// orders what is left.
//
// A record reached twice — linked AND on the invitation, or copied under two
// roles — is ONE subject at its best rank. Without the fold the same account
// would appear in also_present beside itself, and a prep that lists the same
// company twice reads as two accounts.
// Two gates, and they answer different questions. Object RBAC asks whether the
// caller may read that KIND of record at all, and a denied kind is absent
// rather than a 403 — a subject is context the caller did not ask for by name.
// The row-scope probe then asks whether they may read THAT record.
//
// foldSubjects returns them in precedence order and this only ever drops, so
// the order survives and needs no second sort.
func rankSubjects(ctx context.Context, tx pgx.Tx, candidates []activitySubject) ([]activitySubject, error) {
	out := make([]activitySubject, 0, len(candidates))
	for _, subject := range foldSubjects(candidates) {
		if auth.Require(ctx, subject.entityType, principal.ActionRead) != nil {
			continue
		}
		visible, err := auth.VisibleTo(ctx, tx, subject.entityType, subject.id)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, subject)
		}
	}
	return out, nil
}

// foldSubjects reduces the candidates to one entry per record, at its best
// rank, in a deterministic order — the pure half of rankSubjects, so the
// precedence can be proven without a database.
func foldSubjects(candidates []activitySubject) []activitySubject {
	best := map[datasource.EntityRef]activitySubject{}
	for _, candidate := range candidates {
		ref := datasource.EntityRef{Type: datasource.EntityType(candidate.entityType), ID: candidate.id}
		if held, seen := best[ref]; seen && !subjectPrecedes(candidate, held) {
			continue
		}
		best[ref] = candidate
	}
	out := make([]activitySubject, 0, len(best))
	for _, subject := range best {
		out = append(out, subject)
	}
	sort.Slice(out, func(i, j int) bool { return subjectPrecedes(out[i], out[j]) })
	return out
}

// subjectPrecedes is the precedence, most significant first: the tier of
// record, then whether the event asserted the record or merely matched it,
// then the party's role, then the id so the order is total.
func subjectPrecedes(a, b activitySubject) bool {
	switch {
	case a.tier != b.tier:
		return a.tier < b.tier
	case a.named != b.named:
		return a.named < b.named
	case a.role != b.role:
		return a.role < b.role
	default:
		return a.id.String() < b.id.String()
	}
}

func subjectItem(subject activitySubject) graphItem {
	return graphItem{entityType: subject.entityType, id: subject.id, summary: subject.title}
}

// subjectItems renders a run of subjects, bounded. The order is the
// precedence's, so the bound keeps the best of them — sortAndTrim is
// deliberately not used, because these items carry no §10.7.2 rank score and a
// zero would reorder them by id.
func subjectItems(subjects []activitySubject, maxItems int) []graphItem {
	if len(subjects) > maxItems {
		subjects = subjects[:maxItems]
	}
	items := make([]graphItem, 0, len(subjects))
	for _, subject := range subjects {
		items = append(items, subjectItem(subject))
	}
	return items
}

// unresolvedAttendees reads the addresses on the event that matched no record.
//
// This is a deliberate disclosure, not a leak. The addresses are content of an
// event the caller has already been admitted to read, and returning them is
// what makes an empty prep actionable: an agent holding them can call
// resolve_entities, where withholding them would answer a prep with silence.
//
// The items carry the EVENT as their ref, because an attendee we hold no
// record for has no id of their own to name — the ref says where the address
// came from, and the summary is the address and the part they played.
//
// A party matched to a person the caller cannot see is neither here nor a
// subject: it resolved to a record, and reclassifying it as an unmatched
// address would disclose by the back door exactly what the row scope withheld.
// Colleagues (user_id) are likewise absent — they resolved to a member.
func unresolvedAttendees(ctx context.Context, tx pgx.Tx, activityID ids.UUID) ([]graphItem, error) {
	// DISTINCT ON the address, not on (address, role): one party copied as both
	// `to` and `cc` is one person in the room, and listing them twice reads as
	// two. The role kept is the most significant one they held, which is also
	// the order the window is cut by.
	rows, err := tx.Query(ctx, `
		SELECT address, role FROM (
		    SELECT DISTINCT ON (ap.address) ap.address, ap.role, `+participantRoleOrder("ap")+` AS rank
		      FROM activity_participant ap
		     WHERE ap.activity_id = $1
		       AND ap.person_id IS NULL AND ap.user_id IS NULL AND ap.address IS NOT NULL
		     ORDER BY ap.address, rank
		) parties
		 ORDER BY rank, address LIMIT $2`, activityID, graphExpansionLimit)
	if err != nil {
		return nil, fmt.Errorf("search: reading the addresses on an event that matched nobody: %w", err)
	}
	defer rows.Close()
	var out []graphItem
	for rows.Next() {
		var address, role string
		if err := rows.Scan(&address, &role); err != nil {
			return nil, err
		}
		out = append(out, graphItem{
			entityType: string(datasource.EntityActivity), id: activityID,
			summary: fmt.Sprintf("%s — %s", address, role),
		})
	}
	return out, rows.Err()
}
