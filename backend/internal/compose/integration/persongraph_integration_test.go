// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The person local graph against a real database.
//
// Every claim here is about a REFUSAL, and each one is per-arm: the graph
// applies row scope to the direct arm, the account arm and the receipts
// separately, because a root-only check would let it disclose by adjacency
// exactly what the record list withholds.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// graphPerms is a bounded rep. The scope has to be team-level: an unbounded
// admin short-circuits the very clauses these tests exist to prove.
var graphPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":                {Read: true},
		"organization":          {Read: true},
		"relationship":          {Read: true},
		"activity":              {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// readGraph drives the real HTTP handler, so the wiring and the JSON shape are
// exercised rather than the service alone.
func readGraph(ctx context.Context, t *testing.T, e *Env, personID ids.UUID) (int, crmcontracts.PersonGraph) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/people/"+personID.String()+"/graph", nil).WithContext(ctx)
	network.NewReads(e.Pool).GetPersonGraph(rec, req, crmcontracts.Id(personID))

	var out crmcontracts.PersonGraph
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding the graph: %v", err)
		}
	}
	return rec.Code, out
}

// seedExchange records one two-way exchange between a colleague and a contact,
// then folds the projection the cg:graph-edge consumer would have folded.
func seedExchange(t *testing.T, e *Env, colleague, person ids.UUID, subject string) {
	t.Helper()
	owner := OwnerConn(t)
	ctx := context.Background()
	id := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', $3, now(), 'outbound', 'manual', 'human:x')`,
		id, e.WS, subject); err != nil {
		t.Fatalf("seeding the exchange: %v", err)
	}
	LinkActivity(t, owner, e.WS, id, "person", person)
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
		VALUES ($1, $2, $3, 'from')`, e.WS, id, colleague); err != nil {
		t.Fatalf("seeding our side: %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
		VALUES ($1, $2, $3, 'to')`, e.WS, id, person); err != nil {
		t.Fatalf("seeding their side: %v", err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, e.WS)
	if err := database.WithWorkspaceTx(wsCtx, e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(wsCtx, tx, []ids.UUID{id})
	}); err != nil {
		t.Fatalf("folding the edge: %v", err)
	}
}

// A contact outside the caller's row scope must 404. An empty graph would
// confirm the record exists and only its edges are withheld.
func TestPersonGraphRefusesAContactOutsideRowScope(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)

	if code, _ := readGraph(rep, t, e, theirs); code != http.StatusNotFound {
		t.Errorf("graph of another team's contact → %d, want 404", code)
	}
}

// The direct arm names the colleagues who corresponded, and attaches the real
// messages behind each edge — pooled counts alone would ask the reader to
// trust a number.
func TestPersonGraphAttachesTheMessagesBehindADirectEdge(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	seedExchange(t, e, e.Rep1, mine, "Q3 pricing")

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)
	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200", code)
	}

	var direct int
	for _, n := range graph.Nodes {
		if n.Group == crmcontracts.PersonGraphNodeGroupDirect {
			direct++
		}
	}
	if direct != 1 {
		t.Fatalf("direct colleagues = %d, want 1", direct)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(graph.Edges))
	}
	receipts := graph.Edges[0].Receipts
	if receipts == nil || len(*receipts) != 1 {
		t.Fatal("the direct edge carried no receipts; pooled counts alone are a number to trust")
	}
	if (*receipts)[0].Subject == nil || *(*receipts)[0].Subject != "Q3 pricing" {
		t.Error("the receipt did not name the message it was derived from")
	}
	if graph.Route == nil {
		t.Fatal("a direct relationship produced no recommended route")
	}
	if graph.Route.Why == "" {
		t.Error("the route carries no proof line, so it asks the reader to trust it")
	}
}

// The counts are pooled metadata and disclosable; the messages are
// correspondence and are not. A caller with no activity grant keeps the edge
// and loses the receipts.
func TestPersonGraphWithholdsReceiptsFromACallerWithNoActivityGrant(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	seedExchange(t, e, e.Rep1, mine, "Q3 pricing")

	noActivity := graphPerms
	noActivity.Objects = map[string]principal.ObjectGrant{
		"person": {Read: true}, "organization": {Read: true}, "relationship": {Read: true},
	}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, noActivity)

	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200 — the edge stands on the person grant alone", code)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("edges = %d, want 1: losing the activity grant must not lose the route", len(graph.Edges))
	}
	if r := graph.Edges[0].Receipts; r != nil && len(*r) != 0 {
		t.Errorf("a caller with no activity grant was handed %d message(s)", len(*r))
	}
	if graph.Edges[0].Interactions90d == 0 {
		t.Error("the pooled count went with the receipts; it is disclosable where the messages are not")
	}
}

// The account arm is row-scoped IN the query. A coworker outside the caller's
// scope is absent, and the graph must not disclose them by adjacency.
func TestPersonGraphHidesCoworkersOutsideRowScope(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "ScaleCommerce", &e.Rep1)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	visible := e.SeedPerson(t, "Visible Coworker", &e.Rep1)
	hidden := e.SeedPerson(t, "Hidden Coworker", &e.Rep3)

	for _, p := range []ids.UUID{mine, visible, hidden} {
		SeedRow(t, owner, `INSERT INTO relationship
			(id, workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, $2, 'employment', '`+p.String()+`', '`+org.String()+`', 'manual', 'human:x')`, e.WS)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)
	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200", code)
	}
	for _, n := range graph.Nodes {
		if n.Label == "Hidden Coworker" {
			t.Fatal("the account arm named a coworker the caller's row scope hides")
		}
	}
	var named bool
	for _, n := range graph.Nodes {
		if n.Label == "Visible Coworker" {
			named = true
		}
	}
	if !named {
		t.Error("the account arm dropped a coworker the caller CAN read; the scope is too narrow")
	}
	// The remainder must not leak the hidden coworker either: it counts over
	// the same row-scoped predicate the page draws from.
	if graph.DroppedCount != nil && graph.DroppedCount.Account != nil && *graph.DroppedCount.Account != 0 {
		t.Errorf("dropped_count.account = %d, want 0 — it must count only what the caller may read",
			*graph.DroppedCount.Account)
	}
}

// A contact nobody has corresponded with recommends nothing. Inventing a route
// from an empty picture is the failure the evidence posture exists to avoid.
func TestPersonGraphRecommendsNothingWithoutAnEdge(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)

	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200", code)
	}
	if graph.Route != nil {
		t.Errorf("a contact with no exchanges produced a route via %q", graph.Route.ViaDisplayName)
	}
	if len(graph.Nodes) != 1 {
		t.Errorf("nodes = %d, want just the anchor", len(graph.Nodes))
	}
}

// An erased contact is archived in place with owner_id left alone, so the
// plain visibility probe still admits their owner. The graph uses the LIVE
// probe, which is what keeps it from serving who corresponded with a subject
// the controller certified erased.
func TestPersonGraphRefusesAnArchivedContact(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	seedExchange(t, e, e.Rep1, mine, "Q3 pricing")
	if _, err := owner.Exec(context.Background(),
		`UPDATE person SET archived_at = now() WHERE id = $1`, mine); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)
	if code, _ := readGraph(rep, t, e, mine); code != http.StatusNotFound {
		t.Errorf("graph of an archived contact → %d, want 404", code)
	}
}

// The service-level refusal, so the sentinel is asserted rather than inferred
// from a status code the handler chose.
func TestPersonGraphServiceReturnsNotFoundForAForeignContact(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)

	err := database.WithWorkspaceTx(rep, e.Pool, func(tx pgx.Tx) error {
		_, err := search.EdgesForPerson(rep, tx, theirs, 10)
		return err
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("EdgesForPerson out of scope → %v, want ErrNotFound", err)
	}
}

// A colleague can reach the contact directly AND know somebody else at the
// same company. They are one person and get one node; the two edges hang off
// it, or the diagram draws the same human twice.
func TestPersonGraphGivesAColleagueOneNodeAcrossBothArms(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "ScaleCommerce", &e.Rep1)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	coworker := e.SeedPerson(t, "Their Colleague", &e.Rep1)

	for _, p := range []ids.UUID{mine, coworker} {
		SeedRow(t, owner, `INSERT INTO relationship
			(id, workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, $2, 'employment', '`+p.String()+`', '`+org.String()+`', 'manual', 'human:x')`, e.WS)
	}
	// The SAME colleague corresponds with both.
	seedExchange(t, e, e.Rep1, mine, "with Anna")
	seedExchange(t, e, e.Rep1, coworker, "with their colleague")

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)
	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200", code)
	}
	seen := map[string]int{}
	for _, n := range graph.Nodes {
		seen[n.Id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("node %q appears %d times; one human is one node", id, n)
		}
	}
	if len(graph.Edges) != 2 {
		t.Fatalf("edges = %d, want 2 — one per relationship, both hanging off the one node", len(graph.Edges))
	}
	// The point of the case is that ONE human with two relationships is drawn
	// once. Counting nodes and rejecting duplicate ids cannot show that: a
	// graph that drew the colleague twice under two different ids, or hung the
	// two edges off unrelated nodes, satisfies both checks. So name the shared
	// end and require both edges to meet there.
	shared := ""
	for _, end := range []string{graph.Edges[0].From, graph.Edges[0].To} {
		if end == graph.Edges[1].From || end == graph.Edges[1].To {
			shared = end
		}
	}
	if shared == "" {
		t.Errorf("the two edges share no node (%s→%s and %s→%s); the one colleague is drawn as two people",
			graph.Edges[0].From, graph.Edges[0].To, graph.Edges[1].From, graph.Edges[1].To)
	}
	if len(graph.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3 — the anchor, the shared colleague, and the second contact", len(graph.Nodes))
	}
}

// The account arm shows counts and dates and never the messages. Pooled
// interaction metadata is disclosable where the correspondence behind it is
// not, so the absence of receipts here is the rule working, not missing data.
func TestPersonGraphAccountEdgesCarryCountsAndNoMessages(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "ScaleCommerce", &e.Rep1)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	coworker := e.SeedPerson(t, "Their Colleague", &e.Rep1)

	for _, p := range []ids.UUID{mine, coworker} {
		SeedRow(t, owner, `INSERT INTO relationship
			(id, workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, $2, 'employment', '`+p.String()+`', '`+org.String()+`', 'manual', 'human:x')`, e.WS)
	}
	// Nobody knows Anna; somebody knows her coworker. That is exactly the case
	// the account arm exists for.
	seedExchange(t, e, e.Rep1, coworker, "with their colleague")

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, graphPerms)
	code, graph := readGraph(rep, t, e, mine)
	if code != http.StatusOK {
		t.Fatalf("graph → %d, want 200", code)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("edges = %d, want the one account edge", len(graph.Edges))
	}
	if r := graph.Edges[0].Receipts; r != nil && len(*r) != 0 {
		t.Errorf("an account edge carried %d message(s); the counts are disclosable and the mail is not", len(*r))
	}
	if graph.Edges[0].Interactions90d == 0 {
		t.Error("the account edge carries no count, so it says nothing at all")
	}
	// And the route goes THROUGH the coworker, naming them — otherwise the
	// reader is told to ask somebody with no idea why.
	if graph.Route == nil {
		t.Fatal("nobody knows Anna directly and no indirect route was offered")
	}
	if graph.Route.ThroughDisplayName == nil || *graph.Route.ThroughDisplayName != "Their Colleague" {
		t.Error("the indirect route did not name who it goes through")
	}
}
