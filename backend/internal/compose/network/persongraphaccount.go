// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The account arm of the local graph: the other contacts at this person's
// employer, and which colleague is warmest with each.
//
// It exists because the direct arm is so often empty. "Nobody here knows her,
// but Ben has been talking to her head of engineering for a year" is the
// answer a rep can act on, and it is invisible to any read keyed on the
// contact alone.
//
// Split from persongraph.go because it carries its own disclosure rule: this
// arm shows counts and dates and never the messages behind them. Pooled
// interaction metadata is disclosable where the correspondence itself is not
// (ADR-0078 §124), so the receipts the direct arm attaches are deliberately
// absent here rather than merely unfetched.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

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
