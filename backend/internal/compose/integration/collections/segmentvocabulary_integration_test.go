// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package collections

// The three guarantees the unit lane cannot reach, proved against real
// Postgres: a workspace's own custom field really filters a dynamic list
// (a customer who fills in a field can build a target list from it — the
// whole point of letting them add one), retiring that field's catalog
// status leaves an already-saved segment evaluable rather than silently
// widening it, and a filtered export of a segment carries exactly the rows
// membership computed for it — one predicate engine, not two that could
// drift apart. Two more scenarios pin the tag vocabulary against the
// database's own CHECK constraint and the polymorphic join it drives.

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	collectionsmod "github.com/gradionhq/margince/backend/internal/modules/collections"
	customfieldsmod "github.com/gradionhq/margince/backend/internal/modules/customfields"
	dealsmod "github.com/gradionhq/margince/backend/internal/modules/deals"
	peoplemod "github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fullGrant is unbounded object authority — every suite below acts as one
// principal holding exactly the objects this vocabulary spans, so a
// refusal in a test is never mistaken for an RBAC gap.
var fullGrant = principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}

// testPerms is this suite's one actor: unbounded row scope, and every
// object the segment vocabulary touches — custom_field to define columns,
// the four record types a segment can filter, list to build one, and tag
// to mint and apply the polymorphic join's own vocabulary.
var testPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": fullGrant,
		"person":       fullGrant,
		"organization": fullGrant,
		"deal":         fullGrant,
		"lead":         fullGrant,
		"project":      fullGrant,
		"list":         fullGrant,
		"tag":          fullGrant,
	},
	RowScope: principal.RowScopeAll,
}

// fixture bundles one migrated environment with the store wiring compose
// itself uses (serverassembly.go): the collections store and the people
// store both widened by the SAME customfields service, so a test that
// writes a value through one and filters through the other is exercising
// the real cross-module seam, not a hand-built stand-in for it.
type fixture struct {
	e        *integration.Env
	ctx      context.Context
	svc      *customfieldsmod.Service
	people   *peoplemod.Store
	projects *dealsmod.Store
	lists    *collectionsmod.Store
}

func setupFixture(t *testing.T) fixture {
	t.Helper()
	e := integration.Setup(t)
	svc := customfieldsmod.NewService(e.Pool, integration.SchemaPool(t))
	return fixture{
		e: e,
		// A real seeded user, not a synthetic id: custom_field.created_by is
		// foreign-keyed to app_user. The harness seeds Rep1/2/3 AND AdminUser;
		// only the three reps carry a team membership, which is the gap
		// TestARecordWhoseOwnerIsInNoTeamIsCoveredByNoTeam owns a record through.
		ctx:      e.As(e.Rep1, nil, testPerms),
		svc:      svc,
		people:   peoplemod.NewStore(e.DB()).WithFieldCatalog(svc),
		projects: dealsmod.NewStore(e.DB(), installseam.Deals()).WithFieldCatalog(svc),
		lists:    collectionsmod.NewStore(e.DB()).WithFieldCatalog(svc),
	}
}

// createTextField defines an active text field on person and answers its
// physical column and catalog id. label must be unique across this
// package's own tests: testdb.Reset truncates the custom_field catalog
// between tests but never reverts the ALTER TABLE a create ran, so a
// reused label collides with the physical column an earlier test left
// behind in this same schema.
func (f fixture) createTextField(t *testing.T, label string) (column string, id ids.UUID) {
	t.Helper()
	field, err := f.svc.Create(f.ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: label, Type: customfieldsmod.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining %q: %v", label, err)
	}
	if field.ColumnName == nil {
		t.Fatalf("defined field %q carries no column_name", label)
	}
	return *field.ColumnName, ids.UUID(field.Id)
}

// assertSoleMember reads a list's live membership and fails unless it is
// exactly the one record named.
func assertSoleMember(t *testing.T, f fixture, listID ids.ListID, want ids.UUID) {
	t.Helper()
	rows, _, err := f.lists.ListMembers(f.ctx, listID, 50, "")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != want {
		t.Fatalf("members = %v, want exactly [%s]", rows, want)
	}
}

// defineField creates a custom field of any of the six catalog types on
// person and answers its physical column — createTextField's generalized
// sibling, used by the per-type coverage below. label must stay unique
// across this package (see createTextField's own note on testdb.Reset).
func (f fixture) defineField(t *testing.T, spec customfieldsmod.FieldSpec) string {
	t.Helper()
	spec.Object = "person"
	spec.Source = "ui"
	field, err := f.svc.Create(f.ctx, spec)
	if err != nil {
		t.Fatalf("defining %q: %v", spec.Label, err)
	}
	if field.ColumnName == nil {
		t.Fatalf("defined field %q carries no column_name", spec.Label)
	}
	return *field.ColumnName
}

// seedTwoPeople creates two bare person records for a typed-filter test to
// set a custom field value on afterward.
func (f fixture) seedTwoPeople(t *testing.T, name string) (a, b ids.UUID) {
	t.Helper()
	pa, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: name + " A", Source: "manual"})
	if err != nil {
		t.Fatalf("create %s A: %v", name, err)
	}
	pb, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: name + " B", Source: "manual"})
	if err != nil {
		t.Fatalf("create %s B: %v", name, err)
	}
	return ids.UUID(pa.Id), ids.UUID(pb.Id)
}

// setField sets one custom column's value through the update path — the
// same "filled in later, not at create" path every scenario in this file
// filters against.
//
//craft:ignore naked-any value is a wire-shaped custom-field value, spanning every scalar type a cf_* column can hold
func (f fixture) setField(t *testing.T, person ids.UUID, column string, value any) {
	t.Helper()
	if _, err := f.people.UpdatePerson(f.ctx, integration.PersonIDOf(person), peoplemod.UpdatePersonInput{
		CustomFields: map[string]any{column: value},
	}); err != nil {
		t.Fatalf("setting %s=%v: %v", column, value, err)
	}
}

// filterList builds a one-leaf dynamic list on person and answers its id,
// failing the test if the definition is refused.
//
//craft:ignore naked-any value is a predicate leaf's operand, which spans every scalar and array shape the filter DSL accepts
func (f fixture) filterList(t *testing.T, name, field, op string, value any) ids.ListID {
	t.Helper()
	created, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: name, EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": field, "op": op, "value": value},
	})
	if err != nil {
		t.Fatalf("create list %q: %v", name, err)
	}
	return created.ID
}

// TestANumberCustomFieldFiltersOnEqAndGt exercises the typed operators the
// text-only fixture above never reaches: eq narrows to an exact match and
// gt narrows to values strictly above the bound, both against a real
// numeric cf_* column.
func TestANumberCustomFieldFiltersOnEqAndGt(t *testing.T) {
	f := setupFixture(t)
	column := f.defineField(t, customfieldsmod.FieldSpec{Label: "Deal Score", Type: customfieldsmod.TypeNumber})
	high, low := f.seedTwoPeople(t, "Score")
	f.setField(t, high, column, 42.5)
	f.setField(t, low, column, 10.0)

	assertSoleMember(t, f, f.filterList(t, "score eq", column, "eq", 42.5), high)
	assertSoleMember(t, f, f.filterList(t, "score gt", column, "gt", 20.0), high)
}

// TestACurrencyCustomFieldFiltersOnEqAndRefusesAFractionalOperand proves
// both halves of the currency fix: a whole minor-units value narrows
// membership through eq, and a fractional operand is refused as a typed
// validation error rather than silently truncated or reaching the query
// as a query-execution failure.
func TestACurrencyCustomFieldFiltersOnEqAndRefusesAFractionalOperand(t *testing.T) {
	f := setupFixture(t)
	column := f.defineField(t, customfieldsmod.FieldSpec{
		Label: "Lifetime Value", Type: customfieldsmod.TypeCurrency, Currency: strPtr("USD"),
	})
	big, small := f.seedTwoPeople(t, "LTV")
	f.setField(t, big, column, float64(500000))
	f.setField(t, small, column, float64(100))

	assertSoleMember(t, f, f.filterList(t, "ltv eq", column, "eq", float64(500000)), big)

	_, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "ltv fractional", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": 12.5},
	})
	var pred *storekit.PredicateError
	if !errors.As(err, &pred) || pred.Code != storekit.CodeFilterValueInvalid {
		t.Fatalf("a fractional currency operand err = %v, want a PredicateError(%s)", err, storekit.CodeFilterValueInvalid)
	}
}

// TestADateCustomFieldFiltersOnGt proves a date column orders correctly
// through the predicate engine's own gt, not just equality.
func TestADateCustomFieldFiltersOnGt(t *testing.T) {
	f := setupFixture(t)
	column := f.defineField(t, customfieldsmod.FieldSpec{Label: "Renewal Date", Type: customfieldsmod.TypeDate})
	later, earlier := f.seedTwoPeople(t, "Renewal")
	f.setField(t, later, column, "2027-06-01")
	f.setField(t, earlier, column, "2026-01-01")

	assertSoleMember(t, f, f.filterList(t, "renewal gt", column, "gt", "2026-12-31"), later)
}

// TestABooleanCustomFieldFiltersOnEq proves a boolean column's eq narrows
// to exactly the true-valued (or false-valued) rows.
func TestABooleanCustomFieldFiltersOnEq(t *testing.T) {
	f := setupFixture(t)
	column := f.defineField(t, customfieldsmod.FieldSpec{Label: "Is VIP", Type: customfieldsmod.TypeBoolean})
	vip, plain := f.seedTwoPeople(t, "VIP")
	f.setField(t, vip, column, true)
	f.setField(t, plain, column, false)

	assertSoleMember(t, f, f.filterList(t, "vip eq true", column, "eq", true), vip)
}

// TestAPicklistCustomFieldFiltersOnIn proves a picklist column's in
// membership narrows to rows whose value is any of the listed options.
func TestAPicklistCustomFieldFiltersOnIn(t *testing.T) {
	f := setupFixture(t)
	column := f.defineField(t, customfieldsmod.FieldSpec{
		Label: "Region", Type: customfieldsmod.TypePicklist, Options: []string{"emea", "apac", "amer"},
	})
	emea, amer := f.seedTwoPeople(t, "Region")
	f.setField(t, emea, column, "emea")
	f.setField(t, amer, column, "amer")

	assertSoleMember(t, f, f.filterList(t, "region in", column, "in", []any{"emea"}), emea)
}

// TestADynamicListFiltersOnACustomFieldValue is the point of the task: a
// cf_* predicate must be ACCEPTED by list creation and then actually
// narrow membership — a customer who filled in a field can build a
// target list from it, which is the entire reason to let them add one.
func TestADynamicListFiltersOnACustomFieldValue(t *testing.T) {
	f := setupFixture(t)
	column, _ := f.createTextField(t, "Loyalty Tier")

	matching, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Match", Source: "manual"})
	if err != nil {
		t.Fatalf("create matching person: %v", err)
	}
	if _, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Other", Source: "manual"}); err != nil {
		t.Fatalf("create non-matching person: %v", err)
	}
	// Set through the update path, not at create: a field a customer fills
	// in later must filter exactly as one set at creation would.
	if _, err := f.people.UpdatePerson(f.ctx, integration.PersonIDOf(ids.UUID(matching.Id)), peoplemod.UpdatePersonInput{
		CustomFields: map[string]any{column: "gold"},
	}); err != nil {
		t.Fatalf("setting the custom field through the update path: %v", err)
	}

	// The 422→201 flip: this call answers 422 (a *storekit.PredicateError)
	// if the catalogue is not wired into this store, exactly the defect an
	// earlier fix closed in only one of the two collections stores.
	created, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "Gold tier", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "gold"},
	})
	if err != nil {
		t.Fatalf("a dynamic list on a custom field was refused: %v", err)
	}
	assertSoleMember(t, f, created.ID, ids.UUID(matching.Id))
}

// TestAProjectCustomFieldIsFilterable proves the segment vocabulary widens
// from the catalogue for "project" exactly as it does for the other four
// resources: project is a customfields.FieldObjects member with real cf_*
// columns (project.go's own insert/update paths carry them), so refusing to
// consult the catalogue for it would leave a customer's project field
// definable and unfilterable at once.
func TestAProjectCustomFieldIsFilterable(t *testing.T) {
	f := setupFixture(t)
	field, err := f.svc.Create(f.ctx, customfieldsmod.FieldSpec{
		Object: "project", Label: "Engagement Model", Type: customfieldsmod.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the project field: %v", err)
	}
	if field.ColumnName == nil {
		t.Fatal("defined field carries no column_name")
	}
	column := *field.ColumnName

	org, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{DisplayName: "Baer Pharma", Source: "manual"})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	matching, err := f.projects.CreateProject(f.ctx, dealsmod.CreateProjectInput{
		Name: "Match", OrganizationID: orgID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create matching project: %v", err)
	}
	if _, err := f.projects.CreateProject(f.ctx, dealsmod.CreateProjectInput{
		Name: "Other", OrganizationID: orgID, Source: "manual",
	}); err != nil {
		t.Fatalf("create non-matching project: %v", err)
	}
	// Set through the update path, not at create: a field a customer fills
	// in later must filter exactly as one set at creation would.
	if _, err := f.projects.UpdateProject(f.ctx, ids.From[ids.ProjectKind](ids.UUID(matching.Id)), dealsmod.UpdateProjectInput{
		CustomFields: map[string]any{column: "retainer"},
	}); err != nil {
		t.Fatalf("setting the custom field through the update path: %v", err)
	}

	created, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "Retainer projects", EntityType: "project", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "retainer"},
	})
	if err != nil {
		t.Fatalf("a dynamic list on a project custom field was refused: %v", err)
	}
	assertSoleMember(t, f, created.ID, ids.UUID(matching.Id))
}

// TestRetiringACustomFieldLeavesItsSegmentEvaluable proves retirement is a
// status flip, never a column drop: a segment saved against a field that
// is later retired must keep returning the same rows. Dropping the clause
// instead would silently WIDEN the target list — the way someone ends up
// emailing people they never meant to target.
func TestRetiringACustomFieldLeavesItsSegmentEvaluable(t *testing.T) {
	f := setupFixture(t)
	column, fieldID := f.createTextField(t, "Renewal Segment")

	matching, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Match", Source: "manual"})
	if err != nil {
		t.Fatalf("create matching person: %v", err)
	}
	if _, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Other", Source: "manual"}); err != nil {
		t.Fatalf("create non-matching person: %v", err)
	}
	if _, err := f.people.UpdatePerson(f.ctx, integration.PersonIDOf(ids.UUID(matching.Id)), peoplemod.UpdatePersonInput{
		CustomFields: map[string]any{column: "renew"},
	}); err != nil {
		t.Fatalf("setting the custom field: %v", err)
	}

	created, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "Renewals", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "renew"},
	})
	if err != nil {
		t.Fatalf("create dynamic list: %v", err)
	}
	assertSoleMember(t, f, created.ID, ids.UUID(matching.Id))

	if _, err := f.svc.Retire(f.ctx, fieldID); err != nil {
		t.Fatalf("retiring the field: %v", err)
	}

	// Same list, re-read after retirement: still exactly the one row.
	assertSoleMember(t, f, created.ID, ids.UUID(matching.Id))

	// A NEW list on the same, now-retired field must still validate — a
	// saved segment naming a retired column is not a mistake to refuse.
	if _, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "Renewals, take two", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "renew"},
	}); err != nil {
		t.Fatalf("a new list on a retired field's column was refused: %v", err)
	}
}

// TestFilteredExportOfASegmentMatchesItsMembership pins the ONE engine
// (LVS-AC-2): a filtered export of a definition carries exactly the rows
// the members endpoint computed from that same definition, in the same
// order. A second vocabulary lookup anywhere in the export path would
// show up here as an export that disagrees with the list it exports.
func TestFilteredExportOfASegmentMatchesItsMembership(t *testing.T) {
	f := setupFixture(t)
	column, _ := f.createTextField(t, "Export Match Flag")

	seed := func(name, value string) ids.UUID {
		p, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: name, Source: "manual"})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := f.people.UpdatePerson(f.ctx, integration.PersonIDOf(ids.UUID(p.Id)), peoplemod.UpdatePersonInput{
			CustomFields: map[string]any{column: value},
		}); err != nil {
			t.Fatalf("set %s's field: %v", name, err)
		}
		return ids.UUID(p.Id)
	}
	a, b := seed("A", "blue"), seed("B", "blue")
	seed("C", "red")

	created, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "Blues", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "blue"},
	})
	if err != nil {
		t.Fatalf("create dynamic list: %v", err)
	}
	rows, _, err := f.lists.ListMembers(f.ctx, created.ID, 50, "")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	members := map[ids.UUID]bool{}
	for _, m := range rows {
		members[m.EntityID] = true
	}
	if len(rows) != 2 || !members[a] || !members[b] {
		t.Fatalf("membership = %v, want exactly [%s %s]", rows, a, b)
	}

	engine, ok, err := f.lists.SegmentEngine(f.ctx, "person")
	if err != nil || !ok {
		t.Fatalf("resolve person engine: ok=%v err=%v", ok, err)
	}
	result, err := compose.NewFilteredExportWriter(f.e.Pool).WriteFiltered(f.ctx, engine,
		storekit.Predicate{Field: column, Op: "eq", Value: "blue"}, "csv")
	if err != nil {
		t.Fatalf("filtered export: %v", err)
	}

	exported := integration.CSVColumn(t, result.Body, "id")
	if len(exported) != len(rows) {
		t.Fatalf("export carries %d rows, membership carries %d (%v vs %v)", len(exported), len(rows), exported, rows)
	}
	for i, m := range rows {
		if exported[i] != m.EntityID.String() {
			t.Fatalf("export row %d = %s, membership row %d = %s — the two vocabulary lookups disagreed",
				i, exported[i], i, m.EntityID)
		}
	}
}

// checkLiteralRe pulls a quoted literal out of a rendered Postgres CHECK
// constraint (pg_get_constraintdef quotes each value, whether it renders
// as `IN (...)` or the normalized `= ANY (ARRAY[...])`); a double-quoted
// column or type name never matches, only the single-quoted values do.
var checkLiteralRe = regexp.MustCompile(`'([a-z_]+)'`)

// entityTypeCheckValues reads one table's own entity_type CHECK and answers the
// values it admits. Per table, because each polymorphic table carries its own
// constraint and they are not required to agree — assuming they do is the
// mistake this file exists to catch.
func entityTypeCheckValues(t *testing.T, f fixture, table string) map[string]bool {
	t.Helper()
	var def string
	err := database.WithWorkspaceTx(f.ctx, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			 WHERE conrelid = $1::regclass AND contype = 'c'
			   AND pg_get_constraintdef(oid) LIKE '%entity_type%'`, table).Scan(&def)
	})
	if err != nil {
		t.Fatalf("reading %s's own entity_type CHECK: %v", table, err)
	}
	out := map[string]bool{}
	for _, m := range checkLiteralRe.FindAllStringSubmatch(def, -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s's CHECK yielded no values from %q", table, def)
	}
	return out
}

// TestEveryEnumOverTheRecordVocabularyMatchesTheCheckConstraint proves the
// Go-side taggable set is not just consistent with itself (the unit lane's job)
// but COMPLETE against the schema's own CHECK (LVS-DDL-2) — the authority
// every other spelling answers to. The CHECK admits five values: person,
// organization, deal, lead and project (0131_project.up.sql's
// taggable_entity_type_check).
//
// The CONTRACT's enum is compared to the same set, because three vocabularies
// describe one thing here — the CHECK, the generated enum, and the record-type
// set the store gates on — and they disagreed once: the enum omitted project
// while the server accepted, stored and returned project tags, so a client
// switching exhaustively on that enum had no branch for a value it was handed.
func TestEveryEnumOverTheRecordVocabularyMatchesTheCheckConstraint(t *testing.T) {
	f := setupFixture(t)

	var def string
	err := database.WithWorkspaceTx(f.ctx, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			 WHERE conrelid = 'taggable'::regclass AND contype = 'c'
			   AND pg_get_constraintdef(oid) LIKE '%entity_type%'`).Scan(&def)
	})
	if err != nil {
		t.Fatalf("reading taggable's own CHECK constraint: %v", err)
	}
	got := map[string]bool{}
	for _, m := range checkLiteralRe.FindAllStringSubmatch(def, -1) {
		got[m[1]] = true
	}
	want := map[string]bool{"person": true, "organization": true, "deal": true, "lead": true, "project": true}
	if !maps.Equal(got, want) {
		t.Fatalf("taggable's CHECK admits %v, want %v", got, want)
	}

	// Every generated enum over the SAME vocabulary, asked about the CHECK's own
	// values rather than compared to a list restated here. A restated list is
	// the thing this test exists to catch: it cannot fail for an enum that GAINS
	// a member, and an enum that LOSES one takes the constant with it, so the
	// file stops compiling and the legible failure never runs.
	//
	// Fixing one spelling and leaving its siblings is how this diverged in the
	// first place: migration 0131 widened four CHECKs to five and the contract
	// caught up on one, so the API accepted, stored and returned a project list,
	// list member and tag while three enums said it could not exist. The three
	// RESPONSE shapes cast back unchecked at their wire edge (wireTaggable,
	// wireList, wireMember), which is how a value the CHECK admits reaches a
	// client the enum never told about it; ApplyTagRequest is the request half
	// of the same vocabulary, where the cost is a value refused instead.
	for admitted := range got {
		for name, valid := range map[string]bool{
			"Taggable.entity_type":        crmcontracts.TaggableEntityType(admitted).Valid(),
			"ApplyTagRequest.entity_type": crmcontracts.ApplyTagRequestEntityType(admitted).Valid(),
		} {
			if !valid {
				t.Errorf("%s refuses %q, which taggable's CHECK admits — the database stores it and the wire cannot name it", name, admitted)
			}
		}
	}

	// The SIBLING vocabularies, each against ITS OWN table. Comparing them all
	// to taggable's CHECK would pass only because the four happen to admit the
	// same five today, and that coincidence is where this class of bug lives:
	// 0131 widened four CHECKs and the contract tracked one.
	for _, c := range []struct {
		table string
		enums map[string]func(string) bool
	}{
		{
			table: "list",
			enums: map[string]func(string) bool{
				"List.entity_type":              func(v string) bool { return crmcontracts.ListEntityType(v).Valid() },
				"CreateListRequest.entity_type": func(v string) bool { return crmcontracts.CreateListRequestEntityType(v).Valid() },
			},
		},
		{
			table: "list_member",
			enums: map[string]func(string) bool{
				"ListMember.entity_type":           func(v string) bool { return crmcontracts.ListMemberEntityType(v).Valid() },
				"AddListMemberRequest.entity_type": func(v string) bool { return crmcontracts.AddListMemberRequestEntityType(v).Valid() },
			},
		},
		{
			table: "activity_link",
			enums: map[string]func(string) bool{
				"ActivityLink.entity_type":                func(v string) bool { return crmcontracts.ActivityLinkEntityType(v).Valid() },
				"CreateActivityRequest.links.entity_type": func(v string) bool { return crmcontracts.CreateActivityRequestLinksEntityType(v).Valid() },
				"bookMeeting.links.entity_type":           func(v string) bool { return crmcontracts.BookMeetingJSONBodyLinksEntityType(v).Valid() },
			},
		},
	} {
		t.Run(c.table, func(t *testing.T) {
			for admitted := range entityTypeCheckValues(t, f, c.table) {
				for name, valid := range c.enums {
					if !valid(admitted) {
						t.Errorf("%s refuses %q, which %s's CHECK admits", name, admitted, c.table)
					}
				}
			}
		})
	}

	// The export object answers to segmentEngines rather than a CHECK — it names
	// a filterable resource, not a stored column — and collections exports no
	// accessor for that map, so the filter vocabulary is the observable proxy:
	// every record type a segment can filter must be nameable as an export
	// object, or a segment exists that no export of it can address.
	for entity := range want {
		if !crmcontracts.FilteredExportRequestObject(entity).Valid() {
			t.Errorf("FilteredExportRequest.object refuses %q, which carries a segment engine", entity)
		}
	}

	// collections exports no accessor for its private taggable set, so
	// validation is the observable proxy: every type the CHECK admits must
	// let a dynamic list filter by tag, or a record type nobody can see
	// would ship with a filter that silently never matches anything.
	for entity := range want {
		if _, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
			Name: "tag probe " + entity, EntityType: entity, ListType: "dynamic",
			Definition: map[string]any{"field": "tag", "op": "exists", "value": false},
		}); err != nil {
			t.Errorf("%s: a tag filter did not validate: %v", entity, err)
		}
	}
}

func strPtr(s string) *string { return &s }

// seedTaggablePair creates two records of one entity type and answers
// their ids untyped — every caller widens through the same ApplyTag /
// CreateList surface, which takes entity ids untyped for exactly this
// polymorphic reason.
func (f fixture) seedTaggablePair(t *testing.T, entity string, pipeline ids.PipelineID, stage ids.StageID) (tagged, plain ids.UUID) {
	t.Helper()
	switch entity {
	case "person":
		a, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Tagged Person", Source: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.people.CreatePerson(f.ctx, peoplemod.CreatePersonInput{FullName: "Plain Person", Source: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		return ids.UUID(a.Id), ids.UUID(b.Id)
	case "organization":
		a, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{DisplayName: "Tagged Org"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{DisplayName: "Plain Org"})
		if err != nil {
			t.Fatal(err)
		}
		return ids.UUID(a.Id), ids.UUID(b.Id)
	case "deal":
		return f.e.SeedDeal(t, "Tagged Deal", pipeline, stage, nil), f.e.SeedDeal(t, "Plain Deal", pipeline, stage, nil)
	default: // lead
		a, _, err := f.people.CreateLead(f.ctx, peoplemod.CreateLeadInput{FullName: strPtr("Tagged Lead"), Source: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := f.people.CreateLead(f.ctx, peoplemod.CreateLeadInput{FullName: strPtr("Plain Lead"), Source: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		return ids.UUID(a.Id), ids.UUID(b.Id)
	}
}

// assertTagSegment proves the tag leaf both ways for one entity type: an
// `eq` on the tag's id selects exactly the tagged record, and `exists:
// false` selects exactly the untagged one — the join reaches the SAME
// taggable row from either direction.
func (f fixture) assertTagSegment(t *testing.T, entity, tagID string, tagged, plain ids.UUID) {
	t.Helper()
	has, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: entity + " has the tag", EntityType: entity, ListType: "dynamic",
		Definition: map[string]any{"field": "tag", "op": "eq", "value": tagID},
	})
	if err != nil {
		t.Fatalf("%s: create tagged-eq list: %v", entity, err)
	}
	assertSoleMember(t, f, has.ID, tagged)

	untagged, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: entity + " has no tag", EntityType: entity, ListType: "dynamic",
		Definition: map[string]any{"field": "tag", "op": "exists", "value": false},
	})
	if err != nil {
		t.Fatalf("%s: create untagged list: %v", entity, err)
	}
	assertSoleMember(t, f, untagged.ID, plain)
}

// TestATagFilterSelectsTaggedRecordsPerEntityType proves the tag leaf
// reaches the polymorphic taggable join for every entity type that can
// carry one — person, organization, deal and lead — not just the one the
// unit lane happened to exercise.
func TestATagFilterSelectsTaggedRecordsPerEntityType(t *testing.T) {
	f := setupFixture(t)
	tag, err := f.lists.CreateTag(f.ctx, "vip", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	pipeline, open, _ := integration.DealFixture(t, f.e)

	for _, entity := range []string{"person", "organization", "deal", "lead"} {
		tagged, plain := f.seedTaggablePair(t, entity, pipeline, open)
		if _, err := f.lists.ApplyTag(f.ctx, tag.ID, entity, tagged); err != nil {
			t.Fatalf("%s: apply tag: %v", entity, err)
		}
		f.assertTagSegment(t, entity, tag.ID.String(), tagged, plain)
	}
}

// TestACatalogueReadFailureIsNeverMisreportedAsAFilterMistake proves the
// store-level precondition the handler's error routing depends on: a
// failed catalogue read never surfaces as a *storekit.PredicateError,
// which is the one shape the transport maps to 422-blame-the-caller's-
// filter. It does not drive an HTTP call or observe the status code a
// client would see — that a canceled context reaching SegmentEngine
// answers 500 rather than 422 is the handler's own doing, resting on
// this invariant rather than proving it directly. A context already
// canceled before the engine reaches for the workspace's cf_* columns is
// a genuine failure over the real service and the real pool — no
// hand-built adapter, no simulated error — that has nothing at all to do
// with what the caller's filter named.
func TestACatalogueReadFailureIsNeverMisreportedAsAFilterMistake(t *testing.T) {
	f := setupFixture(t)
	dead, cancel := context.WithCancel(f.ctx)
	cancel()

	_, _, err := f.lists.SegmentEngine(dead, "person")
	if err == nil {
		t.Fatal("a canceled catalogue read returned no error")
	}
	var pred *storekit.PredicateError
	if errors.As(err, &pred) {
		t.Fatalf("a catalogue failure was dressed up as a filter validation error: %v", pred)
	}
}

// memberIDs reads a list's live membership as a set, for the scenarios whose
// answer is more than one record. assertSoleMember above covers the single-row
// case and says so more sharply; this is its plural sibling, not a replacement.
func memberIDs(t *testing.T, f fixture, listID ids.ListID) map[ids.UUID]bool {
	t.Helper()
	rows, _, err := f.lists.ListMembers(f.ctx, listID, 50, "")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	got := make(map[ids.UUID]bool, len(rows))
	for _, row := range rows {
		got[row.EntityID] = true
	}
	return got
}

// dealForCustomer seeds one deal against one organization through the real
// writer, so the organization_id the filter joins on is the one CreateDeal
// itself stamps.
func (f fixture) dealForCustomer(
	t *testing.T, name string, pipeline ids.PipelineID, stage ids.StageID, org *ids.OrganizationID,
) ids.UUID {
	t.Helper()
	deal, err := f.e.Deals.CreateDeal(f.ctx, dealsmod.CreateDealInput{
		Name: name, PipelineID: pipeline, StageID: stage, OrganizationID: org, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create deal %q: %v", name, err)
	}
	return ids.UUID(deal.Id)
}

// customerOrg seeds one organization with (or without) an industry.
func (f fixture) customerOrg(t *testing.T, name string, industry *string) ids.OrganizationID {
	t.Helper()
	org, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{
		DisplayName: name, Industry: industry, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create organization %q: %v", name, err)
	}
	return ids.From[ids.OrganizationKind](ids.UUID(org.Id))
}

// "Show me the pipeline for manufacturing" — a deal filtered by an attribute of
// the company it belongs to, which is a correlated join and therefore something
// only the real database can prove. The unit lane can check the SQL text; only
// this can check which deals come back.
func TestADealFilterReachesTheCustomersIndustry(t *testing.T) {
	f := setupFixture(t)
	pipeline, open, _ := integration.DealFixture(t, f.e)
	manufacturing, services := "manufacturing", "services"

	inManufacturing := f.dealForCustomer(t, "Factory renewal", pipeline, open,
		orgPtr(f.customerOrg(t, "Vulcan Works", &manufacturing)))
	f.dealForCustomer(t, "Agency retainer", pipeline, open,
		orgPtr(f.customerOrg(t, "Bright Consulting", &services)))

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "manufacturing pipeline", EntityType: "deal", ListType: "dynamic",
		Definition: map[string]any{
			"field": "organization_industry", "op": "eq", "value": manufacturing,
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	assertSoleMember(t, f, list.ID, inManufacturing)
}

// `exists: false` on a linked field asks about the COLUMN, and the answer spans
// both ways a deal can fail to have a known industry: a customer with none, and
// no customer at all. Pinned because the row reading — "does a linked
// organization exist" — is the plausible wrong one, and it would silently drop
// every deal whose company simply has no industry recorded.
func TestAnUnknownCustomerIndustryCoversBothWaysItCanBeUnknown(t *testing.T) {
	f := setupFixture(t)
	pipeline, open, _ := integration.DealFixture(t, f.e)
	known := "manufacturing"

	customerWithNoIndustry := f.dealForCustomer(t, "Unclassified account", pipeline, open,
		orgPtr(f.customerOrg(t, "Quiet Holdings", nil)))
	noCustomerAtAll := f.dealForCustomer(t, "Inbound, unattributed", pipeline, open, nil)
	classified := f.dealForCustomer(t, "Factory renewal", pipeline, open,
		orgPtr(f.customerOrg(t, "Vulcan Works", &known)))

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "customer industry unknown", EntityType: "deal", ListType: "dynamic",
		Definition: map[string]any{
			"field": "organization_industry", "op": "exists", "value": false,
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	got := memberIDs(t, f, list.ID)
	if !got[customerWithNoIndustry] {
		t.Error("a deal whose customer has no industry is not counted as unknown")
	}
	if !got[noCustomerAtAll] {
		t.Error("a deal with no customer at all is not counted as unknown")
	}
	if got[classified] {
		t.Error("a deal whose customer HAS an industry is reported as unknown")
	}
}

// Archiving the customer does not move the deal out of the filter.
//
// The organization's own segment engine excludes archived and anchor rows,
// because those answer "which of our accounts are segment MEMBERS". This leaf
// answers a fact about the company a deal belongs to, and archiving does not
// change that fact — so the exclusion is deliberately NOT carried over. Without
// this test that decision is a comment; with it, re-adding the base clause fails
// here instead of quietly moving somebody's pipeline figure.
func TestArchivingTheCustomerLeavesItsDealsInTheIndustryFilter(t *testing.T) {
	f := setupFixture(t)
	pipeline, open, _ := integration.DealFixture(t, f.e)
	manufacturing := "manufacturing"
	org := f.customerOrg(t, "Vulcan Works", &manufacturing)
	deal := f.dealForCustomer(t, "Factory renewal", pipeline, open, orgPtr(org))

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "manufacturing pipeline, archived customer", EntityType: "deal", ListType: "dynamic",
		Definition: map[string]any{
			"field": "organization_industry", "op": "eq", "value": manufacturing,
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	assertSoleMember(t, f, list.ID, deal)

	if _, err := f.people.ArchiveOrganization(f.ctx, org); err != nil {
		t.Fatalf("archive organization: %v", err)
	}
	assertSoleMember(t, f, list.ID, deal)
}

func orgPtr(id ids.OrganizationID) *ids.OrganizationID { return &id }
