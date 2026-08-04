// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The local graph around one contact (ADR-0078 / PO-EXT-3).
//
// It answers ONE question — who here can open a door to this person, and what
// is the evidence they really know them. A diagram of the whole network
// answers nothing; this one is shaped by the question and stops where the
// question stops.
//
// Two groups, and the second exists because the first is so often empty.
// `direct` is the colleagues who have corresponded with the contact
// themselves. `account` is the other contacts at their employer and which
// colleague is warmest with each — the route when nobody here knows the person
// but somebody knows the person sitting next to them.
//
// Row scope is applied PER GROUP rather than once at the root. A root-only
// check would let the account arm name contacts the caller may not read, which
// is the leak this shape exists to avoid: the graph would disclose by
// adjacency exactly what the record list withholds.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The caps. Both are reading limits: past ten colleagues or a dozen
// colleagues-at-the-company nobody is choosing a route any more, they are
// reading an org chart.
const (
	graphDirectCap  = 10
	graphAccountCap = 12
	// Over-fetch before ranking, for the reason SortByStrength documents: the
	// projection is ordered by recency, so capping in SQL would evict a
	// genuinely warmer colleague in favour of a recent one-line reply.
	graphDirectFetch = 100
	// receiptsPerEdge bounds the proof shown for one relationship. Three
	// messages settle "do they actually know them"; a fourth is an archive.
	receiptsPerEdge = 3
)

// GetPersonGraph implements GET /people/{id}/graph.
func (h Reads) GetPersonGraph(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	personID := ids.From[ids.PersonKind](ids.UUID(id))
	now := h.now()
	out := crmcontracts.PersonGraph{
		PersonId:      id,
		Nodes:         []crmcontracts.PersonGraphNode{},
		Edges:         []crmcontracts.PersonGraphEdge{},
		GroupsOmitted: []crmcontracts.PersonGraphGroupsOmitted{},
	}

	ctx := r.Context()
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.buildPersonGraph(ctx, tx, personID, now, &out)
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// buildPersonGraph assembles both groups inside one transaction, so the
// picture describes one moment rather than a stack of independently-timed
// reads.
func (h Reads) buildPersonGraph(
	ctx context.Context,
	tx pgx.Tx,
	personID ids.PersonID,
	now time.Time,
	out *crmcontracts.PersonGraph,
) error {
	// The anchor read is mandatory and its refusal is the whole read's
	// refusal: a graph around a contact the caller cannot see must 404 rather
	// than return an empty picture, which would confirm the record exists.
	anchor, err := anchorNode(ctx, tx, personID)
	if err != nil {
		return err
	}
	out.Nodes = append(out.Nodes, anchor)

	dropped := struct {
		Account *int `json:"account,omitempty"`
		Direct  *int `json:"direct,omitempty"`
	}{}

	directDropped, err := h.addDirectGroup(ctx, tx, personID, now, out)
	switch {
	case err == nil:
		dropped.Direct = &directDropped
	case isDenied(err):
		out.GroupsOmitted = append(out.GroupsOmitted, crmcontracts.PersonGraphGroupsOmittedDirect)
	default:
		return err
	}

	accountDropped, err := h.addAccountGroup(ctx, tx, personID, now, out)
	switch {
	case err == nil:
		dropped.Account = &accountDropped
	case isDenied(err):
		out.GroupsOmitted = append(out.GroupsOmitted, crmcontracts.PersonGraphGroupsOmittedAccount)
	default:
		return err
	}

	out.DroppedCount = &dropped
	out.Route = chooseRoute(out, now)
	return nil
}

// isDenied reports the one failure a group survives: the caller lacks its
// grant, so the group is named as withheld rather than returned empty. Any
// other fault fails the read, because half a graph is worse than an error —
// the reader cannot tell which half is missing.
func isDenied(err error) bool {
	return err != nil && errors.Is(err, apperrors.ErrPermissionDenied)
}

// anchorNode reads the contact this graph is about.
func anchorNode(ctx context.Context, tx pgx.Tx, personID ids.PersonID) (crmcontracts.PersonGraphNode, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.PersonGraphNode{}, err
	}
	if err := auth.EnsureVisibleLive(ctx, tx, "person", personID.UUID); err != nil {
		return crmcontracts.PersonGraphNode{}, err
	}
	var name string
	var title *string
	if err := tx.QueryRow(ctx,
		`SELECT full_name, title FROM person WHERE id = $1`, personID).Scan(&name, &title); err != nil {
		return crmcontracts.PersonGraphNode{}, fmt.Errorf("network: reading the contact a graph is about: %w", err)
	}
	pid := openapi_types.UUID(personID.UUID)
	return crmcontracts.PersonGraphNode{
		Id:       personNodeID(personID.UUID),
		Type:     crmcontracts.PersonGraphNodeTypeContact,
		Group:    crmcontracts.PersonGraphNodeGroupAnchor,
		Label:    name,
		Sublabel: title,
		PersonId: &pid,
	}, nil
}

// addDirectGroup adds the colleagues who have corresponded with this contact,
// warmest first, each edge carrying the messages behind it.
func (h Reads) addDirectGroup(
	ctx context.Context,
	tx pgx.Tx,
	personID ids.PersonID,
	now time.Time,
	out *crmcontracts.PersonGraph,
) (int, error) {
	edges, err := search.EdgesForPerson(ctx, tx, personID.UUID, graphDirectFetch)
	if err != nil {
		return 0, err
	}
	search.SortByStrength(edges, now)
	dropped := 0
	if len(edges) > graphDirectCap {
		dropped = len(edges) - graphDirectCap
		edges = edges[:graphDirectCap]
	}
	names, err := userNames(ctx, tx, edgeUsers(edges))
	if err != nil {
		return 0, err
	}
	for _, e := range edges {
		out.Nodes = append(out.Nodes, colleagueNode(e.UserID, names[e.UserID], crmcontracts.PersonGraphNodeGroupDirect))
		receipts, err := edgeReceipts(ctx, tx, e.UserID, e.PersonID)
		if err != nil {
			return 0, err
		}
		edge := wireEdge(e, userNodeID(e.UserID), personNodeID(e.PersonID), now)
		edge.Receipts = &receipts
		out.Edges = append(out.Edges, edge)
	}
	return dropped, nil
}

// addAccountGroup adds the other contacts at this person's current employer,
// each with the colleague who knows them best.
//
// The contacts are row-scoped in the query itself rather than probed
// afterwards: a contact outside the caller's scope must be ABSENT, and
// fetching then filtering leaks the count through the dropped total.
func (h Reads) addAccountGroup(
	ctx context.Context,
	tx pgx.Tx,
	personID ids.PersonID,
	now time.Time,
	out *crmcontracts.PersonGraph,
) (int, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return 0, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return 0, err
	}
	if scope == "" {
		scope = "true"
	}
	limitPos := arg(graphAccountCap + 1)

	// The employer is whichever organization this contact currently works for,
	// and "currently" is an employment edge nobody has ended. is_current_primary
	// answers a different question — which of several employers is the main one —
	// and keying on it would drop a real colleague who holds a second post.
	//
	// Colleagues are the OTHER current employees of it: the person themselves is
	// the anchor and must not appear twice.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT p.id, p.full_name, p.title
		  FROM relationship theirs
		  JOIN relationship colleague
		    ON colleague.organization_id = theirs.organization_id
		   AND colleague.kind = 'employment'
		   AND colleague.ended_at IS NULL
		   AND colleague.archived_at IS NULL
		  JOIN person p ON p.id = colleague.person_id AND p.archived_at IS NULL
		 WHERE theirs.person_id = $%d
		   AND theirs.kind = 'employment'
		   AND theirs.ended_at IS NULL
		   AND theirs.archived_at IS NULL
		   AND p.id <> $%d
		   AND (%s)
		 ORDER BY p.full_name, p.id
		 LIMIT $%d`, personPos, personPos, scope, limitPos), args...)
	if err != nil {
		return 0, fmt.Errorf("network: reading who else works at a contact's company: %w", err)
	}
	defer rows.Close()

	type contact struct {
		id    ids.UUID
		name  string
		title *string
	}
	var contacts []contact
	for rows.Next() {
		var c contact
		if err := rows.Scan(&c.id, &c.name, &c.title); err != nil {
			return 0, fmt.Errorf("network: reading a colleague of a contact: %w", err)
		}
		contacts = append(contacts, c)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("network: reading the colleagues of a contact: %w", err)
	}
	dropped := 0
	if len(contacts) > graphAccountCap {
		dropped = len(contacts) - graphAccountCap
		contacts = contacts[:graphAccountCap]
	}
	if len(contacts) == 0 {
		return dropped, nil
	}

	ids := make([]ids.UUID, 0, len(contacts))
	for _, c := range contacts {
		ids = append(ids, c.id)
	}
	// One read for every account contact's edges rather than one per contact:
	// the answer is a ranking across all of them, so they are gathered
	// together and ranked here.
	edges, err := search.EdgesForPeople(ctx, tx, ids)
	if err != nil {
		return 0, err
	}
	names, err := userNames(ctx, tx, edgeUsers(edges))
	if err != nil {
		return 0, err
	}
	for _, c := range contacts {
		pid := openapi_types.UUID(c.id)
		out.Nodes = append(out.Nodes, crmcontracts.PersonGraphNode{
			Id:       personNodeID(c.id),
			Type:     crmcontracts.PersonGraphNodeTypeContact,
			Group:    crmcontracts.PersonGraphNodeGroupAccount,
			Label:    c.name,
			Sublabel: c.title,
			PersonId: &pid,
		})
	}
	for _, e := range edges {
		// A colleague can reach the contact directly AND know somebody else at
		// the same company. They are one person and get one node; the two
		// edges hang off it.
		if !hasNode(out, userNodeID(e.UserID)) {
			out.Nodes = append(out.Nodes,
				colleagueNode(e.UserID, names[e.UserID], crmcontracts.PersonGraphNodeGroupAccount))
		}
		// No receipts on this arm, and that is the disclosure rule rather than
		// an omission: pooled interaction metadata may be shown where the
		// correspondence behind it may not (ADR-0078 §124). The counts say a
		// route exists; reading the mail is the timeline's decision to make.
		out.Edges = append(out.Edges, wireEdge(e, userNodeID(e.UserID), personNodeID(e.PersonID), now))
	}
	return dropped, nil
}

// hasNode reports whether a node id is already in the graph.
func hasNode(out *crmcontracts.PersonGraph, id string) bool {
	for _, n := range out.Nodes {
		if n.Id == id {
			return true
		}
	}
	return false
}

// edgeReceipts names up to three messages behind one colleague-contact pair.
//
// Each candidate passes the caller's own activity row scope INSIDE the query.
// The edge's counts are pooled metadata and disclosable; the messages are
// correspondence and are not, so an activity outside the caller's scope is
// absent from the receipts while still counted in the total. That difference
// is the whole reason receipts are a separate read rather than a join.
func edgeReceipts(ctx context.Context, tx pgx.Tx, userID, personID ids.UUID) ([]crmcontracts.PersonGraphReceipt, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		// No activity grant is not a reason to fail the graph: the edge's
		// counts stand on the person grant alone, and the receipts are the
		// part this caller may not have.
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return []crmcontracts.PersonGraphReceipt{}, nil
		}
		return nil, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	userPos, personPos := arg(userID), arg(personID)
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = "true"
	}
	limitPos := arg(receiptsPerEdge)

	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, a.subject, a.occurred_at
		  FROM activity a
		 WHERE a.archived_at IS NULL
		   AND EXISTS (SELECT 1 FROM activity_participant up
		                WHERE up.activity_id = a.id AND up.user_id = $%d)
		   AND EXISTS (SELECT 1 FROM activity_participant pp
		                WHERE pp.activity_id = a.id AND pp.person_id = $%d)
		   AND (%s)
		 ORDER BY a.occurred_at DESC, a.id DESC
		 LIMIT $%d`, userPos, personPos, scope, limitPos), args...)
	if err != nil {
		return nil, fmt.Errorf("network: reading the messages behind a relationship: %w", err)
	}
	defer rows.Close()

	out := []crmcontracts.PersonGraphReceipt{}
	for rows.Next() {
		var r crmcontracts.PersonGraphReceipt
		var activityID ids.UUID
		if err := rows.Scan(&activityID, &r.Subject, &r.OccurredAt); err != nil {
			return nil, fmt.Errorf("network: reading a message behind a relationship: %w", err)
		}
		r.ActivityId = openapi_types.UUID(activityID)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("network: reading the messages behind a relationship: %w", err)
	}
	return out, nil
}

// chooseRoute picks the warmest way in.
//
// Deterministic, and the preference order is the whole recommendation: a
// direct relationship beats an indirect one however warm the indirect one
// looks, because "she already knows you" is a different kind of claim from
// "he knows someone at her company".
func chooseRoute(out *crmcontracts.PersonGraph, now time.Time) *crmcontracts.PersonGraphRoute {
	anchor := ""
	labels := map[string]string{}
	groups := map[string]crmcontracts.PersonGraphNodeGroup{}
	users := map[string]*openapi_types.UUID{}
	people := map[string]*openapi_types.UUID{}
	for i := range out.Nodes {
		n := &out.Nodes[i]
		labels[n.Id] = n.Label
		groups[n.Id] = n.Group
		users[n.Id] = n.UserId
		people[n.Id] = n.PersonId
		if n.Group == crmcontracts.PersonGraphNodeGroupAnchor {
			anchor = n.Id
		}
	}

	var best *crmcontracts.PersonGraphEdge
	bestDirect := false
	for i := range out.Edges {
		e := &out.Edges[i]
		direct := e.To == anchor
		switch {
		case best == nil:
		case direct && !bestDirect:
		case direct == bestDirect && edgeRank(*e) > edgeRank(*best):
		default:
			continue
		}
		best, bestDirect = e, direct
	}
	if best == nil {
		return nil
	}
	viaID := users[best.From]
	if viaID == nil {
		return nil
	}
	route := crmcontracts.PersonGraphRoute{
		ViaUserId:      *viaID,
		ViaDisplayName: labels[best.From],
		Why:            proofLine(*best, now),
	}
	if !bestDirect {
		route.ThroughPersonId = people[best.To]
		through := labels[best.To]
		route.ThroughDisplayName = &through
	}
	return &route
}

// edgeRank orders candidate routes. Two-way exchange first, because a
// relationship that answers is worth more than one that only sends; volume
// breaks the tie.
func edgeRank(e crmcontracts.PersonGraphEdge) int {
	twoWay := 0
	if e.Inbound90d != nil && *e.Inbound90d > 0 && e.Outbound90d != nil && *e.Outbound90d > 0 {
		twoWay = 1000
	}
	return twoWay + e.Interactions90d
}

// proofLine writes the route's evidence from the counts — the sentence a rep
// reads before deciding whether to ask.
func proofLine(e crmcontracts.PersonGraphEdge, now time.Time) string {
	when := "no recent contact"
	if e.LastAt != nil {
		days := int(now.Sub(*e.LastAt).Hours() / 24)
		switch days {
		case 0:
			when = "last contact today"
		case 1:
			when = "last contact yesterday"
		default:
			when = fmt.Sprintf("last contact %d days ago", days)
		}
	}
	if e.Inbound90d != nil && *e.Inbound90d > 0 && e.Outbound90d != nil && *e.Outbound90d > 0 {
		return fmt.Sprintf("%s in 90 days · %s", plural(e.Interactions90d, "two-way exchange"), when)
	}
	return fmt.Sprintf("%s in 90 days, one-sided · %s", plural(e.Interactions90d, "interaction"), when)
}

// plural writes a count and its noun. The proof line is read by a person
// deciding whether to ask a colleague for a favour, and "1 interactions"
// undermines the claim it is making.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// wireEdge renders one interaction edge, scoring it through the same §4 curve
// every other surface uses.
func wireEdge(e search.InteractionEdge, from, to string, now time.Time) crmcontracts.PersonGraphEdge {
	score := e.StrengthOf(now)
	in, outCount := e.InCount90d, e.OutCount90d
	last := e.LastAt
	return crmcontracts.PersonGraphEdge{
		From:            from,
		To:              to,
		StrengthBucket:  crmcontracts.PersonGraphEdgeStrengthBucket(score.Bucket),
		Interactions90d: e.Count90d,
		Inbound90d:      &in,
		Outbound90d:     &outCount,
		LastAt:          &last,
	}
}

func colleagueNode(userID ids.UUID, name string, group crmcontracts.PersonGraphNodeGroup) crmcontracts.PersonGraphNode {
	uid := openapi_types.UUID(userID)
	return crmcontracts.PersonGraphNode{
		Id:     userNodeID(userID),
		Type:   crmcontracts.PersonGraphNodeTypeColleague,
		Group:  group,
		Label:  name,
		UserId: &uid,
	}
}

// Node ids are prefixed rather than bare uuids: a user and a person are
// different kinds of node, and an edge that referred to a bare id would be
// ambiguous the first time the two id spaces collided.
func userNodeID(id ids.UUID) string   { return "user:" + id.String() }
func personNodeID(id ids.UUID) string { return "person:" + id.String() }
