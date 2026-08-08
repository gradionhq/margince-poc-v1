// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// planTables is the schema every compilation case here is written against —
// enough of each record type to ask a real question, and deliberately missing
// the derived members (`stalled`) no table holds.
var planTables = map[string][]StoredColumn{
	"deal": columnsOf("id:uuid", "name", "status", "amount_minor:bigint",
		"expected_close_date:date", "closed_at:timestamp with time zone",
		"owner_id:uuid", "organization_id:uuid", "project_id:uuid"),
	"organization": columnsOf("id:uuid", "display_name", "owner_id:uuid",
		"is_anchor:boolean", "address_city", "visibility"),
	"project": columnsOf("id:uuid", "name", "owner_id:uuid", "organization_id:uuid", "visibility"),
	"person":  columnsOf("id:uuid", "full_name", "owner_id:uuid", "address_city", "visibility"),
}

// compilePlanDoc runs a plan document through the REAL decoder and validator
// before compiling it, so a case cannot assert against a plan the validator
// would never have produced.
func compilePlanDoc(ctx context.Context, t *testing.T, doc string) (string, []any) {
	t.Helper()
	plan := validatedPlanDoc(ctx, t, doc)
	executor := &QueryExecutor{columns: stubColumns{tables: planTables}}
	binding, err := executor.bindPlan(ctx, plan)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	compiler := &planCompiler{}
	sql, admitted, err := compiler.compileStatement(ctx, plan, binding)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if !admitted {
		t.Fatal("the record type was not admitted")
	}
	return sql, compiler.args
}

func validatedPlanDoc(ctx context.Context, t *testing.T, doc string) ValidatedPlan {
	t.Helper()
	decoded, err := DecodePlan([]byte(doc))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	validator := NewPlanValidator(NewVocabularyResolver().WithColumnReader(stubColumns{tables: planTables}))
	plan, err := validator.Validate(ctx, decoded)
	if err != nil {
		t.Fatalf("validating: %v", err)
	}
	return plan
}

// refusalFrom compiles a plan expected to be refused at bind time and answers
// the refusal.
func refusalFrom(ctx context.Context, t *testing.T, doc string) *PlanRefusal {
	t.Helper()
	plan := validatedPlanDoc(ctx, t, doc)
	executor := &QueryExecutor{columns: stubColumns{tables: planTables}}
	binding, err := executor.bindPlan(ctx, plan)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	compiler := &planCompiler{}
	_, _, err = compiler.compileStatement(ctx, plan, binding)
	var refusal *PlanRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("expected a typed refusal, got %v", err)
	}
	return refusal
}

// teamReaderFor is a principal bounded to one team's rows — the same grants as
// readerFor with the row scope narrowed, so a case comparing the two isolates
// row scope from object RBAC.
func teamReaderFor(objects ...string) context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, o := range objects {
		grants[o] = principal.ObjectGrant{Read: true}
	}
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		TeamIDs:     []ids.UUID{ids.NewV7()},
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeTeam},
	})
}

func TestAPredicateCompilesToABoundComparison(t *testing.T) {
	sql, args := compilePlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"},
		          {"field": "amount_minor", "op": "gte", "value": 100000}]}`)
	if !strings.Contains(sql, `t."status" = $1`) || !strings.Contains(sql, `t."amount_minor" >= $2`) {
		t.Fatalf("statement is %q", sql)
	}
	if len(args) != 3 || args[0] != "open" || args[1] != int64(100000) {
		t.Fatalf("args are %v; the operands must be bound under their own kinds", args)
	}
	// The page ceiling is the limit plus one, so a truncated answer is
	// detectable rather than merely suspected.
	if args[2] != storekitDefaultPage()+1 {
		t.Errorf("fetch ceiling is %v", args[2])
	}
	if !strings.Contains(sql, "ORDER BY t.id DESC") {
		t.Errorf("the exact lane has no deterministic order: %q", sql)
	}
}

// storekitDefaultPage is the contract default page, read from the same clamp
// the validator reads rather than restated as a number that could disagree.
func storekitDefaultPage() int {
	plan, _ := effectiveLimit(nil)
	return plan
}

// A row whose field is UNSET is distinct from every value. Rendering `neq` as
// `<>` would drop those rows from an answer a caller reads as "everything that
// is not X".
func TestNotEqualAdmitsRowsWhoseFieldIsUnset(t *testing.T) {
	sql, _ := compilePlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "neq", "value": "lost"}]}`)
	if !strings.Contains(sql, `t."status" IS DISTINCT FROM $1`) {
		t.Fatalf("statement is %q", sql)
	}
}

func TestAMembershipTestBindsEveryElementSeparately(t *testing.T) {
	sql, args := compilePlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "in", "values": ["open", "won"]}]}`)
	if !strings.Contains(sql, `t."status" IN ($1, $2)`) {
		t.Fatalf("statement is %q", sql)
	}
	if args[0] != "open" || args[1] != "won" {
		t.Fatalf("args are %v", args)
	}
}

// A date compared through a timestamp is resolved at the session's time zone,
// which makes the same plan answer differently on two servers.
func TestADateBindsAsADateRatherThanAnInstant(t *testing.T) {
	sql, args := compilePlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "expected_close_date", "op": "lte", "value": "2026-12-31"}]}`)
	if !strings.Contains(sql, `t."expected_close_date" <= $1::date`) {
		t.Fatalf("statement is %q", sql)
	}
	if args[0] != "2026-12-31" {
		t.Fatalf("args are %v", args)
	}
}

// The validator left FORMAT to the executor, so this is where a date that is
// not one is refused — rather than becoming a query that quietly matches
// nothing, which is indistinguishable from an empty workspace.
func TestAMalformedDateIsRefusedRatherThanMatchingNothing(t *testing.T) {
	refusal := refusalFrom(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "expected_close_date", "op": "eq", "value": "next tuesday"}]}`)
	assertOperandFault(t, refusal, "where[0].value")
}

func TestAMalformedInstantIsRefused(t *testing.T) {
	refusal := refusalFrom(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "closed_at", "op": "gte", "value": "2026-13-45"}]}`)
	assertOperandFault(t, refusal, "where[0].value")
}

func TestAnIdentifierThatIsNotAUUIDIsRefused(t *testing.T) {
	refusal := refusalFrom(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "owner_id", "op": "eq", "value": "not-a-uuid"}]}`)
	assertOperandFault(t, refusal, "where[0].value")
}

// Every bad operand is reported, not the first: a caller told about one of
// three makes three round trips to learn what one answer could have carried.
func TestEveryUnbindableOperandIsReported(t *testing.T) {
	refusal := refusalFrom(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "expected_close_date", "op": "eq", "value": "yesterday"},
		          {"field": "owner_id", "op": "eq", "value": "nope"}]}`)
	if len(refusal.FieldFaults()) != 2 {
		t.Fatalf("refusal names %d faults: %v", len(refusal.FieldFaults()), refusal.FieldFaults())
	}
}

// One bad element in a membership list names THAT element, so the caller edits
// the value rather than the clause.
func TestABadElementOfAMembershipListNamesItsPosition(t *testing.T) {
	refusal := refusalFrom(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "owner_id", "op": "in",
		           "values": ["0199a5c5-0000-7000-8000-000000000001", "nope"]}]}`)
	assertOperandFault(t, refusal, "where[0].values[1]")
}

// assertOperandFault is the one verdict an unbindable operand gets: the value
// is not the shape its field takes, named at the path the caller wrote it.
func assertOperandFault(t *testing.T, refusal *PlanRefusal, path string) {
	t.Helper()
	for _, fault := range refusal.FieldFaults() {
		if fault.Field == path && fault.Code == CodeValueTypeMismatch {
			return
		}
	}
	t.Fatalf("no %s at %q; faults are %v", CodeValueTypeMismatch, path, refusal.FieldFaults())
}

// A hop returns the record that admitted the row, so the traversal is legible
// as a reason rather than as an invisible filter.
func TestATraversalCompilesToALateralThatCarriesItsEvidence(t *testing.T) {
	sql, args := compilePlanDoc(readerFor(entityDeal, entityOrganization), t, `{
		"version": "v1", "target": "deal",
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	for _, want := range []string{
		"JOIN LATERAL", "hop_id", "hop_title", `h.id = t."organization_id"`,
		"h.archived_at IS NULL", "ORDER BY h.id LIMIT 1",
		// The nested contract path resolves to the flat column that holds it,
		// under the HOP's alias.
		`h."address_city" =`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("statement lacks %q: %s", want, sql)
		}
	}
	if !slices.Contains(args, any("Stuttgart")) {
		t.Fatalf("args are %v", args)
	}
}

// The inverse edge is derived from the referring record's column, so the join
// condition points the other way.
func TestAnInverseTraversalJoinsOnTheReferringColumn(t *testing.T) {
	sql, _ := compilePlanDoc(readerFor(entityOrganization, entityDeal), t, `{
		"version": "v1", "target": "organization",
		"traverse": {"relation": "deals",
		             "where": [{"field": "status", "op": "eq", "value": "open"}]}}`)
	if !strings.Contains(sql, `h."organization_id" = t.id`) {
		t.Fatalf("statement is %q", sql)
	}
}

// A hop is a READ of the record it lands on. A caller who cannot see the
// Stuttgart organization must not be able to select deals through it either.
func TestTheHopCarriesItsOwnRowScopeUnderItsOwnAlias(t *testing.T) {
	sql, _ := compilePlanDoc(teamReaderFor(entityDeal, entityOrganization), t, `{
		"version": "v1", "target": "deal",
		"traverse": {"relation": "organization"}}`)
	lateral, outer, found := strings.Cut(sql, ") hop ON true")
	if !found {
		t.Fatalf("no lateral join in %q", sql)
	}
	// The hop's visibility is decided about the ORGANIZATION row, under the
	// hop's own alias. Rendered against `t` it would decide about the deal —
	// a visibility rule answering about a different record.
	if !strings.Contains(lateral, "h.owner_id") {
		t.Fatalf("the hop's row scope is not rendered against the hop: %s", lateral)
	}
	if strings.Contains(lateral, "t.owner_id") {
		t.Fatalf("the hop's row scope filters the outer table: %s", lateral)
	}
	if !strings.Contains(outer, "t.owner_id") {
		t.Fatalf("the target carries no row scope: %s", outer)
	}
}

// Every read on this surface carries the same narrowing: archived rows are out,
// and the installation's own company is not an account to discover.
func TestTheStatementCarriesTheDiscoveryNarrowingEveryReadCarries(t *testing.T) {
	sql, _ := compilePlanDoc(readerFor(entityOrganization), t, `{"version": "v1", "target": "organization"}`)
	for _, want := range []string{"t.archived_at IS NULL", "NOT t.is_anchor"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("statement lacks %q: %s", want, sql)
		}
	}
}

// Object RBAC can change under a plan. A caller who lost the grant mid-flight
// learns the same thing an empty workspace tells them, rather than a 403 that
// confirms the record type is populated.
func TestARecordTypeNoLongerAdmittedCompilesToNoStatement(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{"version": "v1", "target": "deal"}`)
	compiler := &planCompiler{}
	binding := planBinding{
		branch:  mustBranch(t, entityDeal),
		columns: newStorage(planTables["deal"]),
		fetch:   plan.Limit + 1,
	}
	// The same plan, compiled for a principal who may no longer read deals.
	sql, admitted, err := compiler.compileStatement(readerFor(entityPerson), plan, binding)
	if err != nil {
		t.Fatal(err)
	}
	if admitted || sql != "" {
		t.Fatalf("a record type the caller may not read compiled to %q", sql)
	}
}

func mustBranch(t *testing.T, entity string) searchBranch {
	t.Helper()
	branch, ok := branchFor(entity)
	if !ok {
		t.Fatalf("%q has no branch", entity)
	}
	return branch
}

// The ranked lane narrows the statement to the ids it ranked. Compiling it
// without that membership test would answer the unfiltered question.
func TestTheRankedLaneNarrowsTheStatementToItsCandidates(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{"version": "v1", "target": "deal"}`)
	first, second := ids.NewV7(), ids.NewV7()
	compiler := &planCompiler{}
	sql, admitted, err := compiler.compileStatement(readerFor(entityDeal), plan, planBinding{
		branch:     mustBranch(t, entityDeal),
		columns:    newStorage(planTables["deal"]),
		candidates: []ids.UUID{first, second},
		fetch:      plan.Limit + 1,
	})
	if err != nil || !admitted {
		t.Fatalf("compiling: %v", err)
	}
	if !strings.Contains(sql, "t.id IN ($1, $2)") {
		t.Fatalf("statement is %q", sql)
	}
	// The ranked lane's order is the retriever's, applied after the rows come
	// back, so the statement must not impose the table's.
	if strings.Contains(sql, "ORDER BY t.id") || strings.Contains(sql, "LIMIT") {
		t.Fatalf("the ranked lane imposed the table's order: %s", sql)
	}
	if compiler.args[0] != first || compiler.args[1] != second {
		t.Fatalf("args are %v", compiler.args)
	}
}

// A field the vocabulary published and the table cannot answer is a wiring
// fault, and it fails loudly rather than compiling to a comparison against
// nothing. It is unreachable through Execute — the fitness function is what
// keeps it so.
func TestAFieldWithNoStoragePathRefusesRatherThanCompiling(t *testing.T) {
	compiler := &planCompiler{}
	vocab := TargetVocabulary{Target: entityDeal, Fields: []Field{newField("stalled", KindBoolean)}}
	_, refusal := compiler.clause("t", newStorage(planTables["deal"]), vocab, "where[0]",
		Predicate{Field: "stalled", Op: OpEq, Value: []byte("true")})
	if refusal == nil || refusal.Code != CodeUnknownField {
		t.Fatalf("refusal is %v", refusal)
	}
}

// A plan that never passed validation cannot be smuggled past the compiler by
// naming a field the vocabulary does not carry.
func TestAFieldOutsideTheVocabularyRefusesAtCompileTime(t *testing.T) {
	compiler := &planCompiler{}
	_, refusal := compiler.clause("t", newStorage(planTables["deal"]),
		TargetVocabulary{Target: entityDeal}, "where[0]",
		Predicate{Field: "invented", Op: OpEq, Value: []byte(`"x"`)})
	if refusal == nil || refusal.Code != CodeUnknownField {
		t.Fatalf("refusal is %v", refusal)
	}
}

// An operator with no SQL spelling is refused rather than rendered as its own
// machine name into a statement.
func TestAnOperatorWithNoSQLSpellingIsRefused(t *testing.T) {
	compiler := &planCompiler{}
	vocab := TargetVocabulary{Target: entityDeal, Fields: []Field{newField("status", KindText)}}
	_, refusal := compiler.clause("t", newStorage(planTables["deal"]), vocab, "where[0]",
		Predicate{Field: "status", Op: "matches", Value: []byte(`"open"`)})
	if refusal == nil || refusal.Code != CodeUnknownOperator {
		t.Fatalf("refusal is %v", refusal)
	}
}

// A place is never compared: within_radius answers
// distance_ranking_unavailable and the executor stops before a statement is
// built. Binding one anyway is a fault rather than a comparison against a
// jsonb blob.
func TestAPlaceOperandNeverBinds(t *testing.T) {
	compiler := &planCompiler{}
	_, _, refusal := compiler.bind("where[0].value", newField("address", KindGeo),
		[]byte(`{"center": "Stuttgart", "radius_km": 50}`))
	if refusal == nil || refusal.Code != CodeValueTypeMismatch {
		t.Fatalf("refusal is %v", refusal)
	}
}

// The edge is read off Relation.Via, whose two spellings say which side of the
// reference each direction lives on.
func TestTheEdgeDirectionIsReadOffTheDerivedReference(t *testing.T) {
	forward := newHopBinding(Relation{Name: "organization", Target: "organization", Via: "organization_id"},
		mustBranch(t, entityOrganization), nil)
	if !forward.forward || forward.column != "organization_id" {
		t.Errorf("forward edge is %+v", forward)
	}
	inverse := newHopBinding(Relation{Name: "deals", Target: "deal", Via: "deal.organization_id"},
		mustBranch(t, entityDeal), nil)
	if inverse.forward || inverse.column != "organization_id" {
		t.Errorf("inverse edge is %+v", inverse)
	}
}

// The refusal the compiler answers is the plural fault form every transport
// already renders, so an operand fault reads as a 422 rather than as an
// unclassified internal error.
var _ apperrors.FieldFaults = (*PlanRefusal)(nil)
