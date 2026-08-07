// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package org360

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

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// seedTouch records one activity of the given kind and links it to the person,
// with `colleague` naming which of our people was IN it — nil for an
// interaction nobody on our side is recorded in.
//
// It writes participant rows and folds the projection, which is what capture
// and the cg:graph-edge consumer do between them in production. Seeding an
// activity alone would leave the projection empty and every assertion below
// would pass vacuously.
func seedTouch(t *testing.T, e *integration.Env, owner *pgx.Conn, kind string, colleague *ids.UUID, person ids.UUID) {
	t.Helper()
	ctx := context.Background()
	ws := e.WS
	id := ids.NewV7()
	capturedBy := "connector:gmail"
	if colleague != nil {
		capturedBy = "human:" + colleague.String()
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, $3, 'terms', '2026-05-30T09:00:00Z', 'outbound', 'manual', $4)`,
		id, ws, kind, capturedBy); err != nil {
		t.Fatalf("seeding a %s: %v", kind, err)
	}
	integration.LinkActivity(t, owner, ws, id, "person", person)

	// Only a real exchange has participants. A task is intent and a note is a
	// record of thinking; neither means the two people spoke, which is why
	// they draw no edge without the graph needing a kind filter of its own.
	if kind == "email" || kind == "call" || kind == "meeting" {
		if colleague != nil {
			if _, err := owner.Exec(ctx, `
				INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
				VALUES ($1, $2, $3, 'from')`, ws, id, *colleague); err != nil {
				t.Fatalf("seeding the our-side participant: %v", err)
			}
		}
		if _, err := owner.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES ($1, $2, $3, 'to')`, ws, id, person); err != nil {
			t.Fatalf("seeding the counterparty participant: %v", err)
		}
	}
	foldEdges(t, e, id)
}

// foldEdges runs the recompute the cg:graph-edge consumer runs, so a test sees
// the projection the worker would have built.
func foldEdges(t *testing.T, e *integration.Env, activityID ids.UUID) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(ctx, tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding interaction edges: %v", err)
	}
}

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
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	integration.Employ(t, e, contact, org, "cto")
	// Rep2 shares Team1 with Rep1 and wrote to the contact; Rep1 owns the
	// account and has written nothing.
	seedTouch(t, e, owner, "email", &e.Rep2, contact)

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

// What is NOT contact, and the list is SHORTER than it used to be. Mail synced
// from an inbox used to draw no edge, because the derivation matched a human
// stamp on the activity row and a connector writes its own — that was the
// defect, not the rule, and connector-captured mail whose mailbox owner IS
// recorded now draws an edge like any other exchange.
//
// What remains genuinely not contact: an interaction with no colleague
// recorded in it at all, and a task or a note, which are not interactions in
// the first place — assigning work is intent, and writing something down is
// not reaching out.
func TestOrganizationGraphDrawsNoContactEdgeForANonInteraction(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	integration.Employ(t, e, contact, org, "cto")
	seedTouch(t, e, owner, "email", nil, contact)
	seedTouch(t, e, owner, "email", nil, contact)
	seedTouch(t, e, owner, "task", &e.Rep2, contact)
	seedTouch(t, e, owner, "note", &e.Rep2, contact)

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
	seedTouch(t, e, owner, "email", &e.Rep2, contact)
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
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	integration.Employ(t, e, contact, org, "cto")
	seedTouch(t, e, owner, "email", &e.Rep2, contact)
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
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	integration.Employ(t, e, mine, org, "cto")
	integration.Employ(t, e, theirs, org, "cfo")
	writerToMine := seedMember(t, owner, e.WS, "Writes To Mine")
	writerToTheirs := seedMember(t, owner, e.WS, "Writes To Theirs")
	seedTouch(t, e, owner, "email", &writerToMine, mine)
	seedTouch(t, e, owner, "email", &writerToTheirs, theirs)

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

// setMemberStatus moves one member's app_user.status, which is how these tests
// state "this colleague no longer works here" against a real row.
func setMemberStatus(t *testing.T, owner *pgx.Conn, user ids.UUID, status string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE app_user SET status = $2 WHERE id = $1`, user, status); err != nil {
		t.Fatalf("setting member %s to %s: %v", user, status, err)
	}
}

// A colleague who no longer works here is no answer to "who can introduce me",
// and both halves of our side have to agree about that: owner_id and
// captured_by each survive the day someone's account is closed, so the owns
// edge and the in_contact_with edges must both drop the same person.
//
// The account is simply left unowned. Nothing else about the group changes — a
// live teammate's contact edge is still drawn.
func TestOrganizationGraphDrawsNoColleagueWhoNoLongerWorksHere(t *testing.T) {
	for _, status := range []string{"deactivated", "suspended"} {
		t.Run(status, func(t *testing.T) {
			e := integration.Setup(t)
			owner := integration.OwnerConn(t)
			svc := org360Service(e)

			// Rep2 owns the account AND wrote to its contact, so one person is
			// both would-be edges; Rep1, in the same team, is the caller.
			org := e.SeedOrg(t, "Acme", &e.Rep2)
			contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
			integration.Employ(t, e, contact, org, "cto")
			seedTouch(t, e, owner, "email", &e.Rep2, contact)
			teammate := seedMember(t, owner, e.WS, "Live Teammate")
			seedTouch(t, e, owner, "email", &teammate, contact)
			setMemberStatus(t, owner, e.Rep2, status)

			graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
				ids.From[ids.OrganizationKind](org))
			if err != nil {
				t.Fatalf("graph: %v", err)
			}
			users := graphUserNodes(graph)
			if _, drawn := users[e.Rep2]; drawn {
				t.Errorf("a %s member is on the card; a rep would ask them for an intro", status)
			}
			if edges := graphEdgeKinds(graph); edges[crmcontracts.OrganizationGraphEdgeKindOwns] != 0 {
				t.Errorf("%d owns edges drawn for a %s owner — the account is simply unowned",
					edges[crmcontracts.OrganizationGraphEdgeKindOwns], status)
			}
			// The rest of the group survives: losing the owner blanks neither
			// the group nor anyone else's edges.
			if _, drawn := users[teammate]; !drawn {
				t.Error("the live teammate who wrote to the contact went missing with the owner")
			}
			if got := graphEdgeTargets(graph, crmcontracts.OrganizationGraphEdgeKindInContactWith); got[teammate] != contact {
				t.Errorf("the live teammate's in_contact_with edge points at %v, want the contact %v",
					got[teammate], contact)
			}
			if len(users) != 1 {
				t.Errorf("drew user nodes %v, want the live teammate alone", users)
			}
			if slices.Contains(graph.GroupsOmitted, "our_side") {
				t.Errorf("groups_omitted = %v — a departed owner is not a withheld group", graph.GroupsOmitted)
			}
			if graph.DroppedCount != 0 {
				t.Errorf("dropped_count = %d, want 0 — a member who no longer works here is not a colleague the cap left out",
					graph.DroppedCount)
			}
			assertNoDanglingEdge(t, graph)
		})
	}
}

// graphContactCapSeed is how many contacts the card draws. It mirrors org360's
// unexported graphContactCap; the test below asserts the drawn count, so a
// changed cap fails here loudly rather than quietly weakening the case.
const graphContactCapSeed = 15

// The user cap is applied against the contacts the card actually DRAWS, not
// against every contact the read scanned.
//
// Twelve colleagues here are in touch with contacts the contact cap drops. Run
// against the scanned set they would fill the ten-user allowance, be discarded
// again at placement for having no contact to point at, and leave the card
// showing nobody on our side — while `our_side` and its dropped_count described
// people the graph does not contain.
func TestOrganizationGraphCapsColleaguesAgainstTheContactsItDraws(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	// An unassigned account, so no owner edge pads the user nodes.
	org := e.SeedOrg(t, "Acme", nil)

	// The colleagues of the contacts the cap will drop are seeded FIRST: every
	// interaction shares one timestamp, so the user cap's tie-break is the user
	// id, and these are the ids it reaches for first.
	const undrawn = 12
	outsiders := make([]ids.UUID, 0, undrawn)
	for i := range undrawn {
		contact := e.SeedPerson(t, fmt.Sprintf("Undrawn %02d", i), &e.Rep1)
		integration.Employ(t, e, contact, org, "assistant")
		colleague := seedMember(t, owner, e.WS, fmt.Sprintf("Outsider %02d", i))
		outsiders = append(outsiders, colleague)
		seedTouch(t, e, owner, "email", &colleague, contact)
	}

	// Two colleagues wrote repeatedly to the contacts the card draws. The
	// higher interaction count is what lifts those contacts' §4 score above the
	// ones above, which is what puts them inside the contact cap.
	insiderA := seedMember(t, owner, e.WS, "Insider A")
	insiderB := seedMember(t, owner, e.WS, "Insider B")
	var drawnContacts []ids.UUID
	for i := range graphContactCapSeed {
		contact := e.SeedPerson(t, fmt.Sprintf("Drawn %02d", i), &e.Rep1)
		integration.Employ(t, e, contact, org, "cto")
		drawnContacts = append(drawnContacts, contact)
		for range 3 {
			seedTouch(t, e, owner, "email", &insiderA, contact)
			seedTouch(t, e, owner, "email", &insiderB, contact)
		}
	}

	graph, err := svc.Graph(e.As(e.Rep1, []ids.UUID{e.Team1}, graphRepPerms),
		ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("graph: %v", err)
	}

	drawn := map[ids.UUID]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == crmcontracts.OrganizationGraphNodeKindPerson {
			drawn[ids.UUID(node.Id)] = true
		}
	}
	if len(drawn) != graphContactCapSeed {
		t.Fatalf("the card drew %d contacts, want the cap's %d — the fixture no longer straddles the cap",
			len(drawn), graphContactCapSeed)
	}
	for _, contact := range drawnContacts {
		if !drawn[contact] {
			t.Errorf("contact %s was written to six times and still went undrawn", contact)
		}
	}

	users := graphUserNodes(graph)
	for _, colleague := range outsiders {
		if _, placed := users[colleague]; placed {
			t.Errorf("colleague %s is drawn, but their only contact is not on the card", colleague)
		}
	}
	for name, insider := range map[string]ids.UUID{"Insider A": insiderA, "Insider B": insiderB} {
		if _, placed := users[insider]; !placed {
			t.Errorf("%s is missing; a colleague of a DRAWN contact lost their slot to one of a dropped contact", name)
		}
	}
	if len(users) != 2 {
		t.Errorf("drew user nodes %v, want the two colleagues of the drawn contacts", users)
	}

	// The only records this graph left out are the contacts the contact cap
	// dropped. Nobody on our side was dropped: the cap chose over the drawn
	// contacts, so every colleague it chose is on the card.
	if graph.DroppedCount != undrawn {
		t.Errorf("dropped_count = %d, want the %d contacts the cap cut — our_side left nobody out",
			graph.DroppedCount, undrawn)
	}
	if graph.DroppedCount < 0 {
		t.Error("dropped_count is negative; the contract declares a minimum of 0")
	}
	assertNoDanglingEdge(t, graph)
}

// The cap counts USERS, because that is what it means: twelve colleagues who
// have each written to the account must not be bounded by rows, and the
// remainder has to be reported. dropped_count comes off the same statement as
// the rows — a second count could see a newer snapshot and drive it NEGATIVE,
// which the contract's own `minimum: 0` forbids.
func TestOrganizationGraphUserCapCountsUsersAndReportsTheRemainder(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	// An unassigned account, so the owner edge cannot pad the user count.
	org := e.SeedOrg(t, "Acme", nil)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	integration.Employ(t, e, contact, org, "cto")
	const writers = 13
	for i := range writers {
		member := seedMember(t, owner, e.WS, fmt.Sprintf("Colleague %02d", i))
		// Two interactions each: a row-counting cap would spend its budget on
		// half as many colleagues.
		seedTouch(t, e, owner, "email", &member, contact)
		seedTouch(t, e, owner, "call", &member, contact)
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
