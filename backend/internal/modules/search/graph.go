// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The §10.7.2 retrieval-ranking tunables:
// score = 0.60·similarity + 0.30·recency + 0.10·source_trust, id-ascending
// tie-break. Graph items carry no query similarity (there is no query),
// so their rank is recency + trust over the same weights.
const (
	wRankSim   = 0.60
	wRankRec   = 0.30
	wRankTrust = 0.10
	// recencyHalfLifeDays reuses the §4 relationship-strength primitive:
	// a touch loses half its recency weight every 30 days.
	recencyHalfLifeDays = 30
)

// sourceTrust maps provenance channels to the trust factor: a human
// statement outranks an agent write outranks captured external content
// (the T0/T1/T2 ladder projected onto activity.source).
var sourceTrust = map[string]float64{
	"manual": 1.0,
	"mcp":    0.7,
}

const defaultSourceTrust = 0.4 // captured/connector content — T2

func rankScore(similarity float64, occurredAt time.Time, source string, now time.Time) float64 {
	days := now.Sub(occurredAt).Hours() / 24
	if days < 0 {
		days = 0
	}
	recency := math.Exp2(-days / recencyHalfLifeDays)
	trust, ok := sourceTrust[source]
	if !ok {
		trust = defaultSourceTrust
	}
	return wRankSim*similarity + wRankRec*recency + wRankTrust*trust
}

type graphSection struct {
	name  string
	items []graphItem
}

// sectionProfile names the section every walk opens with: what the anchor IS,
// before anything around it. Named because the activity anchor both emits one
// and drops the one its subject's walk emits (graphactivity.go).
const sectionProfile = "profile"

type graphItem struct {
	entityType string
	id         ids.UUID
	summary    string
	score      float64
}

// graphExpansionLimit caps EVERY leg of the fixed-depth walk — the
// activity timeline and each hop-2 relationship expansion alike. A
// graph view is a window onto the neighborhood, not an export: each leg
// reads at most this many rows and ranking trims further, so an anchor
// with thousands of links costs the same as one with fifty.
const graphExpansionLimit = 50

// anchorLinkColumn names the activity_link column an anchor type walks.
var anchorLinkColumn = map[string]string{
	string(datasource.EntityPerson):       "person_id",
	string(datasource.EntityOrganization): "organization_id",
	string(datasource.EntityDeal):         "deal_id",
	string(datasource.EntityProject):      "project_id",
}

// assembleGraph is the fixed-depth context walk (B-EP05.20a): anchor →
// linked activities (hop 1) → those activities' other link targets
// (hop 2). Depth is fixed by construction — two joins, not a traversal
// that can wander. Activities ride the activity link-walk scope; hop-2
// records are visibility-probed individually.
//
// An ACTIVITY anchor takes the other road (graphactivity.go): it names no
// neighborhood of its own, so it is dereferenced to the records it is about
// and the walk below runs around one of those.
func (s *Store) assembleGraph(ctx context.Context, anchorType string, anchorID ids.UUID, maxItems int) ([]graphSection, error) {
	var sections []graphSection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if anchorType == string(datasource.EntityActivity) {
			sections, err = s.assembleActivityWithin(ctx, tx, anchorID, maxItems)
			return err
		}
		sections, err = s.assembleRecordWithin(ctx, tx, anchorType, anchorID, maxItems)
		return err
	})
	if err != nil {
		return nil, err
	}
	return sections, nil
}

// assembleRecordWithin is the record half of the walk, on a transaction the
// caller owns — so an activity anchor can dereference to a record and walk it
// without opening a second transaction against the same read.
func (s *Store) assembleRecordWithin(ctx context.Context, tx pgx.Tx, anchorType string, anchorID ids.UUID, maxItems int) ([]graphSection, error) {
	// A record graph anchor is any searchable record type except activity
	// itself (an activity is a link, not a thing links hang off).
	var branch *searchBranch
	for i := range searchBranches {
		if searchBranches[i].entity == anchorType && !searchBranches[i].activityWalk {
			branch = &searchBranches[i]
		}
	}
	if branch == nil {
		// The handler pre-validates anchorType against the module's entity
		// table, so this is unreachable in practice — but an unmapped raw
		// error would still surface as an opaque 500 if some caller ever
		// slipped past that gate, so the store layer answers with the same
		// sentinel every other invalid-record-reference case in this module
		// uses.
		return nil, fmt.Errorf("search: %s is not a graph anchor: %w", anchorType, apperrors.ErrNotFound)
	}
	// Object RBAC before row scope, the order every read in this module takes
	// (Search's branch admission spells it the same way). The row-scope probe
	// below answers a different question — WHICH rows of a type — and a caller
	// with no grant on the type at all must not reach it.
	if err := auth.Require(ctx, anchorType, principal.ActionRead); err != nil {
		return nil, err
	}
	// anchorLinkColumn is what this walk can READ, not what activity_link can
	// hold: the link shape has admitted a lead arm since core 0038, and this
	// walk does not follow it, so a lead anchor's context is its profile alone.
	// That is an honestly-empty neighborhood rather than a walk silently
	// skipped — and it is a gap, tracked rather than restated as a property.
	linkCol, walkable := anchorLinkColumn[anchorType]
	now := time.Now().UTC()

	title, err := anchorProfile(ctx, tx, branch, anchorType, anchorID)
	if err != nil {
		return nil, err
	}
	sections := []graphSection{{name: sectionProfile, items: []graphItem{{
		entityType: anchorType, id: anchorID, summary: title,
	}}}}

	// Who on our team knows this contact (ADR-0078). Without this the
	// projection is invisible to the assistant: a rep can see the answer
	// on the person page while the model answering "who should introduce
	// me" has no access to it at all, and confidently says nobody.
	//
	// Person anchors only. An organization's or a deal's colleagues are a
	// join across its contacts, which is a compose read — and a module
	// never imports a sibling to make one.
	if anchorType == string(datasource.EntityPerson) {
		knows, err := whoKnowsSection(ctx, tx, anchorID, maxItems, now)
		if err != nil {
			return nil, err
		}
		sections = append(sections, knows)
	}

	if !walkable {
		return sections, nil
	}

	touches, openTasks, activityIDs, err := anchorTimeline(ctx, tx, linkCol, anchorID, maxItems, now)
	if err != nil {
		return nil, err
	}
	sections = append(sections,
		graphSection{name: "recent_touches", items: touches},
		graphSection{name: "open_tasks", items: openTasks})

	// Hop 2: the other ends of those activities' links — the people
	// and organizations in the same conversations. Each is
	// visibility-probed: the walk widens context, never authority.
	related, err := s.relatedViaLinks(ctx, tx, anchorType, anchorID, activityIDs, maxItems)
	if err != nil {
		return nil, err
	}
	return append(sections, related...), nil
}

// anchorProfile reads the anchor's title, and is the existence and visibility
// gate for the whole assembly: an anchor the caller cannot see yields nothing.
func anchorProfile(ctx context.Context, tx pgx.Tx, branch *searchBranch, anchorType string, anchorID ids.UUID) (string, error) {
	if err := auth.EnsureVisible(ctx, tx, anchorType, anchorID); err != nil {
		return "", err
	}
	var title string
	err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM %s t WHERE t.id = $1 AND t.archived_at IS NULL`, branch.title, branch.table),
		anchorID).Scan(&title)
	if err != nil {
		// EnsureVisible answers nil (not merely "visible") when the caller's
		// scope clause is empty — an unrestricted viewer's full-access grant,
		// not proof the row exists. This is the real existence check: an anchor
		// id nobody wrote resolves to the same not-found every other
		// single-record read gives, never a raw scan error.
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperrors.ErrNotFound
		}
		return "", err
	}
	return title, nil
}

// anchorTimeline is hop 1: the anchor's activity timeline, scope-walked
// and ranked by recency × trust (§10.7.2 with similarity = 0), split
// into recent touches and open tasks.
func anchorTimeline(ctx context.Context, tx pgx.Tx, linkCol string, anchorID ids.UUID, maxItems int, now time.Time) (touches, openTasks []graphItem, activityIDs []ids.ActivityID, err error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	anchorPos := arg(anchorID)
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, nil, nil, err
	}
	activitySQL := fmt.Sprintf(`
		SELECT a.id, coalesce(a.subject, a.kind), a.kind, a.is_done, a.occurred_at, a.source
		FROM activity a JOIN activity_link l ON l.activity_id = a.id
		WHERE l.%s = $%d AND a.archived_at IS NULL`, linkCol, anchorPos)
	if scope != "" {
		activitySQL += " AND " + scope
	}
	activitySQL += fmt.Sprintf(" ORDER BY a.occurred_at DESC LIMIT %d", graphExpansionLimit)
	rows, err := tx.Query(ctx, activitySQL, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.ActivityID
		var summary, kind, source string
		var isDone bool
		var occurredAt time.Time
		if err := rows.Scan(&id, &summary, &kind, &isDone, &occurredAt, &source); err != nil {
			return nil, nil, nil, err
		}
		activityIDs = append(activityIDs, id)
		// graphItem.id is the polymorphic result column (activity here,
		// person/organization/deal on the hop-2 sections), so it carries
		// the untyped UUID.
		item := graphItem{
			entityType: string(datasource.EntityActivity), id: id.UUID, summary: summary,
			score: rankScore(0, occurredAt, source, now),
		}
		if kind == "task" && !isDone {
			openTasks = append(openTasks, item)
			continue
		}
		touches = append(touches, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	sortAndTrim(&touches, maxItems)
	sortAndTrim(&openTasks, maxItems)
	return touches, openTasks, activityIDs, nil
}

// whoKnowsSection reads the colleagues who interact with this contact, warmest
// first, as context items a model can quote.
//
// Over-fetch, rank, THEN cap — the same order the HTTP surface takes and for
// the same reason: warmth is computed at read, so capping in SQL would cap by
// last contact and evict the colleague who has worked the account for a year
// in favour of whoever sent the most recent one-line reply.
//
// The anchor's own visibility was already established above, and EdgesForPerson
// re-gates on the person grant, so a contact the caller cannot read never
// reaches this and its colleagues are never named.
func whoKnowsSection(ctx context.Context, tx pgx.Tx, personID ids.UUID, maxItems int, now time.Time) (graphSection, error) {
	section := graphSection{name: "who_knows"}
	edges, err := EdgesForPerson(ctx, tx, personID, graphExpansionLimit)
	if err != nil {
		return section, err
	}
	SortByStrength(edges, now)
	if len(edges) > maxItems {
		edges = edges[:maxItems]
	}
	names, err := MemberNames(ctx, tx, edges)
	if err != nil {
		return section, err
	}
	for _, e := range edges {
		score := e.StrengthOf(now)
		section.items = append(section.items, graphItem{
			entityType: "user", id: e.UserID,
			// The band and the count travel WITH the name. A bare list of
			// colleagues cannot be ranked by whoever reads it next, and a model
			// handed one picks the first.
			summary: fmt.Sprintf("%s — %s relationship, %d interactions in the last %d days",
				names[e.UserID], score.Bucket, e.Count90d, relstrength.WindowDays),
			// Already ranked by warmth; the section is emitted in that order and
			// sortAndTrim is deliberately not applied, because these items carry
			// no §10.7.2 rank score and a zero would reorder them by id.
		})
	}
	return section, nil
}

// MemberNames resolves the display names of the colleagues on a set of edges.
// The roster is readable by any authenticated member, so naming a colleague on
// a contact the caller can already open discloses nothing new.
//
// Exported because compose needs the same map for the agent seams, and two
// copies of one query are two places for the disclosure argument above to be
// re-decided differently.
func MemberNames(ctx context.Context, tx pgx.Tx, edges []InteractionEdge) (map[ids.UUID]string, error) {
	out := map[ids.UUID]string{}
	if len(edges) == 0 {
		return out, nil
	}
	users := make([]ids.UUID, 0, len(edges))
	for _, e := range edges {
		users = append(users, e.UserID)
	}
	rows, err := tx.Query(ctx, `SELECT id, display_name FROM app_user WHERE id = ANY($1)`, users)
	if err != nil {
		return nil, fmt.Errorf("search: naming the colleagues who know a contact: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// relatedSectionOrder is the hop-2 neighbor types the walk renders a related_*
// section for, in the order they are emitted.
//
// It is a SUBSET of activity_link's arms (activityLinkArms) and the walk skips
// the rest: a project or a lead reached at hop 2 has nowhere to be reported, so
// reading them would be a query whose result is discarded.
var relatedSectionOrder = []string{
	string(datasource.EntityPerson),
	string(datasource.EntityOrganization),
	string(datasource.EntityDeal),
}

func (s *Store) relatedViaLinks(ctx context.Context, tx pgx.Tx, anchorType string, anchorID ids.UUID, activityIDs []ids.ActivityID, maxItems int) ([]graphSection, error) {
	if len(activityIDs) == 0 {
		return nil, nil
	}
	sectionsByType := map[string][]graphItem{}
	for _, hop := range activityLinkArms {
		if hop.entity == anchorType || !slices.Contains(relatedSectionOrder, hop.entity) {
			continue // the anchor is not its own neighbor, and neither is a type with no section
		}
		// Object RBAC hides a denied type SILENTLY here, unlike at the anchor:
		// a neighbor is context the caller did not ask for by name, so a type
		// they hold no grant on is absent rather than a 403 on a read they did
		// not make. Search's branch admission takes the same posture.
		if auth.Require(ctx, hop.entity, principal.ActionRead) != nil {
			continue
		}
		items, err := hopNeighbors(ctx, tx, hop, anchorID, activityIDs)
		if err != nil {
			return nil, err
		}
		sectionsByType[hop.entity] = items
	}
	var out []graphSection
	for _, entity := range relatedSectionOrder {
		items := sectionsByType[entity]
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool { return items[i].id.String() < items[j].id.String() })
		if len(items) > maxItems {
			items = items[:maxItems]
		}
		out = append(out, graphSection{name: "related_" + plural(entity), items: items})
	}
	return out, nil
}

// hopNeighbors reads one hop's bounded, deterministic candidate window and
// returns the visible ones as graph items. Each candidate is
// visibility-probed individually: the walk widens context, never authority.
func hopNeighbors(ctx context.Context, tx pgx.Tx, hop activityLinkArm, anchorID ids.UUID, activityIDs []ids.ActivityID) ([]graphItem, error) {
	// Bounded like the activity leg: the id order makes the window
	// deterministic before the per-row visibility probe thins it.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT t.id, t.%s
		FROM activity_link l JOIN %s t ON t.id = l.%s
		WHERE l.activity_id = ANY($1) AND t.archived_at IS NULL AND l.%s IS NOT NULL AND t.id <> $2
		ORDER BY t.id LIMIT %d`,
		hop.title(), hop.entity, hop.column, hop.column, graphExpansionLimit), activityIDs, anchorID)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id    ids.UUID
		title string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var items []graphItem
	for _, c := range candidates {
		visible, err := auth.VisibleTo(ctx, tx, hop.entity, c.id)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		items = append(items, graphItem{entityType: hop.entity, id: c.id, summary: c.title})
	}
	return items, nil
}

// sortAndTrim orders by score descending with the §10.7.2 id-ascending
// tie-break, then bounds the section.
func sortAndTrim(items *[]graphItem, maxItems int) {
	list := *items
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].id.String() < list[j].id.String()
	})
	if len(list) > maxItems {
		list = list[:maxItems]
	}
	*items = list
}

func plural(entity string) string {
	if strings.HasSuffix(entity, "person") {
		return "people"
	}
	return entity + "s"
}
