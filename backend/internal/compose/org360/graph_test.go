// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The graph's placement rules over already-read inputs, so each one is
// provable without a database: node order, the caps and what they report,
// edge orientation, and when the intro path may speak.
//
// What the graph READS — the per-group grants, the row-scope prune, and the
// HTTP shape — needs a real database and lives in
// compose/integration/org360_graph_integration_test.go.

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var graphNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// newGraph is one assembly centred on a live account, ready to be placed
// into. It mirrors what Service.Graph builds before it reads a group.
func newGraph(t *testing.T) (*graphAssembly, ids.OrganizationID) {
	t.Helper()
	orgID := ids.From[ids.OrganizationKind](ids.NewV7())
	out := &crmcontracts.OrganizationGraph{
		AsOf:          graphNow,
		RootId:        openapi_types.UUID(orgID.UUID),
		Nodes:         []crmcontracts.OrganizationGraphNode{},
		Edges:         []crmcontracts.OrganizationGraphEdge{},
		GroupsOmitted: []crmcontracts.OrganizationGraphGroupsOmitted{},
	}
	g := &graphAssembly{
		orgID:     orgID,
		now:       graphNow,
		out:       out,
		nodeIndex: map[ids.UUID]int{},
		strengths: map[ids.PersonID]people.RelationshipStrength{},
	}
	g.addNode(crmcontracts.OrganizationGraphNode{
		Id:    openapi_types.UUID(orgID.UUID),
		Kind:  crmcontracts.OrganizationGraphNodeKindOrganization,
		Label: "Acme",
		Root:  true,
	})
	return g, orgID
}

// employee registers one contact with the given score, so a test states the
// strength it is ordering by rather than seeding activities to produce one. It
// bumps employeeTotal with it, because the account's true headcount is what
// dropped_count is counted against.
func (g *graphAssembly) employee(t *testing.T, name string, score int) ids.PersonID {
	t.Helper()
	personID := g.unmeasuredEmployee(t, name)
	g.strengths[personID] = people.RelationshipStrength{Strength: score, Bucket: "strong"}
	return personID
}

// unmeasuredEmployee registers a contact the read could not score — the state
// a missing strengths entry means, kept distinct from a stored zero.
func (g *graphAssembly) unmeasuredEmployee(t *testing.T, name string) ids.PersonID {
	t.Helper()
	personID := ids.From[ids.PersonKind](ids.NewV7())
	g.employees = append(g.employees, graphPersonEdge{personID: personID, fullName: name})
	g.employeeTotal++
	return personID
}

// openDeal registers one open deal on the account, total included.
func (g *graphAssembly) openDeal(name string, amountMinor *int64) ids.UUID {
	dealID := ids.NewV7()
	g.openDeals = append(g.openDeals, graphDeal{dealID: dealID, name: name, amountMinor: amountMinor})
	g.openDealTotal++
	return dealID
}

// nodeIDs is the placed node order, which is the order a client lays out.
func nodeIDs(g *graphAssembly) []ids.UUID {
	out := make([]ids.UUID, 0, len(g.out.Nodes))
	for _, node := range g.out.Nodes {
		out = append(out, ids.UUID(node.Id))
	}
	return out
}

// TestContactsAreOrderedStrongestFirst is the contact group's whole ordering
// claim: the card leads with the warmest relationship, and the account itself
// stays first because it is the centre.
func TestContactsAreOrderedStrongestFirst(t *testing.T) {
	g, orgID := newGraph(t)
	weak := g.employee(t, "Weak", 10)
	strong := g.employee(t, "Strong", 90)
	middling := g.employee(t, "Middling", 50)

	g.placeContacts()

	want := []ids.UUID{orgID.UUID, strong.UUID, middling.UUID, weak.UUID}
	got := nodeIDs(g)
	if len(got) != len(want) {
		t.Fatalf("placed %d nodes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d is %s, want %s", i, got[i], want[i])
		}
	}
	if g.out.DroppedCount != 0 {
		t.Errorf("dropped_count is %d for three contacts under the cap", g.out.DroppedCount)
	}
}

// TestEquallyStrongContactsTieBreakOnId is what makes two reads of an
// unchanged account draw the same picture: without the id tie-break the order
// of equal scores would be whatever the row order happened to be.
func TestEquallyStrongContactsTieBreakOnId(t *testing.T) {
	g, _ := newGraph(t)
	first := g.employee(t, "First", 40)
	second := g.employee(t, "Second", 40)
	if first.String() > second.String() {
		first, second = second, first
	}

	g.placeContacts()

	got := nodeIDs(g)
	if got[1] != first.UUID || got[2] != second.UUID {
		t.Errorf("equal scores placed %s then %s, want %s then %s", got[1], got[2], first, second)
	}
}

// TestAnUnscoredContactSortsLastAndCarriesNoStrength holds the line between
// "we measured a cold relationship" and "we could not measure one": the node
// must not claim a zero the read never produced, and it must not tie with a
// contact that genuinely scored zero — a bare map read would collapse both,
// because a missing entry and a stored 0 look the same.
func TestAnUnscoredContactSortsLastAndCarriesNoStrength(t *testing.T) {
	g, _ := newGraph(t)
	// Measured at ZERO, not merely weak: the point is that an unmeasured
	// contact loses to a contact we measured and found cold.
	scored := g.employee(t, "Scored at zero", 0)
	unscored := g.unmeasuredEmployee(t, "Unscored")

	g.placeContacts()

	got := nodeIDs(g)
	if got[1] != scored.UUID || got[2] != unscored.UUID {
		t.Fatalf("placed %v, want the scored contact ahead of the unscored one", got[1:])
	}
	if g.out.Nodes[2].Strength != nil {
		t.Errorf("unscored contact carries strength %d, want none", *g.out.Nodes[2].Strength)
	}
	if g.out.Nodes[2].StrengthBucket != nil {
		t.Errorf("unscored contact carries bucket %q, want none", *g.out.Nodes[2].StrengthBucket)
	}
}

// TestTheContactCapReportsWhatItLeftOut is the difference between a top slice
// and a claim that this is the whole account.
func TestTheContactCapReportsWhatItLeftOut(t *testing.T) {
	g, _ := newGraph(t)
	over := graphContactCap + 3
	for i := range over {
		g.employee(t, "Contact", over-i) // descending, so the cap cuts the weakest
	}

	g.placeContacts()

	if placed := len(g.out.Nodes) - 1; placed != graphContactCap {
		t.Errorf("placed %d contacts, want the cap of %d", placed, graphContactCap)
	}
	if g.out.DroppedCount != 3 {
		t.Errorf("dropped_count is %d, want the 3 contacts the cap cut", g.out.DroppedCount)
	}
}

// TestDroppedCountReportsPastTheBoundedRead is why the counts are taken from
// each group's own total rather than from the rows in hand: the reads are
// bounded (graphScanCap), so an account larger than the bound would otherwise
// report only the part the query happened to fetch — understating, which reads
// as "that is nearly everything".
func TestDroppedCountReportsPastTheBoundedRead(t *testing.T) {
	g, _ := newGraph(t)
	for range graphContactCap + 1 {
		g.employee(t, "Fetched", 50)
	}
	// The account has far more contacts than the read brought back.
	g.employeeTotal = 900
	amount := int64(1)
	g.openDeal("Fetched", &amount)
	g.openDealTotal = 40
	g.relatedTotal = 25

	g.placeContacts()
	g.placeDeals()
	g.placeRelated([]graphRelatedOrg{
		{orgID: ids.NewV7(), displayName: "Holding", relation: graphRelationParent},
	})

	// 900 − 15 contacts, 40 − 1 deals, 25 − 1 organizations.
	want := (900 - graphContactCap) + (40 - 1) + (25 - 1)
	if g.out.DroppedCount != want {
		t.Errorf("dropped_count = %d, want %d — the count must run from each group's true total",
			g.out.DroppedCount, want)
	}
}

// TestASeatOnADroppedDealDrawsNoEdge is the no-dangling-edge rule: the cap
// removes the deal node, so the stakeholder edge that pointed at it must go
// too.
func TestASeatOnADroppedDealDrawsNoEdge(t *testing.T) {
	g, _ := newGraph(t)
	amount := int64(1)
	for range graphDealCap {
		g.openDeal("Kept", &amount)
	}
	droppedDeal := g.openDeal("Dropped", nil)
	seated := ids.From[ids.PersonKind](ids.NewV7())
	g.seats = append(g.seats, graphSeat{
		dealID: droppedDeal,
		person: graphPersonEdge{personID: seated, fullName: "Stakeholder"},
	})

	g.placeDeals()

	if _, drawn := g.nodeIndex[seated.UUID]; drawn {
		t.Error("a stakeholder of a dropped deal was placed as a node")
	}
	for _, edge := range g.out.Edges {
		if ids.UUID(edge.To) == seated.UUID || ids.UUID(edge.To) == droppedDeal {
			t.Errorf("edge %s -> %s points at a record the cap dropped", ids.UUID(edge.From), ids.UUID(edge.To))
		}
	}
	if g.out.DroppedCount != 1 {
		t.Errorf("dropped_count is %d, want the 1 deal the cap cut", g.out.DroppedCount)
	}
}

// TestAStakeholderWhoAlsoWorksHereIsOneNode: a person holding both edges is
// one record, and the employment title is the description that survives.
func TestAStakeholderWhoAlsoWorksHereIsOneNode(t *testing.T) {
	g, _ := newGraph(t)
	title := "CTO"
	personID := g.employee(t, "Both", 60)
	g.employees[0].title = &title
	dealID := g.openDeal("Renewal", nil)
	role := "champion"
	g.seats = append(g.seats, graphSeat{
		dealID: dealID,
		person: graphPersonEdge{personID: personID, fullName: "Both"},
		role:   &role,
	})

	g.placeContacts()
	g.placeDeals()

	contacts := 0
	for _, node := range g.out.Nodes {
		if node.Kind == crmcontracts.OrganizationGraphNodeKindPerson {
			contacts++
			if node.Detail == nil || *node.Detail != title {
				t.Errorf("person node detail is %v, want the employment title %q", node.Detail, title)
			}
		}
	}
	if contacts != 1 {
		t.Errorf("placed %d person nodes, want 1 for a contact holding two edges", contacts)
	}
	edges := map[crmcontracts.OrganizationGraphEdgeKind]int{}
	for _, edge := range g.out.Edges {
		edges[edge.Kind]++
	}
	if edges[crmcontracts.OrganizationGraphEdgeKindEmployment] != 1 ||
		edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder] != 1 {
		t.Errorf("edges are %v, want one employment and one stakeholder edge", edges)
	}
}

// TestTheHierarchyEdgeAlwaysPointsParentToChild: the account is the child of
// its parent and the parent of its children, and one direction must not be
// drawn as the other.
func TestTheHierarchyEdgeAlwaysPointsParentToChild(t *testing.T) {
	g, orgID := newGraph(t)
	parent := graphRelatedOrg{orgID: ids.NewV7(), displayName: "Holding", relation: graphRelationParent}
	child := graphRelatedOrg{orgID: ids.NewV7(), displayName: "Subsidiary", relation: graphRelationChild}

	g.relatedTotal = 2
	g.placeRelated([]graphRelatedOrg{parent, child})

	want := map[ids.UUID]ids.UUID{parent.orgID: orgID.UUID, orgID.UUID: child.orgID}
	if len(g.out.Edges) != len(want) {
		t.Fatalf("drew %d edges, want %d", len(g.out.Edges), len(want))
	}
	for _, edge := range g.out.Edges {
		if edge.Kind != crmcontracts.OrganizationGraphEdgeKindParentOf {
			t.Errorf("hierarchy edge kind is %q, want parent_of", edge.Kind)
		}
		if to, ok := want[ids.UUID(edge.From)]; !ok || to != ids.UUID(edge.To) {
			t.Errorf("edge %s -> %s is not a parent-to-child edge", ids.UUID(edge.From), ids.UUID(edge.To))
		}
	}
}

// TestAPartnerEdgePointsFromTheOrgThatRecordsIt: `referred_by` on the
// counterparty's row means the counterparty referred us, so the edge starts
// there — reading every partner edge as outbound from this account would
// invert half of them.
func TestAPartnerEdgePointsFromTheOrgThatRecordsIt(t *testing.T) {
	g, orgID := newGraph(t)
	kind := "referred_by"
	counterparty := ids.NewV7()
	theirs := graphRelatedOrg{
		orgID: counterparty, displayName: "Referrer", relation: graphRelationPartner,
		partnerKind: &kind, edgeOwner: &counterparty,
	}
	ours := ids.NewV7()
	oursOwner := orgID.UUID
	mine := graphRelatedOrg{
		orgID: ours, displayName: "Reseller", relation: graphRelationPartner,
		partnerKind: &kind, edgeOwner: &oursOwner,
	}

	g.relatedTotal = 2
	g.placeRelated([]graphRelatedOrg{theirs, mine})

	oriented := map[ids.UUID]ids.UUID{}
	for _, edge := range g.out.Edges {
		if edge.Kind != crmcontracts.OrganizationGraphEdgeKind(kind) {
			t.Errorf("partner edge kind is %q, want %q", edge.Kind, kind)
		}
		oriented[ids.UUID(edge.From)] = ids.UUID(edge.To)
	}
	if oriented[counterparty] != orgID.UUID {
		t.Errorf("their edge runs %s -> %s, want it to start at the counterparty", counterparty, oriented[counterparty])
	}
	if oriented[orgID.UUID] != ours {
		t.Errorf("our edge runs %s -> %s, want it to start at this account", orgID, oriented[orgID.UUID])
	}
}

// TestTheOrganizationCapCountsCompaniesOnce: a company attached twice — a
// parent that is also a reseller — is one node with two edges, and judging it
// twice would both double-count the cap and double-count the drop.
func TestTheOrganizationCapCountsCompaniesOnce(t *testing.T) {
	g, _ := newGraph(t)
	kind := "partner_of"
	both := ids.NewV7()
	owner := both
	related := []graphRelatedOrg{
		{orgID: both, displayName: "Holding", relation: graphRelationParent},
		{orgID: both, displayName: "Holding", relation: graphRelationPartner, partnerKind: &kind, edgeOwner: &owner},
	}
	for range graphOrgCap {
		related = append(related, graphRelatedOrg{
			orgID: ids.NewV7(), displayName: "Other", relation: graphRelationChild,
		})
	}

	// Eleven DISTINCT companies on twelve rows: the doubly-attached one counts
	// once, the way the read's own DISTINCT total counts it.
	g.relatedTotal = graphOrgCap + 1
	g.placeRelated(related)

	// The doubly-attached company takes ONE of the cap's slots, so exactly one
	// of the ten others is left out.
	if orgs := len(g.out.Nodes) - 1; orgs != graphOrgCap {
		t.Errorf("placed %d organizations, want the cap of %d", orgs, graphOrgCap)
	}
	if g.out.DroppedCount != 1 {
		t.Errorf("dropped_count is %d, want 1 — the company beyond the cap, counted once", g.out.DroppedCount)
	}
}

// TestTheIntroPathNamesTheWarmRoomsContact is the shared-ranking claim: the
// card marks the contact signals.RankRouteIn puts first, not the one the card
// happens to draw first.
func TestTheIntroPathNamesTheWarmRoomsContact(t *testing.T) {
	g, _ := newGraph(t)
	weak := g.employee(t, "Weak", 10)
	strong := g.employee(t, "Strong", 80)
	signalID := ids.NewV7()
	g.signalID = &signalID
	g.routeIn = []signals.RouteInEdge{{PersonID: weak}, {PersonID: strong}}

	g.placeContacts()
	g.markIntroPath()

	if g.out.IntroPath == nil {
		t.Fatal("no intro path for an open signal with two measured contacts")
	}
	if ids.UUID(g.out.IntroPath.ContactId) != strong.UUID {
		t.Errorf("intro path routes through %s, want the strongest contact %s",
			ids.UUID(g.out.IntroPath.ContactId), strong)
	}
	if ids.UUID(g.out.IntroPath.SignalId) != signalID {
		t.Errorf("intro path cites signal %s, want %s", ids.UUID(g.out.IntroPath.SignalId), signalID)
	}
	marked := 0
	for _, node := range g.out.Nodes {
		if node.IntroPath != nil && *node.IntroPath {
			marked++
			if ids.UUID(node.Id) != strong.UUID {
				t.Errorf("marked node %s, want %s", ids.UUID(node.Id), strong)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d nodes carry intro_path, want exactly 1", marked)
	}
}

// TestNoIntroPathWithoutAnActiveSignal: the ranking exists whenever the
// account has contacts, but a path nobody is on is not a path.
func TestNoIntroPathWithoutAnActiveSignal(t *testing.T) {
	g, _ := newGraph(t)
	only := g.employee(t, "Only", 70)
	g.routeIn = []signals.RouteInEdge{{PersonID: only}}

	g.placeContacts()
	g.markIntroPath()

	if g.out.IntroPath != nil {
		t.Errorf("intro path %v reported with no open signal", *g.out.IntroPath)
	}
}

// TestNoIntroPathWhenTheRouteInIsNotDrawn is the rule that keeps the card and
// the warm room from naming different people: the route-in contact's only seat
// is on a deal this card did not draw, so the card says nothing rather than
// promoting the next contact it happens to have.
func TestNoIntroPathWhenTheRouteInIsNotDrawn(t *testing.T) {
	g, _ := newGraph(t)
	drawn := g.employee(t, "Drawn", 20)
	elsewhere := ids.From[ids.PersonKind](ids.NewV7())
	g.strengths[elsewhere] = people.RelationshipStrength{Strength: 95, Bucket: "strong"}
	signalID := ids.NewV7()
	g.signalID = &signalID
	g.routeIn = []signals.RouteInEdge{{PersonID: drawn}, {PersonID: elsewhere}}

	g.placeContacts()
	g.markIntroPath()

	if g.out.IntroPath != nil {
		t.Errorf("intro path names %s, but the route-in contact is not a node here",
			ids.UUID(g.out.IntroPath.ContactId))
	}
	for _, node := range g.out.Nodes {
		if node.IntroPath != nil && *node.IntroPath {
			t.Errorf("node %s marked as the route in, want no mark", ids.UUID(node.Id))
		}
	}
}

// TestAnUnmeasuredRouteInIsNotAWarmPath: a contact whose strength the caller
// could not resolve ranks nowhere, the same drop the warm/cold join makes,
// rather than sorting in as a zero.
func TestAnUnmeasuredRouteInIsNotAWarmPath(t *testing.T) {
	g, _ := newGraph(t)
	unmeasured := ids.From[ids.PersonKind](ids.NewV7())
	g.employees = append(g.employees, graphPersonEdge{personID: unmeasured, fullName: "Unmeasured"})
	signalID := ids.NewV7()
	g.signalID = &signalID
	g.routeIn = []signals.RouteInEdge{{PersonID: unmeasured}}

	g.placeContacts()
	g.markIntroPath()

	if g.out.IntroPath != nil {
		t.Errorf("intro path %v reported for a contact with no measured relationship", *g.out.IntroPath)
	}
}
