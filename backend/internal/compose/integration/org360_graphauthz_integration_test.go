// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What the connections card is NOT allowed to show. The graph's shape is
// pinned in org360_graph_integration_test.go; this file is only about the
// gates:
//
//   - a group the caller's grants refuse is absent and NAMED, never drawn as a
//     company with no contacts;
//   - every node carries its own object's row scope, so the card cannot
//     out-see the endpoint that owns the record;
//   - an edge needs BOTH its ends visible — a stakeholder seat is withheld
//     when either the deal or the person is;
//   - the intro path names the contact the warm room names, and stays quiet
//     rather than naming a different one.

import (
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestRouteInEdgesRefusesWithoutThePersonGrant pins the gate at the ENTRY
// POINT rather than at the callers.
//
// Every row RouteInEdges returns names a person, and its row-scope clause
// narrows WHICH people a caller sees, never whether they may see people at
// all — so the object grant has to be asked here, or each new caller has to
// remember to ask it and one eventually will not. The warm/cold join and the
// connections card are both callers today; when this read was gated only by
// its callers, reordering the graph's group list turned it into an ungated one.
func TestRouteInEdgesRefusesWithoutThePersonGrant(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, contact, org.UUID, "cto")

	noPeople := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true},
			"signal":       {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	err := database.WithWorkspaceTx(noPeople, e.Pool, func(tx pgx.Tx) error {
		_, err := signals.RouteInEdges(noPeople, tx, org)
		return err
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("RouteInEdges without the person grant → %v, want ErrPermissionDenied", err)
	}

	// The positive control: the same call WITH the grant returns the contact, so
	// the gate refuses a missing grant rather than breaking the read.
	granted := e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms)
	var edges []signals.RouteInEdge
	if err := database.WithWorkspaceTx(granted, e.Pool, func(tx pgx.Tx) error {
		var err error
		edges, err = signals.RouteInEdges(granted, tx, org)
		return err
	}); err != nil {
		t.Fatalf("RouteInEdges with the person grant: %v", err)
	}
	if len(edges) != 1 || edges[0].PersonID.UUID != contact {
		t.Errorf("route-in edges = %+v, want the account's one contact %v", edges, contact)
	}
}

// TestOrganizationGraphOmitsAGroupTheCallerMayNotRead is the honesty rule the
// whole card rests on: a company with contacts the caller cannot read must not
// look like a company with no contacts.
func TestOrganizationGraphOmitsAGroupTheCallerMayNotRead(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipeline, stage, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	employee := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, employee, org, "cto")
	deal := e.SeedDeal(t, "Renewal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, deal_id, role, source, captured_by)
		VALUES ($1, 'deal_stakeholder', $2, $3, 'champion', 'manual', 'human:x')`, e.WS, employee, deal)

	full, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph as a fully-granted rep: %v", err)
	}
	if kinds := graphNodeKinds(full); kinds[crmcontracts.OrganizationGraphNodeKindPerson] != 1 {
		t.Errorf("person nodes = %d for a granted rep, want 1", kinds[crmcontracts.OrganizationGraphNodeKindPerson])
	}

	// Without the person grant: no contacts, no stakeholder edges, and both
	// contacts and the intro path named as withheld.
	noPeople := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true},
			"deal":         {Read: true},
			"signal":       {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	graph, err := svc.Graph(noPeople, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph without the person grant: %v", err)
	}
	if kinds := graphNodeKinds(graph); kinds[crmcontracts.OrganizationGraphNodeKindPerson] != 0 {
		t.Errorf("person nodes = %d without the person grant, want 0", kinds[crmcontracts.OrganizationGraphNodeKindPerson])
	}
	if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder] != 0 {
		t.Error("a stakeholder edge was drawn without the person grant — an edge names two records")
	}
	for _, want := range []crmcontracts.OrganizationGraphGroupsOmitted{"contacts", "intro_path"} {
		if !slices.Contains(graph.GroupsOmitted, want) {
			t.Errorf("groups_omitted = %v, want it to name %q", graph.GroupsOmitted, want)
		}
	}
	// The deal is still drawn: losing one grant narrows the card, it does not
	// blank it.
	if !graphHasNode(graph, deal) {
		t.Error("the open deal is missing for a caller who holds the deal grant")
	}

	// Without the deal grant: no deals, no stakeholder edges, contacts intact.
	noDeals := e.As(e.Rep1, []ids.UUID{e.Team1}, org360NoDealPerms)
	graph, err = svc.Graph(noDeals, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph without the deal grant: %v", err)
	}
	if graphHasNode(graph, deal) {
		t.Error("a deal was drawn for a caller who cannot read deals")
	}
	if !slices.Contains(graph.GroupsOmitted, "deals") {
		t.Errorf("groups_omitted = %v, want it to name deals", graph.GroupsOmitted)
	}
	if !graphHasNode(graph, employee) {
		t.Error("the contact is missing for a caller who holds the person grant")
	}
}

// TestOrganizationGraphPrunesNodesToTheCallersRowScope: the grant says which
// KINDS a caller may see, the row scope says which ROWS. Every node kind is
// checked, because one unscoped arm is a side channel for the whole class.
func TestOrganizationGraphPrunesNodesToTheCallersRowScope(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipeline, stage, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	employ(t, e, mine, org, "cto")
	employ(t, e, theirs, org, "cfo")

	myDeal := e.SeedDeal(t, "My Renewal", pipeline, stage, &e.Rep1)
	theirDeal := e.SeedDeal(t, "Their Renewal", pipeline, stage, &e.Rep3)
	for _, deal := range []ids.UUID{myDeal, theirDeal} {
		e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
	}

	myPartner := e.SeedOrg(t, "My Partner", &e.Rep1)
	theirPartner := e.SeedOrg(t, "Their Partner", &e.Rep3)
	for _, partner := range []ids.UUID{myPartner, theirPartner} {
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, organization_id, counterparty_org_id, source, captured_by)
			VALUES ($1, 'referred_by', $2, $3, 'manual', 'human:x')`, e.WS, partner, org)
	}

	// A seat on the caller's own deal held by a person they cannot read: the
	// deal is visible, the person is not, so the edge must not appear.
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, deal_id, role, source, captured_by)
		VALUES ($1, 'deal_stakeholder', $2, $3, 'blocker', 'manual', 'human:x')`, e.WS, theirs, myDeal)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	for _, want := range []ids.UUID{mine, myDeal, myPartner} {
		if !graphHasNode(graph, want) {
			t.Errorf("node %v is missing — it is inside the caller's row scope", want)
		}
	}
	for _, forbidden := range []ids.UUID{theirs, theirDeal, theirPartner} {
		if graphHasNode(graph, forbidden) {
			t.Errorf("node %v was drawn — it is outside the caller's row scope", forbidden)
		}
	}
	if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindDealStakeholder] != 0 {
		t.Error("a stakeholder edge was drawn for a person outside the caller's row scope")
	}
	// referred_by is recorded on the PARTNER's row, so the edge starts there.
	for _, edge := range graph.Edges {
		if edge.Kind != crmcontracts.OrganizationGraphEdgeKindReferredBy {
			continue
		}
		if ids.UUID(edge.From) != myPartner || ids.UUID(edge.To) != org {
			t.Errorf("referred_by edge runs %v -> %v, want %v -> %v", edge.From, edge.To, myPartner, org)
		}
	}
}

// TestOrganizationGraphHidesAnAccountOutsideTheCallersRowScope: the root read
// is the whole graph's gate, and out-of-scope must be indistinguishable from
// nonexistent.
func TestOrganizationGraphHidesAnAccountOutsideTheCallersRowScope(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	theirs := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Other Team Account", &e.Rep3))

	_, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), theirs)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("graph out of row scope → %v, want ErrNotFound (existence-hiding)", err)
	}
	mine := ids.From[ids.OrganizationKind](e.SeedOrg(t, "My Account", &e.Rep1))
	if _, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), mine); err != nil {
		t.Errorf("graph on the caller's own account: %v", err)
	}
}

// TestOrganizationGraphIntroPathNamesTheWarmRoomsContact is the no-drift
// claim, taken against real rows: the card marks the same contact
// GET /signals/{id}/intro-path would route through, and cites the signal.
func TestOrganizationGraphIntroPathNamesTheWarmRoomsContact(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	cold := e.SeedPerson(t, "Cold Contact", &e.Rep1)
	warm := e.SeedPerson(t, "Warm Contact", &e.Rep1)
	employ(t, e, cold, org, "cfo")
	employ(t, e, warm, org, "cto")
	// Only the warm contact has qualifying interactions inside the §4 window,
	// so the ranking has one honest answer rather than a tie.
	for _, direction := range []string{"inbound", "outbound"} {
		activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'terms', '2026-05-30T09:00:00Z', '`+direction+`', 'manual', 'human:x')`, e.WS)
		LinkActivity(t, owner, e.WS, activity, "person", warm)
	}
	signal := seedOpenSignal(t, owner, e.WS, org)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.IntroPath == nil {
		t.Fatal("no intro path for an open resolved signal with a measured contact")
	}
	if ids.UUID(graph.IntroPath.ContactId) != warm {
		t.Errorf("intro path routes through %v, want the strongest contact %v", graph.IntroPath.ContactId, warm)
	}
	if ids.UUID(graph.IntroPath.SignalId) != signal {
		t.Errorf("intro path cites signal %v, want %v", graph.IntroPath.SignalId, signal)
	}
	marked := 0
	for _, node := range graph.Nodes {
		if node.IntroPath != nil && *node.IntroPath {
			marked++
			if ids.UUID(node.Id) != warm {
				t.Errorf("marked node %v as the route in, want %v", node.Id, warm)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d nodes carry intro_path, want exactly 1", marked)
	}

	// A caller without the signal grant gets the same contacts and no path,
	// named as withheld — advice absent is not advice that does not exist.
	blind := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	graph, err = svc.Graph(blind, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph without the signal grant: %v", err)
	}
	if graph.IntroPath != nil {
		t.Errorf("intro path %+v served without the signal grant", *graph.IntroPath)
	}
	if !slices.Contains(graph.GroupsOmitted, "intro_path") {
		t.Errorf("groups_omitted = %v, want it to name intro_path", graph.GroupsOmitted)
	}
	if !graphHasNode(graph, warm) {
		t.Error("the contact is missing for a caller who holds the person grant")
	}
}

// TestOrganizationGraphCitesAnOrganizationSubjectSignal: a signal created
// directly ABOUT the account carries the subject pair and no resolved_org_id at
// all, so a card that only looked at resolved_org_id would never cite one. The
// predicate comes from the signals module (signals.OfOrganizationWhere), which
// is what keeps this card and GET /signals agreeing about what belongs to an
// account.
func TestOrganizationGraphCitesAnOrganizationSubjectSignal(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Warm Contact", &e.Rep1)
	employ(t, e, contact, org, "cto")
	activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', 'terms', '2026-05-30T09:00:00Z', 'inbound', 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, activity, "person", contact)
	signal := seedOrgSubjectSignal(t, owner, e.WS, org)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.IntroPath == nil {
		t.Fatal("no intro path for a signal created about the account itself")
	}
	if ids.UUID(graph.IntroPath.SignalId) != signal {
		t.Errorf("intro path cites signal %v, want the org-subject signal %v",
			graph.IntroPath.SignalId, signal)
	}
}

// TestOrganizationGraphReportsNoIntroPathWithoutAnOpenSignal: the ranking is
// available whenever the account has contacts, so the absence has to come from
// the signal, not from the contacts.
func TestOrganizationGraphReportsNoIntroPathWithoutAnOpenSignal(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Warm Contact", &e.Rep1)
	employ(t, e, contact, org, "cto")
	activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', 'terms', '2026-05-30T09:00:00Z', 'inbound', 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, activity, "person", contact)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.IntroPath != nil {
		t.Errorf("intro path %+v reported for an account with no open signal", *graph.IntroPath)
	}
	if slices.Contains(graph.GroupsOmitted, "intro_path") {
		t.Error("groups_omitted names intro_path for a caller who HAS the signal grant — nothing was withheld")
	}
	if !graphHasNode(graph, contact) {
		t.Error("the contact is missing")
	}
}
