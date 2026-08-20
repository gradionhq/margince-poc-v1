// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package org360

// What an operator sees after they restrict the edge.
//
// `relationship` is a first-class RBAC object because reading an edge discloses
// its endpoints AS A PAIR — a fact neither endpoint's own grant covers. Before
// #1846 the account page answered that fact anyway: an operator who zeroed the
// grant got 403 from GET /v1/relationships/{id}, 403 from the employment list
// and an empty employment section on the person page, so the restriction looked
// enforced everywhere they would check, while the connections graph, the
// contact roster and the per-contact deal roles quietly went on answering.
//
// The census in backend/edgereaders_test.go proves every read CALLS the gate.
// This proves the gate ANSWERS the way an operator would check — and that the
// page says "withheld" through its own channel rather than rendering an account
// with nobody at it, which is the difference between a withheld section and a
// wrong one.
//
// The principal is built in-process rather than PATCHed over HTTP, matching
// TestARoleRefusedTheEdgeObjectCannotTraverseTheEmploymentEdge, which pins the
// same restriction for the query grammar: it is the shape zeroing the grant
// produces, and a role mutated mid-suite would leak into every other test on
// the shared seed.

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// repWithoutTheEdge holds everything the account page reads EXCEPT the edge —
// both endpoints of every edge on the page still readable, which is what makes
// a withheld section attributable to the edge grant and nothing else.
func repWithoutTheEdge() principal.Permissions {
	objects := map[string]principal.ObjectGrant{}
	for object, grant := range graphRepPerms.Objects {
		if object == "relationship" {
			continue
		}
		objects[object] = grant
	}
	return principal.Permissions{
		RoleKeys: graphRepPerms.RoleKeys, Objects: objects, RowScope: graphRepPerms.RowScope,
	}
}

func TestARoleRefusedTheEdgeSeesTheAccountPageWithoutItsEdgesAndIsToldSo(t *testing.T) {
	e := integration.Setup(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	pipeline, stage, _ := integration.DealFixture(t, e)
	employee := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, employee, org, "cto")
	deal := e.SeedDeal(t, "Renewal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
	e.WsExec(t, `INSERT INTO relationship (kind, person_id, deal_id, role, source, captured_by)
		VALUES ('deal_stakeholder', $1, $2, 'champion', 'manual', 'human:x')`, employee, deal)

	orgID := ids.From[ids.OrganizationKind](org)
	granted := e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms)
	restricted := e.As(e.Rep1, []ids.UUID{e.Team1}, repWithoutTheEdge())

	// THE POSITIVE CONTROL FIRST, and it is not optional: every assertion below
	// is an absence, and an absent employment most often means the fixture
	// never seeded one. This proves the seed is real before anything claims it
	// was withheld.
	full, err := svc.Graph(granted, orgID)
	if err != nil {
		t.Fatalf("graph as a rep holding the edge grant: %v", err)
	}
	if kinds := graphNodeKinds(full); kinds[crmcontracts.OrganizationGraphNodeKindPerson] != 1 {
		t.Fatalf("person nodes = %d for a rep holding the edge grant, want 1 — the fixture did not "+
			"seed the employment, so nothing below would prove a withholding",
			kinds[crmcontracts.OrganizationGraphNodeKindPerson])
	}
	if edges := graphEdgeKinds(full); edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder] != 1 {
		t.Fatalf("stakeholder edges = %d for a rep holding the edge grant, want 1",
			edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder])
	}
	if slices.Contains(full.GroupsOmitted, "contacts") {
		t.Fatal("the contacts group was named as withheld for a rep who holds the edge grant")
	}

	// Now the same page for the same rep, with only the edge grant removed.
	graph, err := svc.Graph(restricted, orgID)
	if err != nil {
		t.Fatalf("graph without the edge grant should be assembled and NAMED as partial, not fail: %v", err)
	}
	if kinds := graphNodeKinds(graph); kinds[crmcontracts.OrganizationGraphNodeKindPerson] != 0 {
		t.Errorf("person nodes = %d without the edge grant, want 0 — an employment edge is what puts "+
			"a contact on this card", kinds[crmcontracts.OrganizationGraphNodeKindPerson])
	}
	if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder] != 0 {
		t.Error("a stakeholder seat was drawn without the edge grant — the seat IS the edge, and both " +
			"of its endpoints being readable is exactly the case the edge grant exists for")
	}
	// The honesty channel. Absent-and-named is the whole point: absent alone
	// reads as "nobody works here", which is a different and false statement.
	//
	// `contacts` only. intro_path is NOT named, and that is correct rather than
	// a gap: readRouteIn returns before it reaches the edge when the account
	// carries no open resolved signal, and this fixture seeds none — "there is
	// no active path to propose" is not "a group you may not read", which is
	// the distinction that read states in its own comment.
	if !slices.Contains(graph.GroupsOmitted, "contacts") {
		t.Errorf("groups_omitted = %v without the edge grant, want it to name \"contacts\" — a card "+
			"that withholds silently tells the reader there is nothing to see", graph.GroupsOmitted)
	}
	// The required fields stay present. There is no response validation on this
	// path and the generated client types make them non-optional, so a nil
	// slice here marshals as null and takes the screen down.
	if graph.Nodes == nil || graph.Edges == nil || graph.GroupsOmitted == nil {
		t.Errorf("a required field went absent rather than empty: nodes=%v edges=%v groups=%v",
			graph.Nodes == nil, graph.Edges == nil, graph.GroupsOmitted == nil)
	}
	// dropped_count is a plain int with `minimum: 0`, and it is the number the
	// gate's placement protects: refusing BEFORE the statement leaves it at
	// zero, where filtering rows after the read would report a remainder that
	// discloses the size of the withheld population.
	// Zero, not merely non-negative. The gate refuses BEFORE the statement, so
	// nothing was read and nothing was dropped; a post-filtering implementation
	// would pass a >= 0 assertion while reporting a remainder that discloses
	// the size of the withheld population.
	if graph.DroppedCount != 0 {
		t.Errorf("dropped_count = %d without the edge grant, want 0 — a non-zero remainder means rows "+
			"were read and then filtered, and the number itself then discloses how many were withheld",
			graph.DroppedCount)
	}
}

// The account page's OTHER two edge readers: the contact roster and the
// per-contact deal roles. They matter separately from the graph because the
// roster is what makes a half-gated page incoherent — before this change the
// graph could withhold the employment group while the roster beside it listed
// exactly who worked at the account.
func TestTheContactRosterAndItsDealRolesAreWithheldWithTheEdgeGrant(t *testing.T) {
	e := integration.Setup(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	pipeline, stage, _ := integration.DealFixture(t, e)
	employee := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, employee, org, "cto")
	deal := e.SeedDeal(t, "Renewal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
	e.WsExec(t, `INSERT INTO relationship (kind, person_id, deal_id, role, source, captured_by)
		VALUES ('deal_stakeholder', $1, $2, 'champion', 'manual', 'human:x')`, employee, deal)

	orgID := ids.From[ids.OrganizationKind](org)

	// Positive control, again first.
	full, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), orgID)
	if err != nil {
		t.Fatalf("the account page as a rep holding the edge grant: %v", err)
	}
	if full.People == nil || len(full.People.Data) != 1 {
		t.Fatalf("the roster held %v contacts for a granted rep, want 1 — the fixture did not seed",
			full.People)
	}
	if roles := full.People.Data[0].DealRoles; len(roles) != 1 {
		t.Fatalf("deal_roles = %v for a granted rep, want the champion seat", roles)
	}

	page, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, repWithoutTheEdge()), orgID)
	if err != nil {
		t.Fatalf("the account page without the edge grant should assemble and name what is missing: %v", err)
	}
	// The roster is drawn from employment edges, so it goes with them — named
	// in sections_omitted, which is the contract's own stated shape for a
	// section the caller's grants refuse.
	if page.People != nil && len(page.People.Data) != 0 {
		t.Errorf("the roster listed %d contacts without the edge grant — the employment edge is what "+
			"says they work here", len(page.People.Data))
	}
	if !slices.Contains(page.SectionsOmitted, "people") {
		t.Errorf("sections_omitted = %v without the edge grant, want it to name \"people\"",
			page.SectionsOmitted)
	}
	// contact_count follows the same rule on the account record itself: absent,
	// never zero. Zero would be a wrong number on screen where absent is a
	// withheld one, and the field's own contract description says so.
	if page.Organization.ContactCount != nil {
		t.Errorf("contact_count = %d without the edge grant, want it absent — a count over the "+
			"employment pairs is the fact the edge grant governs",
			*page.Organization.ContactCount)
	}
}
