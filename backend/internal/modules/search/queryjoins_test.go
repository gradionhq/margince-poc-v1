// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// joinSchema is the two join tables as the real schema declares them, plus the
// record tables a hop lands on. Written from the DDL rather than from the
// derivation, so a test cannot agree with the code by construction.
var joinSchema = map[string][]StoredColumn{
	"person":       columnsOf("id:uuid", "full_name", "owner_id:uuid", "visibility"),
	"organization": columnsOf("id:uuid", "display_name", "owner_id:uuid", "is_anchor:boolean", "visibility"),
	"deal":         columnsOf("id:uuid", "name", "owner_id:uuid", "organization_id:uuid", "visibility"),
	"lead":         columnsOf("id:uuid", "full_name", "owner_id:uuid", "visibility"),
	"project":      columnsOf("id:uuid", "name", "owner_id:uuid", "organization_id:uuid", "visibility"),
	"activity":     columnsOf("id:uuid", "subject", "kind", "owner_id:uuid", "visibility"),
	// core 0007 + 0131. counterparty_org_id is deliberately here: it is the
	// column the derivation must NOT read.
	"relationship": columnsOf("id:uuid", "kind", "person_id:uuid", "organization_id:uuid",
		"counterparty_org_id:uuid", "deal_id:uuid", "project_id:uuid",
		"archived_at:timestamp with time zone", "started_at:date", "ended_at:date"),
	// core 0008 + 0038 + 0131. Five arms, and no archived_at.
	"activity_link": columnsOf("id:uuid", "activity_id:uuid", "entity_type",
		"person_id:uuid", "organization_id:uuid", "deal_id:uuid", "lead_id:uuid",
		"project_id:uuid"),
}

// relationsOf resolves one record type's vocabulary against joinSchema and
// answers its hops by name.
func relationsOf(t *testing.T, entity string) map[string]Relation {
	t.Helper()
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: joinSchema})
	readable := make([]string, 0, len(contractRecords))
	for record := range contractRecords {
		readable = append(readable, record)
	}
	vocab, err := resolver.Resolve(readerFor(readable...), entity)
	if err != nil {
		t.Fatalf("resolving %s: %v", entity, err)
	}
	if len(vocab.Targets) != 1 {
		t.Fatalf("resolving %s answered %d targets", entity, len(vocab.Targets))
	}
	byName := map[string]Relation{}
	for _, relation := range vocab.Targets[0].Relations {
		byName[relation.Name] = relation
	}
	return byName
}

// The defect this file exists for, in the direction it was reported: a person's
// employer lives in `relationship`, so before the join derivation `person` had
// no hop at all and the question could not be asked.
func TestAPersonTraversesToTheirEmployerAndAnOrganizationToItsPeople(t *testing.T) {
	fromPerson := relationsOf(t, entityPerson)
	employer, ok := fromPerson["organizations"]
	if !ok {
		t.Fatalf("person has no hop to its employer; it has %v", slices.Sorted(maps.Keys(fromPerson)))
	}
	if employer.Target != entityOrganization {
		t.Errorf("person → organizations lands on %q", employer.Target)
	}
	if employer.Join == nil {
		t.Fatal("person → organizations is not a join edge, so it was derived from a column person does not have")
	}
	if employer.Join.Table != "relationship" || employer.Join.From != "person_id" || employer.Join.To != "organization_id" {
		t.Errorf("person → organizations runs through %+v, not relationship(person_id → organization_id)", *employer.Join)
	}

	// The inverse is the same edge read the other way, and it must exist for
	// the same reason: an account's staff is the question the employment table
	// was built to answer.
	fromOrg := relationsOf(t, entityOrganization)
	people, ok := fromOrg["persons"]
	if !ok {
		t.Fatalf("organization has no hop to its people; it has %v", slices.Sorted(maps.Keys(fromOrg)))
	}
	if people.Join == nil || people.Join.From != "organization_id" || people.Join.To != "person_id" {
		t.Errorf("organization → persons runs through %+v, not relationship(organization_id → person_id)", people.Join)
	}
}

// The half nobody had noticed was missing. An activity's links live in
// `activity_link`, so `activity` could name nothing it was about — and a
// derived vocabulary with a blind spot looks complete while saying so.
func TestAnActivityTraversesToEveryRecordItLinks(t *testing.T) {
	relations := relationsOf(t, entityActivity)
	for _, want := range []string{"persons", "organizations", "deals", "leads", "projects"} {
		relation, ok := relations[want]
		if !ok {
			t.Errorf("activity has no hop %q; it has %v", want, slices.Sorted(maps.Keys(relations)))
			continue
		}
		if relation.Join == nil || relation.Join.Table != "activity_link" {
			t.Errorf("activity → %s does not run through activity_link: %+v", want, relation.Join)
		}
	}
	// All five arms, and the fifth is why this is a list rather than a spot
	// check: 0131 added the project arm to a table 0038 had already widened
	// once, and a derivation that hard-coded the arms it was written against
	// would still pass a test written against the same stale list.
	if len(relations) != 5 {
		t.Errorf("activity publishes %d hops, not the five arms activity_link declares: %v",
			len(relations), slices.Sorted(maps.Keys(relations)))
	}
	// The name a caller has to type. The naive plural rule produces `activitys`,
	// which reached the vocabulary the moment a join edge gave activity its
	// first inverse hop.
	fromProject := relationsOf(t, entityProject)
	if _, ok := fromProject["activities"]; !ok {
		t.Errorf("project's hop back to its timeline is misnamed; it has %v",
			slices.Sorted(maps.Keys(fromProject)))
	}
}

// Every relation is derived from a column, so a column that is not there
// publishes nothing. This is the same bar the field vocabulary holds and the
// reason a hop can be trusted to compile.
func TestAJoinHopIsPublishedOnlyWhenBothColumnsAreThere(t *testing.T) {
	narrowed := map[string][]StoredColumn{}
	for table, columns := range joinSchema {
		narrowed[table] = columns
	}
	// The employment edge, with the far side taken away.
	narrowed["relationship"] = columnsOf("id:uuid", "kind", "person_id:uuid",
		"archived_at:timestamp with time zone", "ended_at:date")
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: narrowed})
	vocab, err := resolver.Resolve(readerFor(entityPerson, entityOrganization), entityPerson)
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range vocab.Targets[0].Relations {
		if relation.Join != nil && relation.Join.Table == "relationship" {
			t.Errorf("a relationship hop to %s was published from a table with only one reference column",
				relation.Target)
		}
	}
}

// A join edge has no contract spelling to fall back on the way a field does,
// so an unwired deployment publishes none rather than publishing one nothing
// confirmed. Narrower than the unwired field vocabulary, and deliberately.
func TestNoJoinRelationIsPublishedWithoutASchemaToConfirmIt(t *testing.T) {
	vocab, err := NewVocabularyResolver().Resolve(readerFor(entityPerson, entityOrganization), entityPerson)
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range vocab.Targets[0].Relations {
		if relation.Join != nil {
			t.Errorf("a join hop to %s was published with no column reader wired", relation.Target)
		}
	}
}

// Via is prose for a join edge, and the executor must never read it for
// structure: `relationship(person_id → organization_id)` contains a dot, which
// the inverse spelling `deal.organization_id` also does. Reading it would bind
// the hop to a column the join table does not have.
func TestAJoinEdgeIsNeverReadAsAnInverseOne(t *testing.T) {
	relation := Relation{
		Name: "organizations", Target: entityOrganization,
		Via:  joinVia("relationship", "person_id", "organization_id"),
		Join: &JoinEdge{Table: "relationship", From: "person_id", To: "organization_id"},
	}
	branch, _ := branchFor(entityOrganization)
	hop := newHopBinding(relation, branch, unfilteredStorage())
	if hop.column != "" || hop.forward {
		t.Fatalf("a join edge bound as a scalar one: column=%q forward=%v", hop.column, hop.forward)
	}
	compiler := &planCompiler{}
	condition := compiler.edgeCondition(hop)
	if !strings.Contains(condition, "FROM \"relationship\" j") {
		t.Errorf("the edge does not traverse the join table: %s", condition)
	}
	if strings.Contains(condition, "t.organization_id") || strings.Contains(condition, "h.person_id = t.id") {
		t.Errorf("the edge compiled against a record column instead of the join table: %s", condition)
	}
}

// An archived edge carries no hop where the join table records one, and the
// clause is absent where it does not — `activity_link` has no archived_at, and
// naming one would be a database error on every plan that traversed it.
func TestAJoinHopReadsItsLifecycleGuardsOffTheTableRatherThanAssumingThem(t *testing.T) {
	employment := relationsOf(t, entityPerson)["organizations"]
	if employment.Join == nil || !employment.Join.Archivable || !employment.Join.Ends {
		t.Fatalf("the employment edge does not know what relationship's lifecycle columns are: %+v",
			employment.Join)
	}
	condition := joinEdgeCondition(*employment.Join)
	// Deleted, and left. Two questions, and filtering only the first is what
	// makes a company a person left go on reading as where they work.
	if !strings.Contains(condition, "j.archived_at IS NULL") {
		t.Errorf("an archived employment still carries a hop: %s", condition)
	}
	if !strings.Contains(condition, "j.ended_at IS NULL OR j.ended_at > current_date") {
		t.Errorf("a job the person left still carries a hop: %s", condition)
	}
	// A future ended_at is a notice period and is still current, so the
	// comparison is against a date rather than a presence check.
	if strings.Contains(condition, "j.ended_at IS NULL)") {
		t.Errorf("the hop drops a person serving out their notice: %s", condition)
	}

	link := relationsOf(t, entityActivity)["persons"]
	if link.Join == nil || link.Join.Archivable || link.Join.Ends {
		t.Fatalf("the activity link edge invented a lifecycle column: %+v", link.Join)
	}
	if linked := joinEdgeCondition(*link.Join); strings.Contains(linked, "archived_at") ||
		strings.Contains(linked, "ended_at") {
		t.Errorf("the activity_link hop names a column that table does not have: %s", linked)
	}
}

// Two derivations feed ONE namespace, so a name can be claimed twice, and which
// hop then runs would depend on the order the derivations happened to run in —
// right until somebody sorts a slice. The scalar edge is the one a caller
// naming it means: it is the record's own declared reference, where a join edge
// is a weaker path to the same record type through a table between them.
//
// No pair of derivations collides on today's schema, which is why this asserts
// against mergeRelations rather than against a resolved vocabulary: a test
// written at the vocabulary level would pass without the merge ever running,
// and would go on passing if the rule were reversed.
func TestADirectEdgeWinsAJoinEdgeOfTheSameName(t *testing.T) {
	direct := []Relation{{Name: "deals", Target: entityDeal, Via: "deal.organization_id"}}
	joined := []Relation{{
		Name: "deals", Target: entityDeal, Via: joinVia("relationship", "organization_id", "deal_id"),
		Join: &JoinEdge{Table: "relationship", From: "organization_id", To: "deal_id"},
	}}
	merged := mergeRelations(direct, joined)
	if len(merged) != 1 {
		t.Fatalf("one name resolved to %d hops: %+v", len(merged), merged)
	}
	if merged[0].Join != nil {
		t.Errorf("the join edge won the name %q over the record's own reference", merged[0].Name)
	}
	if merged[0].Via != "deal.organization_id" {
		t.Errorf("the surviving hop is derived from %q", merged[0].Via)
	}
	// A join edge whose name nothing else claims still reaches the vocabulary —
	// the merge drops a duplicate, never a hop.
	free := []Relation{{
		Name: "persons", Target: entityPerson,
		Join: &JoinEdge{Table: "relationship", From: "organization_id", To: "person_id"},
	}}
	if got := mergeRelations(direct, append(joined, free...)); len(got) != 2 {
		t.Errorf("the merge dropped an uncontested hop: %+v", got)
	}
}

// `relationship.counterparty_org_id` names its ROLE rather than its target, so
// the same test that keeps `deal.owner_id` out of the vocabulary keeps it out
// here. Org↔org partner edges are therefore untraversable, and this states it
// rather than leaving it to be rediscovered: the honest fix is a contract
// rename, not a special case in the derivation.
//
// Asserted against a table whose ONLY organization reference is the role-named
// one, so the derivation cannot pass by finding the properly named column
// beside it. A suffix match on `_org_id`, or any other loosening of "strip
// `_id` and look the name up as a record type", publishes a hop here.
func TestAReferenceNamedForItsRoleYieldsNoHop(t *testing.T) {
	roleNamed := map[string][]StoredColumn{
		"person":       joinSchema["person"],
		"organization": joinSchema["organization"],
		"relationship": columnsOf("id:uuid", "kind", "person_id:uuid", "counterparty_org_id:uuid",
			"archived_at:timestamp with time zone", "ended_at:date"),
		"activity_link": columnsOf("id:uuid", "activity_id:uuid"),
	}
	schema := newSchemaReads(stubColumns{tables: roleNamed})
	relations, err := joinRelations(context.Background(), schema, entityPerson)
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range relations {
		t.Errorf("person → %s was derived from %q, whose stripped name is not a record type",
			relation.Name, relation.Join.To)
	}
}

// The declaration is two table names and the test that holds them against the
// schema. A join table added later and not declared is unaskable in a way
// nothing else would report — which is exactly how the reported defect came to
// exist — so it fails here instead.
func TestEveryJoinTableInTheSchemaIsDeclared(t *testing.T) {
	found := joinTablesInMigrations(t)
	for table, records := range found {
		traversed := slices.ContainsFunc(joinTables, func(j joinTable) bool { return j.table == table })
		reason, excused := notAnEdge[table]
		switch {
		case traversed && excused:
			t.Errorf("%s is both traversed and excused; one table cannot have two verdicts", table)
		case !traversed && !excused:
			t.Errorf("%s carries references to %v and has no verdict: declare it in joinTables if a hop "+
				"should traverse it, or in notAnEdge with the reason it is not one. Left alone it is "+
				"unaskable and nothing reports it — which is how the defect this file fixes came to exist",
				table, records)
		case excused && strings.TrimSpace(reason) == "":
			t.Errorf("%s is excused with no reason, which reads as handled rather than decided", table)
		}
	}
	for _, declared := range joinTables {
		if _, ok := found[declared.table]; !ok {
			t.Errorf("joinTables declares %s, which no core migration creates with two record references",
				declared.table)
		}
		if !joinSchemaHolds(declared.table, declared.hub) {
			t.Errorf("%s declares the hub column %q, which the table does not have — every hop through "+
				"it would silently derive nothing", declared.table, declared.hub)
		}
	}
	for excused := range notAnEdge {
		if _, ok := found[excused]; !ok {
			t.Errorf("notAnEdge excuses %s, which the census does not find — the verdict is about a table "+
				"that either no longer exists or never carried two record references", excused)
		}
	}
}

// Two declared join tables offering the same hop would make which one ran
// depend on the order joinTables happens to be written in. `activity_link` and
// `activity_participant` both reach person from activity, which is exactly the
// collision that would arise the day the second were promoted to an edge.
func TestNoTwoJoinTablesOfferTheSameHop(t *testing.T) {
	for record := range contractRecords {
		seen := map[string]string{}
		for _, relation := range mustJoinRelations(t, record) {
			if first, clash := seen[relation.Name]; clash {
				t.Errorf("%s → %s is offered by both %s and %s; which one runs depends on slice order",
					record, relation.Name, first, relation.Join.Table)
			}
			seen[relation.Name] = relation.Join.Table
		}
	}
}

// joinSchemaHolds reports whether the schema this file is written against has
// the column, so a hub declared for a column nobody added is caught here rather
// than by a hop that quietly never appears.
func joinSchemaHolds(table, column string) bool {
	return slices.ContainsFunc(joinSchema[table], func(c StoredColumn) bool { return c.Name == column })
}

// mustJoinRelations derives one record type's join hops against joinSchema.
func mustJoinRelations(t *testing.T, record string) []Relation {
	t.Helper()
	schema := newSchemaReads(stubColumns{tables: joinSchema})
	relations, err := joinRelations(context.Background(), schema, record)
	if err != nil {
		t.Fatalf("deriving %s join relations: %v", record, err)
	}
	return relations
}

// joinTablesInMigrations reads the core migrations and answers every table
// that ends up carrying `<record type>_id` columns for two or more searchable
// record types — CREATE TABLE and later ALTER TABLE ADD COLUMN alike, since
// activity_link gained its lead arm in 0038 and relationship its project arm
// in 0131. A record's OWN table is not a join table however many references it
// holds; those are the scalar edges the contract already declares.
func joinTablesInMigrations(t *testing.T) map[string][]string {
	t.Helper()
	// BOTH namespaces (ADR-0017). A join table added by a custom migration is a
	// join table, and a census that read only core would hand it the silence
	// this gate exists to refuse.
	var files []string
	for _, namespace := range []string{"core", "custom"} {
		found, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", namespace, "*.up.sql"))
		if err != nil {
			t.Fatalf("listing the %s migrations: %v", namespace, err)
		}
		files = append(files, found...)
	}
	slices.Sort(files)
	create := regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?(\w+)\s*\((.*?)\n\);`)
	// One ALTER TABLE may add several columns, comma-separated across lines —
	// which is the form core 0195 uses to give `attachment` four record
	// references at once. Matching only `ALTER TABLE x ADD COLUMN y uuid` on a
	// single line saw none of them, so the table needed no verdict: a census
	// that misses the repository's own DDL is not a census.
	alter := regexp.MustCompile(`(?is)ALTER TABLE (?:IF EXISTS )?(\w+)\s(.*?);`)
	added := regexp.MustCompile(`(?i)ADD COLUMN (?:IF NOT EXISTS )?(\w+)\s+uuid`)
	// Case-insensitive, like its sibling above: a CREATE TABLE declaring
	// `person_id UUID` is the same column, and a census that reads only one
	// casing is a census with a spelling in it.
	reference := regexp.MustCompile(`(?im)^\s*(\w+_id)\s+uuid`)
	// A line comment may contain a semicolon, and this repository's do — core
	// 0195's explanation of the attachment roll-up says "keeps owning its
	// visibility; this makes". Matching a statement as "up to the next
	// semicolon" over the raw text therefore ENDED that ALTER four columns
	// early and the table went uncounted. Stripping comments first is what
	// makes the statement boundary the statement's.
	comment := regexp.MustCompile(`--[^\n]*`)

	references := map[string]map[string]bool{}
	note := func(table, column string) {
		record, cut := strings.CutSuffix(column, relationSuffix)
		if !cut || contractRecords[record] == nil {
			return
		}
		if references[table] == nil {
			references[table] = map[string]bool{}
		}
		references[table][record] = true
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		body := comment.ReplaceAllString(string(raw), "")
		for _, m := range create.FindAllStringSubmatch(body, -1) {
			for _, column := range reference.FindAllStringSubmatch(m[2], -1) {
				note(m[1], column[1])
			}
		}
		for _, statement := range alter.FindAllStringSubmatch(body, -1) {
			for _, column := range added.FindAllStringSubmatch(statement[2], -1) {
				note(statement[1], column[1])
			}
		}
	}

	joins := map[string][]string{}
	for table, records := range references {
		if len(records) < 2 || tableIsARecord(table) {
			continue
		}
		joins[table] = slices.Sorted(maps.Keys(records))
	}
	if len(joins) == 0 {
		t.Fatal("no join table was found in the core migrations, so this census proves nothing")
	}
	return joins
}

// tableIsARecord reports whether a table is a searchable record's own table,
// which carries scalar references rather than being an edge between two.
func tableIsARecord(table string) bool {
	for record := range contractRecords {
		if stored, ok := tableFor(record); ok && stored == table {
			return true
		}
	}
	return false
}

// The end-to-end proof, and the question the issue was filed for: "people at an
// organization in Stuttgart" is one plan, and before the join derivation it
// could not be written at all.
//
// The hop is a read of the record it lands on, so it carries everything a hop
// has always carried — its own archived_at, its own discovery narrowing, its
// own row scope — and the join table contributes only membership.
func TestAPlanTraversesAJoinEdgeAndTheHopKeepsItsOwnGuards(t *testing.T) {
	sql, args := compilePlanDoc(readerFor(entityPerson, entityOrganization), t, `{
		"version": "v1", "target": "person",
		"traverse": {"relation": "organizations",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	for _, want := range []string{
		`h.id IN (SELECT j."organization_id" FROM "relationship" j WHERE j."person_id" = t.id`,
		// The employment itself must be live: an archived one is a job the
		// person no longer holds, and it must not carry the hop.
		`j.archived_at IS NULL`,
		// Everything a hop already carried, unchanged by the new edge form.
		"JOIN LATERAL", "hop_id", "hop_title", "h.archived_at IS NULL",
		"NOT h.is_anchor", `h."address_city" =`, "ORDER BY h.id LIMIT 1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("statement lacks %q:\n%s", want, sql)
		}
	}
	// The join table is membership only. Selecting from it, or letting the hop
	// predicate bind against it, would make the evidence the hop returns a row
	// of the edge rather than of the organization.
	if strings.Contains(sql, `j."address_city"`) || strings.Contains(sql, "j.id AS hop_id") {
		t.Fatalf("the join table leaked into the hop's own read:\n%s", sql)
	}
	if !slices.Contains(args, any("Stuttgart")) {
		t.Fatalf("args are %v", args)
	}
}

// The other direction and the other table, which is the half nobody had
// noticed was missing: an activity can name the person it is about.
func TestAnActivityTraversesToItsPersonThroughTheLinkTable(t *testing.T) {
	sql, _ := compilePlanDoc(readerFor(entityActivity, entityPerson), t, `{
		"version": "v1", "target": "activity",
		"traverse": {"relation": "persons",
		             "where": [{"field": "full_name", "op": "eq", "value": "Ronny"}]}}`)
	if !strings.Contains(sql, `h.id IN (SELECT j."person_id" FROM "activity_link" j WHERE j."activity_id" = t.id`) {
		t.Fatalf("the activity hop does not traverse activity_link:\n%s", sql)
	}
	// activity_link has no archived_at, and naming one would be a database
	// error on every plan that traversed it — not a narrower answer.
	if strings.Contains(sql, "j.archived_at") {
		t.Fatalf("the hop names an archived_at column activity_link does not have:\n%s", sql)
	}
}

// A refusal answers rather than redirecting: it names the hops this caller
// actually has, so the vocabulary is discoverable without a second round trip
// and without guessing. The list is the caller's own resolved one, so it says
// the same thing whether the relation never existed or lands somewhere they
// cannot read.
func TestAnUnknownRelationIsRefusedByNameAndNamesTheOnesThatExist(t *testing.T) {
	validator := NewPlanValidator(NewVocabularyResolver().WithColumnReader(stubColumns{tables: joinSchema}))
	decoded, err := DecodePlan([]byte(`{"version": "v1", "target": "person",
		"traverse": {"relation": "employment"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = validator.Validate(readerFor(entityPerson, entityOrganization), decoded)
	refusal := &PlanRefusal{}
	if !errors.As(err, &refusal) {
		t.Fatalf("a plan naming a relationship kind was not refused: %v", err)
	}
	if len(refusal.Refusals) != 1 || refusal.Refusals[0].Code != CodeUnknownRelation {
		t.Fatalf("refusal is %+v", refusal.Refusals)
	}
	message := refusal.Refusals[0].Message
	// The name it refused, so a caller knows which of several went wrong...
	if !strings.Contains(message, `"employment"`) {
		t.Errorf("the refusal does not name what it refused: %s", message)
	}
	// ...and the hop that answers the question they were asking.
	if !strings.Contains(message, `"organizations"`) {
		t.Errorf("the refusal does not name the hop that exists: %s", message)
	}
}

// The refusal's list is bounded, and the bound is what says so. A hop count is
// a product of the record types and join tables a schema declares — not a
// number this refusal controls — and a refusal that grows with the schema is
// the defect #1787 describes, where the sentence saying what to fix is the part
// that gets cut. A truncated list that looked complete would be worse than a
// long one: a caller would read "these are my hops" and stop.
func TestTheRefusalsListOfHopsIsBoundedAndSaysWhenItTruncates(t *testing.T) {
	many := TargetVocabulary{Target: entityPerson}
	for i := range 20 {
		many.Relations = append(many.Relations, Relation{Name: fmt.Sprintf("hop%02d", i)})
	}
	message := unknownRelation(many, "employment")[0].Message
	if !strings.Contains(message, "and 8 more") {
		t.Errorf("a 20-hop vocabulary was rendered without saying what was cut: %s", message)
	}
	if strings.Contains(message, `"hop12"`) {
		t.Errorf("the list ran past its bound: %s", message)
	}
	// A record type with no hops at all says so, rather than trailing off after
	// "it has" — which reads as a rendering fault and not as an answer.
	none := unknownRelation(TargetVocabulary{Target: entityLead}, "organizations")[0].Message
	if !strings.Contains(none, "it has none") {
		t.Errorf("a target with no hops rendered as %q", none)
	}
}

// A derivation asked about something that is not a searchable record answers a
// wiring fault rather than an empty list. An empty list is a real state — a
// record type no join table reaches — and the two must not look alike.
func TestJoinRelationsRefusesARecordTypeThisModuleDoesNotSearch(t *testing.T) {
	schema := newSchemaReads(stubColumns{tables: joinSchema})
	if _, err := joinRelations(context.Background(), schema, "workspace"); err == nil {
		t.Fatal("a record type this module does not search derived relations instead of failing")
	}
	// The honest empty: lead is searchable and is an arm of activity_link, so
	// it has a hop; project is an arm of relationship only. Neither is empty
	// today, so the state is asserted where it exists — a table this record has
	// no column in contributes nothing rather than erroring.
	relations, err := joinRelations(context.Background(), schema, entityLead)
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range relations {
		if relation.Join.Table == "relationship" {
			t.Errorf("lead derived a hop through relationship, which has no lead_id column: %+v", relation)
		}
	}
}
