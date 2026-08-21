// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Executing a validated query plan against real rows, real RLS and the real
// schema (SEARCH-PARAM-7, execution half).
//
// The exit criterion of this work is a security property — two principals over
// ONE corpus get different answers and neither can infer the other's rows — so
// it is proven here rather than against a stub. The other half proven here is
// that the published vocabulary is answerable: a field the schema advertises
// and no table holds would refuse at execution what it advertised at
// discovery, and only the live catalog can say which fields those are.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// queryEnv is the SearchEnv plus the two halves of the query feature, wired
// the way compose wires them: one resolver behind both the validator and the
// published document, and the live schema behind both.
type queryEnv struct {
	*SearchEnv
	resolver  *search.VocabularyResolver
	validator *search.PlanValidator
	executor  *search.QueryExecutor
}

func setupQuery(t *testing.T) *queryEnv {
	t.Helper()
	e := SetupSearch(t)
	columns := search.NewColumnCatalog(e.DB())
	resolver := search.NewVocabularyResolver().WithColumnReader(columns)
	return &queryEnv{
		SearchEnv: e,
		resolver:  resolver,
		validator: search.NewPlanValidator(resolver),
		// No embedder: the offline posture, which is also what proves the
		// degradation is REPORTED rather than hidden.
		executor: search.NewQueryExecutor(e.Store, nil, columns),
	}
}

// queryObjects is every record type a plan can target, which is wider than the
// shared fixture's read set — a plan traverses into `project`, and a hop into a
// record type the caller cannot read is no hop at all. It is declared here
// rather than widened in SearchEnv: the suites riding that fixture assert what
// a reader SEES, and a grant added for this one would quietly widen theirs.
//
// `relationship` is here for the same reason and is not a record type: it is
// the RBAC object governing the EDGE a join-table hop reads, and without it
// TestEveryPublishedRelationExecutes would quietly stop covering the
// employment and stakeholder hops rather than fail.
var queryObjects = []string{"person", "organization", "deal", "lead", "project", "activity", "relationship"}

func queryGrants() map[string]principal.ObjectGrant {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range queryObjects {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	return grants
}

// admin reads every record type with nothing hidden — the positive control the
// team rep's narrower view is measured against.
func (q *queryEnv) admin() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), q.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: queryGrants(), RowScope: principal.RowScopeAll},
	})
}

// teamRep is the same object vocabulary with the row scope narrowed to one
// team, so a comparison isolates row scope from object RBAC.
func (q *queryEnv) teamRep(user, team ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), q.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     []ids.UUID{team},
		Permissions: principal.Permissions{Objects: queryGrants(), RowScope: principal.RowScopeTeam},
	})
}

// run decodes, validates and executes one plan document, failing on anything
// that is not an answer.
func (q *queryEnv) run(ctx context.Context, t *testing.T, doc string) search.QueryResult {
	t.Helper()
	result, err := q.answer(ctx, doc)
	if err != nil {
		t.Fatalf("plan %s\n  → %v", doc, err)
	}
	return result
}

func (q *queryEnv) answer(ctx context.Context, doc string) (search.QueryResult, error) {
	plan, err := search.DecodePlan([]byte(doc))
	if err != nil {
		return search.QueryResult{}, err
	}
	validated, err := q.validator.Validate(ctx, plan)
	if err != nil {
		return search.QueryResult{}, err
	}
	return q.executor.Execute(ctx, validated)
}

// queryFixture is one corpus split across two teams: each rep owns a deal at
// an organization they own, so a rep sees exactly their own half through
// either the target or the hop.
type queryFixture struct {
	rep1Org, rep3Org   ids.UUID
	rep1Deal, rep3Deal ids.UUID
	sharedDeal         ids.UUID
	project            ids.UUID
}

// seedLocatedCompany inserts a company that has already been geocoded: an
// address, a point, and the 'ok' status that says the two match.
//
// The status is written explicitly rather than left to the worker, because
// this lane has no geocoder and never will — what is under test is whether the
// QUERY reads the columns correctly, not whether Nominatim answers.
func (q *queryEnv) seedLocatedCompany(t *testing.T, name string, lat, lon float64) ids.UUID {
	t.Helper()
	return q.SeedID(t, `INSERT INTO organization
		(id, owner_id, display_name, address_line1, address_city,
		 geocode_lat, geocode_lon, geocode_status, geocode_provider, geocode_input_hash,
		 source, captured_by)
		VALUES ($1, $2, $3, 'Teststrasse 1', 'Teststadt', $4, $5, 'ok', 'test', 'seeded', 'manual', 'human:x')`,
		q.Rep1, name, lat, lon)
}

// seedUnlocatedCompany inserts a company with an address and NO coordinates —
// the state every company is in before the worker has run. It must be absent
// from a radius answer rather than placed at the origin.
func (q *queryEnv) seedUnlocatedCompany(t *testing.T, name string) ids.UUID {
	t.Helper()
	return q.SeedID(t, `INSERT INTO organization
		(id, owner_id, display_name, address_line1, address_city, source, captured_by)
		VALUES ($1, $2, $3, 'Unbekannt 1', 'Teststadt', 'manual', 'human:x')`, q.Rep1, name)
}

// moveCompany changes a company's address the way any writer does, and lets
// the SCHEMA do the invalidating — no test-only status update, because what is
// under test is that the trigger fires for an ordinary write.
func (q *queryEnv) moveCompany(t *testing.T, org ids.UUID, line1 string) {
	t.Helper()
	if _, err := q.Owner.Exec(context.Background(),
		`UPDATE organization SET address_line1 = $2 WHERE id = $1`, org, line1); err != nil {
		t.Fatalf("moving the company: %v", err)
	}
}

func (q *queryEnv) seedFixture(t *testing.T) queryFixture {
	t.Helper()
	pipeline := q.SeedID(t, `INSERT INTO pipeline (id, name, is_default, position) VALUES ($1, 'Sales', true, 0)`)
	stage := q.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, pipeline)

	var f queryFixture
	f.rep1Org = q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, address_city, source, captured_by)
		VALUES ($1, $2, 'Stuttgart Werke', 'Stuttgart', 'manual', 'human:x')`, q.Rep1)
	f.rep3Org = q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, address_city, source, captured_by)
		VALUES ($1, $2, 'Stuttgart Logistik', 'Stuttgart', 'manual', 'human:x')`, q.Rep3)
	// A project named like a deal, so the traversal proves that two tables
	// sharing a column name resolve to the right one.
	f.project = q.SeedID(t, `INSERT INTO project (id, owner_id, name, organization_id, source, captured_by)
		VALUES ($1, $2, 'Rollout', $3, 'manual', 'human:x')`, q.Rep1, f.rep1Org)

	f.rep1Deal = q.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, organization_id, project_id, amount_minor, currency, status, expected_close_date, source, captured_by)
		VALUES ($1, $2, 'Rollout', $3, $4, $5, $6, 100000, 'EUR', 'open', '2026-12-01', 'manual', 'human:x')`,
		q.Rep1, pipeline, stage, f.rep1Org, f.project)
	f.rep3Deal = q.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, status, expected_close_date, source, captured_by)
		VALUES ($1, $2, 'Logistik Rahmenvertrag', $3, $4, $5, 250000, 'EUR', 'open', '2026-11-01', 'manual', 'human:x')`,
		q.Rep3, pipeline, stage, f.rep3Org)
	// An ownerless deal is workspace-shared and visible at every tier — the
	// control that keeps "the rep sees fewer rows" from being read as "the rep
	// sees only their own".
	f.sharedDeal = q.SeedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, status, source, captured_by)
		VALUES ($1, 'Unowned Deal', $2, $3, 50000, 'EUR', 'open', 'manual', 'human:x')`, pipeline, stage)
	return f
}

func TestQueryPlanAnswersExactPredicatesCompletely(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"},
		          {"field": "amount_minor", "op": "gte", "value": 100000}]}`)

	if got := idSet(result); !got[f.rep1Deal] || !got[f.rep3Deal] || got[f.sharedDeal] {
		t.Fatalf("rows are %v", rowNames(result))
	}
	if result.Coverage != search.CoverageCompleteExact {
		t.Errorf("coverage is %q with notes %v", result.Coverage, result.Notes)
	}
	if len(result.Notes) != 0 {
		t.Errorf("a complete answer carries notes: %v", result.Notes)
	}
	if !strings.Contains(result.Narrative, `status is "open"`) {
		t.Errorf("narrative is %q", result.Narrative)
	}
	for _, row := range result.Rows {
		if row.Title == "" {
			t.Errorf("row %s has no title", row.ID)
		}
	}
}

// The exit criterion. Two principals, one corpus: the rep's answer is a strict
// subset of the admin's, and nothing in the rep's answer — not the rows, not
// the count, not the coverage verdict — is computed over a row they cannot see.
//
// The target is `organization`, the record type that still narrows a reader:
// every shareable record type is read by every seat holding the object grant
// (platform/auth tableclass.go), so capture privacy is the one narrowing left
// and two principals asking about anything else get the same answer by design.
func TestQueryPlanAnswersTwoPrincipalsFromOneCorpusWithoutLeaking(t *testing.T) {
	q := setupQuery(t)
	// Rep1's unpromoted capture: theirs alone until a human promotes it, and
	// capture privacy does not yield to row_scope=all — so the two principals
	// compared here are the capture's OWNER and a colleague, not an admin and
	// a rep.
	rep1Capture := q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, visibility, source, captured_by)
		VALUES ($1, $2, 'Rollout', 'owner', 'manual', 'human:x')`, q.Rep1)
	// An ownerless workspace-visible company is visible at every tier — the
	// control that keeps "the colleague sees fewer rows" from being read as
	// "the colleague sees nothing".
	sharedOrg := q.SeedID(t, `INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Rollout', 'manual', 'human:x')`)
	rep3Org := q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Rollout', 'manual', 'human:x')`, q.Rep3)
	const plan = `{"version": "v1", "target": "organization",
		"where": [{"field": "display_name", "op": "eq", "value": "Rollout"}]}`

	owner := idSet(q.run(q.teamRep(q.Rep1, q.Team1), t, plan))
	colleague := idSet(q.run(q.teamRep(q.Rep3, q.Team2), t, plan))

	if len(owner) != 3 {
		t.Fatalf("the capture's owner sees %d of 3 companies — the corpus is not what the "+
			"narrowed arm is measured against", len(owner))
	}
	// A workspace-visible company is read by every seat, whoever owns it.
	if !colleague[sharedOrg] || !colleague[rep3Org] {
		t.Fatalf("the colleague cannot see the rows they are entitled to: %v", colleague)
	}
	if colleague[rep1Capture] {
		t.Fatal("the colleague sees another rep's unpromoted capture")
	}
	for id := range colleague {
		if !owner[id] {
			t.Fatalf("the colleague sees a row the wider reader does not: %s", id)
		}
	}
}

// A hop is a READ of the record it lands on. Filtering by an organization the
// caller cannot see must not admit rows through it — otherwise the answer's
// membership discloses a record the row scope hides.
func TestQueryPlanTraversalCarriesTheHopsOwnRowScope(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	const plan = `{"version": "v1", "target": "deal",
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`

	admin := idSet(q.run(q.admin(), t, plan))
	if !admin[f.rep1Deal] || !admin[f.rep3Deal] {
		t.Fatalf("the unbounded reader does not reach both Stuttgart deals: %v", admin)
	}
	// rep3 captured the other organization privately, which is what keeps
	// an organization out of a colleague's row scope. Their deal — readable
	// in itself — is reachable through this hop only via an organization
	// rep1 cannot read.
	if _, err := q.Owner.Exec(context.Background(),
		`UPDATE organization SET visibility = 'owner' WHERE id = $1`, f.rep3Org); err != nil {
		t.Fatalf("capturing the organization privately: %v", err)
	}
	rep := idSet(q.run(q.teamRep(q.Rep1, q.Team1), t, plan))
	if rep[f.rep3Deal] {
		t.Fatal("a hop through an organization the caller cannot read admitted a row")
	}
	if !rep[f.rep1Deal] {
		t.Fatalf("the rep cannot reach their own deal through their own organization: %v", rep)
	}
}

// The hop comes back as the record that admitted the row — a traversal that is
// legible as a reason rather than as an invisible filter.
func TestQueryPlanTraversalReturnsTheRecordThatAdmittedTheRow(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "amount_minor", "op": "eq", "value": 100000}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	if len(result.Rows) != 1 {
		t.Fatalf("rows are %v", rowNames(result))
	}
	evidence := result.Rows[0].Evidence
	if len(evidence) != 1 {
		t.Fatalf("the row carries %d pieces of evidence", len(evidence))
	}
	if evidence[0].ID != f.rep1Org || evidence[0].Type != "organization" || evidence[0].Relation != "organization" {
		t.Fatalf("evidence is %+v", evidence[0])
	}
	if evidence[0].Title != "Stuttgart Werke" {
		t.Errorf("evidence title is %q", evidence[0].Title)
	}
}

// deal.name and project.name are both `name`. The hop's title expression must
// resolve to the HOP's table, or the statement is ambiguous — and an ambiguity
// this shape is invisible until two tables happen to share a column name.
func TestQueryPlanTraversalBetweenTablesThatShareAColumnName(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal",
		"traverse": {"relation": "project", "where": [{"field": "name", "op": "eq", "value": "Rollout"}]}}`)
	if len(result.Rows) != 1 || result.Rows[0].ID != f.rep1Deal {
		t.Fatalf("rows are %v", rowNames(result))
	}
	if len(result.Rows[0].Evidence) != 1 || result.Rows[0].Evidence[0].ID != f.project {
		t.Fatalf("evidence is %+v", result.Rows[0].Evidence)
	}
}

// The inverse edge is derived from the referring record's column, and executes
// in the other direction.
func TestQueryPlanTraversalFollowsAnInverseEdge(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "organization",
		"traverse": {"relation": "deals",
		             "where": [{"field": "amount_minor", "op": "gte", "value": 200000}]}}`)
	if len(result.Rows) != 1 || result.Rows[0].ID != f.rep3Org {
		t.Fatalf("rows are %v", rowNames(result))
	}
}

// v1 has no cursor member, so a caller who hits the limit cannot ask for the
// rest. Calling that complete would be the silent narrowing the whole feature
// exists to prevent.
func TestQueryPlanTruncationIsDegradationRatherThanPagination(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"}], "limit": 2}`)
	if len(result.Rows) != 2 {
		t.Fatalf("the page carries %d rows", len(result.Rows))
	}
	if result.Coverage != search.CoveragePartialDegraded {
		t.Fatalf("coverage is %q", result.Coverage)
	}
	if !hasNote(result, search.CodeResultTruncated) {
		t.Fatalf("notes are %v", result.Notes)
	}
}

// An answer that fits says so, and carries no truncation note.
func TestQueryPlanAnAnswerThatFitsIsNotReportedAsTruncated(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"}], "limit": 3}`)
	if len(result.Rows) != 3 || result.Coverage != search.CoverageCompleteExact {
		t.Fatalf("coverage is %q over %d rows", result.Coverage, len(result.Rows))
	}
}

// SEARCH-AC-17 against real data: a question this deployment cannot answer
// returns its note and NO row count, so nothing leaks the size of an answer
// the caller cannot have.
//
// A PERSON, because that is what is genuinely unanswerable now: a person has an
// address, so the field and the operator both exist, but this product does not
// geocode where people live and there is nothing to measure from. A company's
// radius runs — see the test below.
func TestQueryPlanAnUnanswerablePredicateReturnsItsNoteNotRows(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "person",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"center": "Stuttgart", "radius_km": 50}}]}`)
	if len(result.Rows) != 0 {
		t.Fatalf("an unanswerable plan returned %d rows over a populated corpus", len(result.Rows))
	}
	if !hasNote(result, search.CodeDistanceRankingUnavailable) {
		t.Fatalf("notes are %v", result.Notes)
	}
}

// The whole point, against Postgres: a radius on a company returns the ones
// inside it, NEAREST FIRST, each saying how far.
//
// This is the test that proves the SQL rather than the Go. The haversine, the
// bounding box, the geocode_status filter and the distance projection are all
// strings until a database evaluates them.
func TestQueryPlanARadiusAnswersNearestFirstWithDistances(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)

	// Three companies at known distances from Stuttgart (48.7758, 9.1829), and
	// one far outside the radius. Distances are roughly 0km, 12km, 27km and
	// 190km (Munich), so a 50km radius admits exactly three.
	near := q.seedLocatedCompany(t, "Radius Zentrum GmbH", 48.7758, 9.1829)
	mid := q.seedLocatedCompany(t, "Radius Mitte AG", 48.8800, 9.1829)
	far := q.seedLocatedCompany(t, "Radius Rand KG", 49.0200, 9.1829)
	q.seedLocatedCompany(t, "Radius Muenchen GmbH", 48.1351, 11.5820)
	// A company with an address and no coordinates: it must be absent rather
	// than placed at the origin.
	q.seedUnlocatedCompany(t, "Radius Ungeocodiert GmbH")

	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "organization",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"lat": 48.7758, "lon": 9.1829, "radius_km": 50}}],
		"limit": 20}`)

	if hasNote(result, search.CodeDistanceRankingUnavailable) {
		t.Fatalf("a company radius still reports itself unavailable: %v", result.Notes)
	}
	got := make([]ids.UUID, 0, len(result.Rows))
	for _, row := range result.Rows {
		got = append(got, row.ID)
	}
	if len(got) != 3 {
		t.Fatalf("the radius returned %d companies, want the 3 inside it: %v", len(got), got)
	}
	if got[0] != near || got[1] != mid || got[2] != far {
		t.Errorf("rows came back in the order %v; want nearest first (%v, %v, %v)", got, near, mid, far)
	}
	for i, row := range result.Rows {
		if row.DistanceKM == nil {
			t.Fatalf("row %d carries no distance, so the answer cannot say how far", i)
		}
		if *row.DistanceKM > 50 {
			t.Errorf("row %d is %vkm away, outside the 50km asked for", i, *row.DistanceKM)
		}
		if i > 0 && *result.Rows[i-1].DistanceKM > *row.DistanceKM {
			t.Errorf("row %d (%vkm) sorts after row %d (%vkm)",
				i-1, *result.Rows[i-1].DistanceKM, i, *row.DistanceKM)
		}
	}
}

// A company whose address MOVED drops out of the answer rather than appearing
// at its old coordinates.
//
// This is the staleness design proving itself end to end. The trigger marks the
// row stale when the address changes; the radius reads geocode_status and takes
// only 'ok'. Without either half, this company would answer from where it used
// to be — and the answer would look exactly like a correct one.
func TestQueryPlanACompanyThatMovedIsNotAnsweredFromItsOldAddress(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	moved := q.seedLocatedCompany(t, "Radius Umgezogen GmbH", 48.7758, 9.1829)

	before := q.run(q.admin(), t, `{
		"version": "v1", "target": "organization",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"lat": 48.7758, "lon": 9.1829, "radius_km": 10}}],
		"limit": 20}`)
	if !containsID(before.Rows, moved) {
		t.Fatal("the company was not in the answer before it moved, so this test proves nothing")
	}

	q.moveCompany(t, moved, "Neue Strasse 1")

	after := q.run(q.admin(), t, `{
		"version": "v1", "target": "organization",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"lat": 48.7758, "lon": 9.1829, "radius_km": 10}}],
		"limit": 20}`)
	if containsID(after.Rows, moved) {
		t.Error("a company that changed its address still answers from its OLD coordinates — " +
			"either the staleness trigger did not fire or the radius is not reading geocode_status")
	}
}

func containsID(rows []search.QueryRow, want ids.UUID) bool {
	for _, row := range rows {
		if row.ID == want {
			return true
		}
	}
	return false
}

// A similarity clause with no embedding lane bound ranks lexically. The
// degradation is reported, and the answer never labels itself complete.
func TestQueryPlanARankedAnswerNeverLabelsItselfComplete(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal", "similar_to": "Rollout"}`)
	if result.Coverage == search.CoverageCompleteExact {
		t.Fatal("a ranked answer labelled itself complete")
	}
	if !hasNote(result, search.CodeSemanticRankingDegraded) {
		t.Fatalf("an unbound embedding lane was not reported: %v", result.Notes)
	}
	if len(result.Rows) != 1 || result.Rows[0].ID != f.rep1Deal {
		t.Fatalf("rows are %v", rowNames(result))
	}
	if !strings.Contains(result.Narrative, "ranked by similarity") {
		t.Errorf("narrative is %q", result.Narrative)
	}
}

// The ranking runs WITHIN the plan's record type. A global page filtered
// afterwards spends itself on types the plan never named — here the same word
// names an organization, a project and a deal, and a page of two would answer
// no deals at all for a corpus that has one.
func TestQueryPlanRanksWithinTheRecordTypeItWasAsked(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	// "Rollout" names the project and the deal; the organizations rank on
	// "Stuttgart". Asking about deals must answer the deal.
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal", "similar_to": "Rollout", "limit": 1}`)
	if len(result.Rows) != 1 || result.Rows[0].ID != f.rep1Deal {
		t.Fatalf("rows are %v", rowNames(result))
	}
	for _, row := range result.Rows {
		if row.Type != "deal" {
			t.Errorf("a %s row came back for a deal plan", row.Type)
		}
	}
}

// A ranking that admits nothing answers nothing. Running the statement without
// the membership test would answer the unfiltered question instead — every
// deal in the workspace, under a sentence promising a ranked few.
func TestQueryPlanARankingThatMatchesNothingAnswersNoRows(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	result := q.run(q.admin(), t, `{
		"version": "v1", "target": "deal", "similar_to": "zzzznothingmatchesthis"}`)
	if len(result.Rows) != 0 {
		t.Fatalf("a ranking that matched nothing answered %d rows", len(result.Rows))
	}
}

// The archived and discovery narrowing every read carries, carried here too.
func TestQueryPlanNeverReturnsArchivedRecordsOrTheOwnCompany(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	anchor := q.SeedID(t, `INSERT INTO organization (id, display_name, is_anchor, source, captured_by)
		VALUES ($1, 'Our Own Company', true, 'manual', 'human:x')`)
	if _, err := q.Owner.Exec(context.Background(),
		`UPDATE organization SET archived_at = now() WHERE id = $1`, f.rep3Org); err != nil {
		t.Fatal(err)
	}
	got := idSet(q.run(q.admin(), t, `{"version": "v1", "target": "organization"}`))
	if got[anchor] {
		t.Error("the installation's own company is discoverable through a query plan")
	}
	if got[f.rep3Org] {
		t.Error("an archived organization is returned")
	}
	if !got[f.rep1Org] {
		t.Error("a live organization is missing")
	}
}

// The fitness function this PR turns on: EVERY field the vocabulary publishes
// is answerable end to end, against the real schema — either as rows, or, for
// a place, as the declared note that says this deployment cannot rank by it. A
// field the contract declares and no table holds would otherwise refuse at
// execution what it advertised at discovery, and only the live catalog can say
// which fields those are.
func TestEveryPublishedFieldCompilesToAStoragePath(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	ctx := q.admin()
	vocab, err := q.resolver.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vocab.Targets) == 0 {
		t.Fatal("the vocabulary publishes no record types")
	}
	for _, target := range vocab.Targets {
		if len(target.Fields) == 0 {
			t.Errorf("%s publishes no fields", target.Target)
		}
		for _, field := range target.Fields {
			doc := fmt.Sprintf(`{"version": "v1", "target": %q, "where": [{"field": %q, "op": %q, "value": %s}]}`,
				target.Target, field.Name, field.Ops[0], probeOperand(field.Kind))
			result, err := q.answer(ctx, doc)
			if err != nil {
				t.Errorf("%s.%s (%s) is published and cannot be asked: %v", target.Target, field.Name, field.Kind, err)
				continue
			}
			// A PLACE is the one published field with no storage behind it,
			// and it is published precisely so that it answers the note rather
			// than reading as an unknown operator (SEARCH-AC-17). Asserting
			// that here is what keeps this test's claim exact: every other
			// published field runs, and this one is declared unanswerable
			// rather than quietly exempt.
			if field.Kind == search.KindGeo {
				if !hasNote(result, search.CodeDistanceRankingUnavailable) {
					t.Errorf("%s.%s is a place and did not answer %s: %v",
						target.Target, field.Name, search.CodeDistanceRankingUnavailable, result.Notes)
				}
				continue
			}
			if hasNote(result, search.CodeDistanceRankingUnavailable) {
				t.Errorf("%s.%s (%s) answered a place's note", target.Target, field.Name, field.Kind)
			}
		}
	}
}

// The same invariant one level up: every HOP the vocabulary publishes executes.
// A forward hop is filtered incidentally — its reference is one of the target's
// own fields — but an inverse hop is declared by the referring record and joins
// on THAT table's column, so it is the direction that can be published against
// a column no table holds. That failure is a database error rather than a
// refusal, which is the one thing a validated plan must never produce.
func TestEveryPublishedRelationExecutes(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	ctx := q.admin()
	vocab, err := q.resolver.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hops := 0
	for _, target := range vocab.Targets {
		for _, relation := range target.Relations {
			hops++
			doc := fmt.Sprintf(`{"version": "v1", "target": %q, "traverse": {"relation": %q}}`,
				target.Target, relation.Name)
			if _, err := q.answer(ctx, doc); err != nil {
				t.Errorf("%s → %s (via %s) is published and cannot be traversed: %v",
					target.Target, relation.Name, relation.Via, err)
			}
		}
	}
	if hops == 0 {
		t.Fatal("no hops were exercised; the vocabulary publishes no traversals at all")
	}
}

// probeOperand is one well-formed operand per kind, so the fitness function
// above exercises the storage path rather than the operand check.
func probeOperand(kind search.FieldKind) string {
	switch kind {
	case search.KindNumber:
		return "1"
	case search.KindBoolean:
		return "true"
	case search.KindDate:
		return `"2026-01-01"`
	case search.KindTimestamp:
		return `"2026-01-01T00:00:00Z"`
	case search.KindID:
		return `"01999999-0000-7000-8000-000000000001"`
	case search.KindGeo:
		return `{"center": "Stuttgart", "radius_km": 50}`
	default:
		return `"probe"`
	}
}

// The published document is a read of the caller's own surface, so it narrows
// with them — and what it narrows to is still answerable.
func TestThePublishedVocabularyNarrowsWithTheCaller(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	admin, err := q.resolver.Resolve(q.admin())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(admin.TargetNames())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "deal") {
		t.Fatalf("the admin's vocabulary is %s", body)
	}
	// A field no table holds is absent from the document rather than present
	// and unanswerable.
	deal, ok := admin.Target("deal")
	if !ok {
		t.Fatal("deal absent from the admin's vocabulary")
	}
	if _, ok := deal.Field("stalled"); ok {
		t.Error("`stalled` is computed in the record mapper and is published as askable")
	}
	if _, ok := deal.Field("status"); !ok {
		t.Error("`status` is a column and is not published")
	}
}

func idSet(result search.QueryResult) map[ids.UUID]bool {
	out := map[ids.UUID]bool{}
	for _, row := range result.Rows {
		out[row.ID] = true
	}
	return out
}

func rowNames(result search.QueryResult) []string {
	names := make([]string, len(result.Rows))
	for i, row := range result.Rows {
		names[i] = row.Title
	}
	return names
}

func hasNote(result search.QueryResult, code string) bool {
	for _, note := range result.Notes {
		if note.Code == code {
			return true
		}
	}
	return false
}
