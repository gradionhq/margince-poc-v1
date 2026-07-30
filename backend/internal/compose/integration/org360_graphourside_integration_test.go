// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Our side of the connections card: who in THIS workspace is connected to the
// account. Before it existed the card answered "who works there" and left out
// the half a rep opens it for — that they, or a colleague, already have a way
// in.
//
// Two kinds of connection, and the tests below are mostly about what does NOT
// count as one: an `owns` edge from the account's owner, and an
// `in_contact_with` edge from whoever AUTHORED a real interaction (email, call,
// meeting) with one of the contacts the card drew. A connector-captured
// message, an agent-captured one, a task and a note all fail that test.
//
// The placement rules over already-read rows — the owner who also wrote being
// one node, the cap's drop count — need no database and live in
// org360/graph_test.go.

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedMember adds one more live workspace member, for the cases that need more
// colleagues than the harness's three.
func seedMember(t *testing.T, owner *pgx.Conn, ws ids.UUID, name string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, $4)`,
		id, ws, id.String()+"@authz.test", name); err != nil {
		t.Fatalf("seeding member %s: %v", name, err)
	}
	return id
}

// seedTouch records one activity of the given kind, stamped with the given
// provenance, and links it to the person. capturedBy is spelled by the caller
// on purpose: whether the stamp is a human's is exactly what decides an edge.
func seedTouch(t *testing.T, owner *pgx.Conn, ws ids.UUID, kind, capturedBy string, person ids.UUID) {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, $3, 'terms', '2026-05-30T09:00:00Z', 'manual', $4)`,
		id, ws, kind, capturedBy); err != nil {
		t.Fatalf("seeding a %s: %v", kind, err)
	}
	LinkActivity(t, owner, ws, id, "person", person)
}

// humanStamp is the provenance a human write carries; matching it is what
// keeps a connector's inbox sync from drawing edges nobody earned.
func humanStamp(user ids.UUID) string { return "human:" + user.String() }

// graphUserNodes is the user nodes the card drew, by id, which is how these
// tests state who is on our side without depending on node order.
func graphUserNodes(graph crmcontracts.OrganizationGraph) map[ids.UUID]string {
	out := map[ids.UUID]string{}
	for _, node := range graph.Nodes {
		if node.Kind == crmcontracts.OrganizationGraphNodeKindUser {
			out[ids.UUID(node.Id)] = node.Label
		}
	}
	return out
}

// graphEdgeTargets is the far end of every edge of one kind.
func graphEdgeTargets(graph crmcontracts.OrganizationGraph, kind crmcontracts.OrganizationGraphEdgeKind) map[ids.UUID]ids.UUID {
	out := map[ids.UUID]ids.UUID{}
	for _, edge := range graph.Edges {
		if edge.Kind == kind {
			out[ids.UUID(edge.From)] = ids.UUID(edge.To)
		}
	}
	return out
}

// The two connections, against real rows: the account's owner, and the
// colleague who emailed one of its people.
func TestOrganizationGraphDrawsTheOwnerAndWhoHasBeenInContact(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, contact, org, "cto")
	// Rep2 shares Team1 with Rep1 and wrote to the contact; Rep1 owns the
	// account and has written nothing.
	seedTouch(t, owner, e.WS, "email", humanStamp(e.Rep2), contact)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	users := graphUserNodes(graph)
	if len(users) != 2 {
		t.Fatalf("drew %d user nodes (%v), want the owner and the colleague who wrote", len(users), users)
	}
	if _, drawn := users[e.Rep1]; !drawn {
		t.Error("the account owner is not on the card")
	}
	if _, drawn := users[e.Rep2]; !drawn {
		t.Error("the colleague who emailed the contact is not on the card")
	}
	if got := graphEdgeTargets(graph, crmcontracts.OrganizationGraphEdgeKindOwns); got[e.Rep1] != org {
		t.Errorf("owns edge from the owner points at %v, want the account %v", got[e.Rep1], org)
	}
	if got := graphEdgeTargets(graph, crmcontracts.OrganizationGraphEdgeKindInContactWith); got[e.Rep2] != contact {
		t.Errorf("in_contact_with edge from the writer points at %v, want the contact %v", got[e.Rep2], contact)
	}
	assertNoDanglingEdge(t, graph)
	if graph.DroppedCount != 0 {
		t.Errorf("dropped_count = %d, want 0 — nothing here reaches a cap", graph.DroppedCount)
	}
	if slices.Contains(graph.GroupsOmitted, "our_side") {
		t.Errorf("groups_omitted = %v, want our_side absent for a caller holding both grants", graph.GroupsOmitted)
	}
}

// What is NOT contact. A connector syncing an inbox stamps the row with the
// connector, not with a person, and an agent stamps itself — neither is a
// colleague who spoke to anyone. A task and a note are not interactions at all:
// assigning work is intent, and writing something down is not reaching out.
func TestOrganizationGraphDrawsNoContactEdgeForANonInteraction(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, contact, org, "cto")
	seedTouch(t, owner, e.WS, "email", "connector:gmail", contact)
	seedTouch(t, owner, e.WS, "email", "agent:overnight", contact)
	seedTouch(t, owner, e.WS, "task", humanStamp(e.Rep2), contact)
	seedTouch(t, owner, e.WS, "note", humanStamp(e.Rep2), contact)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindInContactWith] != 0 {
		t.Errorf("%d in_contact_with edges drawn from connector, agent, task and note rows",
			edges[crmcontracts.OrganizationGraphEdgeKindInContactWith])
	}
	if _, drawn := graphUserNodes(graph)[e.Rep2]; drawn {
		t.Error("a colleague was placed for a task and a note — neither is contact")
	}
	// The owner edge is unaffected: it comes off the account, not the timeline.
	if users := graphUserNodes(graph); len(users) != 1 {
		t.Errorf("drew %d user nodes (%v), want the owner alone", len(users), users)
	}

	// The positive control: one human-captured EMAIL from the same colleague
	// draws the edge, so the silence above is the rule and not a broken read.
	seedTouch(t, owner, e.WS, "email", humanStamp(e.Rep2), contact)
	graph, err = svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph after the email: %v", err)
	}
	if got := graphEdgeTargets(graph, crmcontracts.OrganizationGraphEdgeKindInContactWith); got[e.Rep2] != contact {
		t.Errorf("a human-captured email drew no edge from %v to %v", e.Rep2, contact)
	}
}

// The group asks BOTH its gates itself. Every edge names a contact, and every
// interaction edge is derived from an activity — so a caller missing either
// grant gets the group named as withheld rather than an account that looks like
// nobody here has ever spoken to it.
func TestOrganizationGraphOmitsOurSideWithoutThePersonOrActivityGrant(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, contact, org, "cto")
	seedTouch(t, owner, e.WS, "email", humanStamp(e.Rep2), contact)
	orgID := ids.From[ids.OrganizationKind](org)

	noPeople := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true},
			"activity":     {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	noActivities := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true},
			"person":       {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	for name, ctx := range map[string]context.Context{
		"no person grant":   noPeople,
		"no activity grant": noActivities,
	} {
		t.Run(name, func(t *testing.T) {
			graph, err := svc.Graph(ctx, orgID)
			if err != nil {
				t.Fatalf("graph: %v", err)
			}
			if users := graphUserNodes(graph); len(users) != 0 {
				t.Errorf("drew user nodes %v for a caller who may not read the group", users)
			}
			if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindOwns] != 0 {
				t.Error("an owns edge was drawn for a caller the group was withheld from")
			}
			if !slices.Contains(graph.GroupsOmitted, "our_side") {
				t.Errorf("groups_omitted = %v, want it to name our_side", graph.GroupsOmitted)
			}
		})
	}

	// With the activity grant back, the contacts caller sees the group again:
	// the refusals above narrow the card, they do not describe a broken read.
	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms), orgID)
	if err != nil {
		t.Fatalf("graph as a fully-granted rep: %v", err)
	}
	if len(graphUserNodes(graph)) != 2 {
		t.Errorf("a fully-granted rep sees %v, want the owner and the writer", graphUserNodes(graph))
	}
}

// An interaction with a person the caller's row scope hides draws nothing: the
// contact is not a node, so the colleague who wrote to them has nothing to
// point at. Anything else would leak the fact of contact with a record whose
// existence the card is hiding.
func TestOrganizationGraphDrawsNoContactEdgeForAnOutOfScopeContact(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	employ(t, e, mine, org, "cto")
	employ(t, e, theirs, org, "cfo")
	writerToMine := seedMember(t, owner, e.WS, "Writes To Mine")
	writerToTheirs := seedMember(t, owner, e.WS, "Writes To Theirs")
	seedTouch(t, owner, e.WS, "email", humanStamp(writerToMine), mine)
	seedTouch(t, owner, e.WS, "email", humanStamp(writerToTheirs), theirs)

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	users := graphUserNodes(graph)
	if _, drawn := users[writerToMine]; !drawn {
		t.Error("the colleague who wrote to the in-scope contact is missing")
	}
	if _, drawn := users[writerToTheirs]; drawn {
		t.Error("a colleague was drawn for contact with a person outside the caller's row scope")
	}
	targets := graphEdgeTargets(graph, crmcontracts.OrganizationGraphEdgeKindInContactWith)
	if len(targets) != 1 || targets[writerToMine] != mine {
		t.Errorf("in_contact_with edges = %v, want the one to %v", targets, mine)
	}
	assertNoDanglingEdge(t, graph)
}

// The cap counts USERS, because that is what it means: twelve colleagues who
// have each written to the account must not be bounded by rows, and the
// remainder has to be reported. dropped_count comes off the same statement as
// the rows — a second count could see a newer snapshot and drive it NEGATIVE,
// which the contract's own `minimum: 0` forbids.
func TestOrganizationGraphUserCapCountsUsersAndReportsTheRemainder(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	// An unassigned account, so the owner edge cannot pad the user count.
	org := e.SeedOrg(t, "Acme", nil)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employ(t, e, contact, org, "cto")
	const writers = 13
	for i := range writers {
		member := seedMember(t, owner, e.WS, fmt.Sprintf("Colleague %02d", i))
		// Two interactions each: a row-counting cap would spend its budget on
		// half as many colleagues.
		seedTouch(t, owner, e.WS, "email", humanStamp(member), contact)
		seedTouch(t, owner, e.WS, "call", humanStamp(member), contact)
	}

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if users := len(graphUserNodes(graph)); users != 10 {
		t.Errorf("drew %d user nodes, want the full allowance of 10 — two interactions each must not halve it", users)
	}
	if graph.DroppedCount != writers-10 {
		t.Errorf("dropped_count = %d, want %d — the colleagues the cap left out", graph.DroppedCount, writers-10)
	}
	if graph.DroppedCount < 0 {
		t.Error("dropped_count is negative; the contract declares a minimum of 0")
	}
	assertNoDanglingEdge(t, graph)
}
