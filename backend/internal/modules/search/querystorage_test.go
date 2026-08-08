// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// stubColumns is the ColumnReader seam, so a test can say what a table holds
// without a database. The seam is the boundary; mocking it is what mocking a
// boundary is for. The real schema is proven against the real catalog in the
// integration lane.
type stubColumns struct {
	tables map[string][]StoredColumn
	err    error
}

func (c stubColumns) Columns(_ context.Context, table string) ([]StoredColumn, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.tables[table], nil
}

// columnsOf builds a table from `name` or `name:sqltype` specs, defaulting to
// text — the type only matters where a case is about the kind a column can
// answer.
func columnsOf(specs ...string) []StoredColumn {
	columns := make([]StoredColumn, len(specs))
	for i, spec := range specs {
		name, sqlType, typed := strings.Cut(spec, ":")
		if !typed {
			sqlType = "text"
		}
		columns[i] = StoredColumn{Name: name, Type: sqlType}
	}
	return columns
}

func TestATableAnswersTheFieldsItHasColumnsFor(t *testing.T) {
	stored := newStorage(columnsOf("status", "amount_minor:bigint"))
	for _, field := range []Field{newField("status", KindText), newField("amount_minor", KindNumber)} {
		if !stored.answers(field) {
			t.Errorf("%q has a column and is not answerable", field.Name)
		}
	}
	// `stalled` is computed in the record mapper from last_activity_at: the
	// contract declares it, no table holds it.
	if stored.answers(newField("stalled", KindBoolean)) {
		t.Error("a field with no column is answerable; the vocabulary would publish what nothing can answer")
	}
}

// A nested contract object is stored flat — `address.city` is the column
// `address_city` — so the dotted name resolves under its flattened spelling.
func TestANestedContractPathResolvesToItsFlatColumn(t *testing.T) {
	stored := newStorage(columnsOf("address_city", "address_country"))
	city := newField("address.city", KindText)
	if !stored.answers(city) {
		t.Fatal("a nested path with a column behind it is not answerable")
	}
	expr, ok := stored.expr("t", city)
	if !ok || expr != `t."address_city"` {
		t.Errorf("expression is %q (ok=%v)", expr, ok)
	}
}

// The lookup still decides. A path that merely LOOKS like a stored one is not
// askable unless a column answers it.
func TestANestedPathWithNoColumnBehindItIsNotAnswerable(t *testing.T) {
	stored := newStorage(columnsOf("id:uuid", "full_name"))
	for _, field := range []string{"strength.score", "partner.margin_tier", "address.city"} {
		if stored.answers(newField(field, KindText)) {
			t.Errorf("%q is answerable off a table that does not hold it", field)
		}
	}
}

// The contract and the schema disagree about more than names.
// `deal.fx_rate_to_base` is a STRING in the contract — a decimal rendered as
// text so a ten-place rate never rounds through a float — and a `numeric`
// column in the table. Comparing it as text is not a narrower answer; it is a
// database syntax fault for a field the schema said was askable.
func TestAColumnThatCannotAnswerTheFieldsKindIsNotAskable(t *testing.T) {
	stored := newStorage(columnsOf("fx_rate_to_base:numeric", "status", "created_at:timestamp with time zone"))
	if stored.answers(newField("fx_rate_to_base", KindText)) {
		t.Error("a numeric column answers a text field")
	}
	if !stored.answers(newField("fx_rate_to_base", KindNumber)) {
		t.Error("a numeric column does not answer a number field")
	}
	// And the ordinary agreements still hold.
	if !stored.answers(newField("status", KindText)) || !stored.answers(newField("created_at", KindTimestamp)) {
		t.Error("a column of the field's own kind is not answerable")
	}
}

// A column of a type this vocabulary has no kind for is present and unaskable,
// rather than absent — there is no kind it could be compared under, and
// guessing one is how a comparison answers a different question.
func TestAColumnOfAnUnknownTypeAnswersNothing(t *testing.T) {
	stored := newStorage(columnsOf("search_tsv:tsvector", "raw:jsonb"))
	for _, kind := range []FieldKind{KindText, KindNumber, KindTimestamp, KindID, KindBoolean, KindDate} {
		if stored.answers(Field{Name: "raw", Kind: kind}) {
			t.Errorf("a jsonb column answers a %s field", kind)
		}
	}
	if stored.answers(newField("search_tsv", KindText)) {
		t.Error("a tsvector column answers a text field")
	}
}

// SEARCH-AC-17: the place is the ONE field published with no column behind it.
// Filtering it out would turn `distance_ranking_unavailable` into an
// unknown-operator refusal, which sends a caller to a text match on a city
// name — the quietly wrong answer declaring the operator exists to avoid.
func TestAPlaceIsPublishedWithoutStorageAndCompilesToNoExpression(t *testing.T) {
	stored := newStorage(columnsOf("address_city"))
	address := newField("address", KindGeo)
	if !stored.answers(address) {
		t.Fatal("the place is filtered out; within_radius would then be an unknown-vocabulary refusal")
	}
	if _, ok := stored.expr("t", address); ok {
		t.Error("a place compiles to an expression")
	}
}

// The unwired seam widens rather than narrows, and it never executes: a
// vocabulary is not a place to execute from.
func TestAnUnwiredStorageSeamAnswersEverythingAndCompilesNothing(t *testing.T) {
	stored := unfilteredStorage()
	if !stored.answers(newField("stalled", KindBoolean)) {
		t.Error("the unwired seam narrows the vocabulary")
	}
	if _, ok := stored.expr("t", newField("status", KindText)); ok {
		t.Error("the unwired seam compiles an expression; a pass-through would execute against a column it never checked")
	}
}

// The half of the invariant a unit test can hold: whatever the table holds,
// what the vocabulary publishes is exactly what the SQL builder can compile.
// Both halves read one locate(), so they cannot drift; this asserts it rather
// than asserting the comment. The other half — that the REAL schema answers
// the published vocabulary — is proven against the live catalog in the
// integration lane.
func TestEveryAnswerableFieldCompilesToAnExpression(t *testing.T) {
	stored := newStorage(columnsOf("status", "amount_minor:bigint", "address_city"))
	for _, field := range []Field{
		newField("status", KindText), newField("amount_minor", KindNumber),
		newField("address.city", KindText), newField("stalled", KindBoolean),
		newField("strength.score", KindNumber), newField("address.postal_code", KindText),
	} {
		_, compiles := stored.expr("t", field)
		if answerable := stored.answers(field); answerable != compiles {
			t.Errorf("%q: answerable=%v but compiles=%v", field.Name, answerable, compiles)
		}
	}
}

// SEARCH-AC-15 still holds with the storage half wired: the filter removes a
// name, it never contributes one. cf_* columns are physically present for
// every workspace that declared one, so a catalog read used as a SOURCE would
// hand one workspace's private column to the next caller.
func TestTheStorageFilterRemovesNamesAndNeverAddsThem(t *testing.T) {
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: map[string][]StoredColumn{
		"deal": columnsOf("id:uuid", "name", "status", "organization_id:uuid", "cf_another_workspaces_column"),
	}})
	vocab, err := resolver.Resolve(readerFor(entityDeal), entityDeal)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := vocab.Target(entityDeal)
	if !ok {
		t.Fatal("deal absent from its own vocabulary")
	}
	if _, ok := target.Field("cf_another_workspaces_column"); ok {
		t.Fatal("a column read off the schema entered the vocabulary; another workspace's field is now askable")
	}
	for _, want := range []string{"id", "name", "status"} {
		if _, ok := target.Field(want); !ok {
			t.Errorf("%q has a column and is not published", want)
		}
	}
	if _, ok := target.Field("stalled"); ok {
		t.Error("`stalled` has no column and is published")
	}
}

// The vocabulary published with the seam wired is a SUBSET of the one
// published without it. A storage filter that could widen would be a
// disclosure channel rather than a narrowing.
func TestTheStorageFilterOnlyEverNarrows(t *testing.T) {
	columns := map[string][]StoredColumn{"deal": columnsOf("id:uuid", "name", "status")}
	wide, err := NewVocabularyResolver().Resolve(readerFor(entityDeal), entityDeal)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := NewVocabularyResolver().WithColumnReader(stubColumns{tables: columns}).
		Resolve(readerFor(entityDeal), entityDeal)
	if err != nil {
		t.Fatal(err)
	}
	wideTarget, _ := wide.Target(entityDeal)
	narrowTarget, ok := narrow.Target(entityDeal)
	if !ok {
		t.Fatal("deal absent from the narrowed vocabulary")
	}
	if len(narrowTarget.Fields) >= len(wideTarget.Fields) {
		t.Fatalf("the filter did not narrow: %d fields with storage, %d without", len(narrowTarget.Fields), len(wideTarget.Fields))
	}
	for _, f := range narrowTarget.Fields {
		if _, ok := wideTarget.Field(f.Name); !ok {
			t.Errorf("%q is published only WITH the storage filter; the filter widened the vocabulary", f.Name)
		}
	}
}

// The INVERSE hop is declared by the referring record and executed against the
// referring record's column, so it is filtered by THAT table's schema. Kept
// separate from the forward case because the forward one is filtered
// incidentally — its reference is one of the target's own fields — and the
// inverse is the direction that would otherwise publish a hop no table can
// answer.
func TestAnInverseRelationIsFilteredByTheReferringTablesSchema(t *testing.T) {
	schema := map[string][]StoredColumn{
		"organization": columnsOf("id:uuid", "display_name"),
		"deal":         columnsOf("id:uuid", "name", "organization_id:uuid"),
		"project":      columnsOf("id:uuid", "name"), // no organization_id: no edge back
	}
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: schema})
	vocab, err := resolver.Resolve(readerFor(entityOrganization, entityDeal, entityProject), entityOrganization)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := vocab.Target(entityOrganization)
	if !ok {
		t.Fatal("organization absent from its own vocabulary")
	}
	if _, ok := target.Relation("deals"); !ok {
		t.Error("deal.organization_id is a column and declares no inverse hop")
	}
	if _, ok := target.Relation("projects"); ok {
		t.Error("an inverse hop is published from a column the referring table does not hold")
	}
}

// One schema read per TABLE per resolve. Composing a record type asks about
// every table that refers to it, so the same table comes up under several
// targets; re-reading it each time would multiply the cost by the size of the
// catalog for no new answer.
func TestTheSchemaIsReadAtMostOncePerTablePerResolve(t *testing.T) {
	reader := &countingColumns{tables: map[string][]StoredColumn{
		"organization": columnsOf("id:uuid", "display_name"),
		"deal":         columnsOf("id:uuid", "name", "organization_id:uuid", "project_id:uuid"),
		"project":      columnsOf("id:uuid", "name", "organization_id:uuid"),
	}}
	resolver := NewVocabularyResolver().WithColumnReader(reader)
	if _, err := resolver.Resolve(readerFor(entityOrganization, entityDeal, entityProject)); err != nil {
		t.Fatal(err)
	}
	for table, reads := range reader.perTable {
		if reads != 1 {
			t.Errorf("%s was read %d times in one resolve", table, reads)
		}
	}
	// The three composed record types, plus the tables that refer to them: a
	// resolve reads what it has to and nothing twice.
	if len(reader.perTable) < 3 {
		t.Errorf("only %d tables were read; the composition asks about more than its targets", len(reader.perTable))
	}
}

// A hop is derived from the fields that survived the filter, so a reference
// the table does not hold cannot become a traversable edge.
func TestARelationIsDerivedOnlyFromAReferenceTheTableHolds(t *testing.T) {
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: map[string][]StoredColumn{
		"deal": columnsOf("id:uuid", "name", "project_id:uuid"),
	}})
	vocab, err := resolver.Resolve(readerFor(entityDeal, entityOrganization, entityProject), entityDeal)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := vocab.Target(entityDeal)
	if _, ok := target.Relation(entityProject); !ok {
		t.Error("project_id is a column and declares no hop")
	}
	if _, ok := target.Relation(entityOrganization); ok {
		t.Error("organization_id is not a column on this table and still declares a hop")
	}
}

// A schema read that fails is not an empty schema. Answering an empty
// vocabulary would refuse every field the caller names with `unknown_field`,
// which reads exactly like a caller who mistyped one.
func TestASchemaReadThatFailsRefusesRatherThanNarrows(t *testing.T) {
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{err: errors.New("connection reset")})
	_, err := resolver.Resolve(readerFor(entityDeal), entityDeal)
	if err == nil {
		t.Fatal("a failed schema read resolved a vocabulary")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("the schema fault is not carried: %v", err)
	}
}

// tableFor and branchFor read the branch declarations rather than a second
// list, so a searchable record type cannot be storable in one place for a
// search and another for a plan.
func TestEverySearchableRecordTypeLocatesItsOwnTable(t *testing.T) {
	for entity := range contractRecords {
		table, ok := tableFor(entity)
		if !ok || table == "" {
			t.Errorf("%q has no table", entity)
		}
		branch, ok := branchFor(entity)
		if !ok || branch.table != table {
			t.Errorf("%q: branch table %q disagrees with %q", entity, branch.table, table)
		}
	}
	if _, ok := tableFor("workspace"); ok {
		t.Error("a record type this module does not search located a table")
	}
	if _, ok := branchFor("workspace"); ok {
		t.Error("a record type this module does not search located a branch")
	}
}

// The seam is read per resolve rather than memoized, so the custom-field
// engine's runtime ALTER TABLE is visible on the next plan instead of on the
// next restart.
func TestTheSchemaIsReadOnEveryResolve(t *testing.T) {
	reader := &countingColumns{tables: map[string][]StoredColumn{"deal": columnsOf("id:uuid", "name")}}
	resolver := NewVocabularyResolver().WithColumnReader(reader)
	for range 3 {
		if _, err := resolver.Resolve(readerFor(entityDeal), entityDeal); err != nil {
			t.Fatal(err)
		}
	}
	if reader.reads != 3 {
		t.Errorf("the schema was read %d times over 3 resolves; a cached schema keeps answering as it was", reader.reads)
	}
}

type countingColumns struct {
	tables   map[string][]StoredColumn
	reads    int
	perTable map[string]int
}

func (c *countingColumns) Columns(_ context.Context, table string) ([]StoredColumn, error) {
	c.reads++
	if c.perTable == nil {
		c.perTable = map[string]int{}
	}
	c.perTable[table]++
	return c.tables[table], nil
}

// The published document is the filtered vocabulary — one computation, so a
// caller cannot read a field in the schema that the validator will refuse.
func TestThePublishedDocumentCarriesTheFilteredVocabulary(t *testing.T) {
	resolver := NewVocabularyResolver().WithColumnReader(stubColumns{tables: map[string][]StoredColumn{
		"deal": columnsOf("id:uuid", "name", "status"),
	}})
	vocab, err := resolver.Resolve(readerFor(entityDeal))
	if err != nil {
		t.Fatal(err)
	}
	doc := querySchemaDocument(vocab)
	if len(doc.Targets) != 1 {
		t.Fatalf("the document publishes %d targets", len(doc.Targets))
	}
	names := make([]string, 0, len(doc.Targets[0].Fields))
	for _, f := range doc.Targets[0].Fields {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"id", "name", "status"}) {
		t.Errorf("the document publishes %v", names)
	}
}
