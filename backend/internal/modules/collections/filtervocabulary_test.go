// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// What this operation guarantees is a subset relation, and the tests below are
// organised around it: everything advertised is accepted by the engine, and the
// gap between advertised and accepted is exactly the retired set.
//
// The advertised ⊆ accepted half is structural — the operation starts from the
// engine's own map — so the tests that can actually FAIL are the ones about what
// it removes from that map and what it says about what remains: the retirement
// exclusions (both kinds), the operator subset, the gate, the ordering, and the
// one thing the engine does not know (whether a workspace defined the field).
//
// The two contract ENUMS this operation reports through are gated a level up, in
// compose/filtervocabularyenums_test.go, which reads them out of the contract
// rather than naming them — and lives there because compose is the one package
// allowed to hold both the contract and the engine. A list of enum members
// written here would be a third copy of the vocabulary, which is the defect this
// endpoint exists to remove.
//
// And operatorOrder — the projection storekit.OperatorsFor answers through, which
// every gate here derives its expectation from — is pinned in storekit's own
// suite, since that is the only package where it and the operator matrix are
// visible together.

import (
	"context"
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// grantCtx binds a human actor holding exactly one object grant, so a test
// cannot pass on a permission the operation does not require.
func grantCtx(object string, grant principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:        principal.PrincipalHuman,
		Permissions: principal.Permissions{Objects: map[string]principal.ObjectGrant{object: grant}},
	})
}

// readerCtx holds `list:read` — the gate FilterVocabulary applies.
func readerCtx() context.Context {
	return grantCtx("list", principal.ObjectGrant{Read: true})
}

// everyTypeCatalog seeds one custom column per filterable custom-field type, so
// the operator loop below sees all seven types rather than the three the core
// vocabularies happen to use.
//
// Without this the ordering operators (gt/gte/lt/lte) are only ever probed
// against id/text/picklist fields, which correctly omit them — so the "every
// reported operator compiles" direction would never be exercised for a single one
// of them, and a test named for the whole matrix would prove less than half of it.
//
// Derived from fieldcatalog.Types(), so a seventh type joins by existing.
func everyTypeCatalog() stubFilterable {
	columns := make([]fieldcatalog.Column, 0, len(fieldcatalog.Types()))
	for _, declared := range fieldcatalog.Types() {
		columns = append(columns, fieldcatalog.Column{Name: "cf_" + declared, Type: declared})
	}
	cols := map[string][]fieldcatalog.Column{}
	for resource := range segmentEngines {
		cols[resource] = columns
	}
	return stubFilterable{cols: cols}
}

// The subset relation's operator half: for every field the vocabulary reports,
// every operator it reports must compile, and every operator it does NOT report
// must be refused. A builder that disabled the wrong operators would show a human
// a control the engine rejects.
func TestEveryReportedOperatorCompilesAndEveryOmittedOneIsRefused(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(everyTypeCatalog())
	seenTypes := map[string]bool{}
	for resource := range segmentEngines {
		fields, ok, err := store.FilterVocabulary(readerCtx(), resource)
		if err != nil || !ok {
			t.Fatalf("%s: filterVocabulary: ok=%v err=%v", resource, ok, err)
		}
		engine, _, err := store.SegmentEngine(context.Background(), resource)
		if err != nil {
			t.Fatalf("%s: segmentEngine: %v", resource, err)
		}
		for _, field := range fields {
			seenTypes[field.Type] = true
			reported := make(map[string]bool, len(field.Operators))
			for _, op := range field.Operators {
				reported[op] = true
			}
			for _, op := range everyOperator() {
				_, compileErr := storekit.CompilePredicate(
					storekit.Predicate{Field: field.Name, Op: op, Value: operandFor(op)},
					engine.Fields,
					func(any) int { return 1 },
				)
				// The operand is only well-typed for some field types, so a
				// refusal can be about the VALUE rather than the operator. Only
				// an operator-shaped refusal answers this question — and it is
				// distinguishable because compileLeaf checks the operator matrix
				// BEFORE validating the operand, so an unadmitted operator can
				// only ever come back as CodeFilterOpNotAllowed. If that ordering
				// ever inverted, this test would start failing rather than start
				// concluding the wrong thing.
				refusedTheOperator := isOperatorRefusal(compileErr)
				if reported[op] && refusedTheOperator {
					t.Errorf("%s.%s reports %q but the engine refuses that operator", resource, field.Name, op)
				}
				if !reported[op] && !refusedTheOperator {
					t.Errorf("%s.%s omits %q but the engine admits it", resource, field.Name, op)
				}
			}
		}
	}
	// The loop above is only as good as the types it reached, so it says which.
	// Silence here would let the catalogue stub stop working and leave a test
	// named for the whole matrix quietly proving three sevenths of it.
	for _, declared := range append(fieldcatalog.Types(), string(storekit.FieldID)) {
		if !seenTypes[declared] {
			t.Errorf("no field of type %q was reported by any resource, so the operator check never covered that type", declared)
		}
	}
}

// A custom column is reported as custom and a core field is not. The merge lets
// core win a name collision, so this also pins that a colliding catalogue row is
// reported the way the engine actually resolved it.
func TestACustomColumnIsReportedCustomAndACollidingOneIsNot(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {
			{Name: "cf_qa_owner", Type: fieldcatalog.TypeText},
			// Collides with a core field; SegmentEngine keeps the core one.
			{Name: "owner_id", Type: fieldcatalog.TypeText},
		},
	}})
	byName := map[string]VocabularyField{}
	fields, _, err := store.FilterVocabulary(readerCtx(), "person")
	if err != nil {
		t.Fatalf("filterVocabulary: %v", err)
	}
	for _, f := range fields {
		byName[f.Name] = f
	}
	if custom, present := byName["cf_qa_owner"]; !present || !custom.Custom {
		t.Errorf("cf_qa_owner: present=%v custom=%v, want a reported custom field", present, custom.Custom)
	}
	if core, present := byName["owner_id"]; !present || core.Custom {
		t.Errorf("owner_id: present=%v custom=%v — the core field won the collision, so it is not custom", present, core.Custom)
	}
	if got := byName["owner_id"].Type; got != string(storekit.FieldID) {
		t.Errorf("owner_id type = %q, want id — reporting the catalogue's text would retype a core field", got)
	}
}

// Two identical requests answer the same order. The fields come out of a map, so
// without the sort this passes by luck and a picker reshuffles between renders.
func TestTheVocabularyIsOrderedTheSameWayTwice(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {
			{Name: "cf_zeta", Type: fieldcatalog.TypeText},
			{Name: "cf_alpha", Type: fieldcatalog.TypeNumber},
			{Name: "cf_mid", Type: fieldcatalog.TypeDate},
		},
	}})
	first, _, err := store.FilterVocabulary(readerCtx(), "person")
	if err != nil {
		t.Fatalf("filterVocabulary: %v", err)
	}
	for range 8 {
		again, _, err := store.FilterVocabulary(readerCtx(), "person")
		if err != nil {
			t.Fatalf("filterVocabulary: %v", err)
		}
		// Lengths first: indexing one by the other's range panics on a length
		// regression instead of reporting it.
		if len(again) != len(first) {
			t.Fatalf("the vocabulary answered %d fields then %d for the same request", len(first), len(again))
		}
		for i := range first {
			if first[i].Name != again[i].Name {
				t.Fatalf("field %d = %q then %q — the order is not stable", i, first[i].Name, again[i].Name)
			}
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Name >= first[i].Name {
			t.Errorf("fields %q and %q are not in name order", first[i-1].Name, first[i].Name)
		}
	}
}

// The gap between advertised and accepted, in the custom half: a retired column
// is still compilable and no longer offered.
//
// Both halves matter and they are different failures. Advertising it puts a field
// in a picker that an admin retired precisely to get it out of there
// (CUSTOM-FIELDS-AC-13). Dropping it from the engine turns every saved segment
// naming it into a read-time error.
func TestARetiredCustomColumnIsStillCompilableAndNoLongerOffered(t *testing.T) {
	const retired, live = "cf_old_tier", "cf_tier"
	store := (&Store{}).WithFieldCatalog(stubFilterable{
		cols: map[string][]fieldcatalog.Column{"person": {
			{Name: retired, Type: fieldcatalog.TypeText},
			{Name: live, Type: fieldcatalog.TypeText},
		}},
		retired: map[string]bool{retired: true},
	})

	fields, _, err := store.FilterVocabulary(readerCtx(), "person")
	if err != nil {
		t.Fatalf("filterVocabulary: %v", err)
	}
	advertised := map[string]bool{}
	for _, f := range fields {
		advertised[f.Name] = true
	}
	if advertised[retired] {
		t.Errorf("%s is retired and was offered for a new clause", retired)
	}
	if !advertised[live] {
		t.Errorf("%s is active and was not offered", live)
	}

	// The other half, through the engine the saved segment would be evaluated by.
	engine, _, err := store.SegmentEngine(context.Background(), "person")
	if err != nil {
		t.Fatalf("segmentEngine: %v", err)
	}
	if _, compileErr := storekit.CompilePredicate(
		storekit.Predicate{Field: retired, Op: storekit.OpEq, Value: "gold"},
		engine.Fields,
		func(any) int { return 1 },
	); compileErr != nil {
		t.Errorf("a segment naming the retired %s no longer evaluates: %v", retired, compileErr)
	}
}

// The same gap in the core half, which has no catalogue row behind it at all.
//
// organization.classification was retired by ADR-0079/A124 and has no
// `custom_field` row, so no client-side join could ever discover that it is
// retired — the exclusion has to happen here or not at all.
func TestARetiredCoreFieldIsStillCompilableAndNoLongerOffered(t *testing.T) {
	const retired = "classification"
	store := &Store{}
	fields, _, err := store.FilterVocabulary(readerCtx(), "organization")
	if err != nil {
		t.Fatalf("filterVocabulary: %v", err)
	}
	for _, f := range fields {
		if f.Name == retired {
			t.Errorf("%s was retired by ADR-0079 and is still offered for a new clause", retired)
		}
	}
	engine, _, err := store.SegmentEngine(context.Background(), "organization")
	if err != nil {
		t.Fatalf("segmentEngine: %v", err)
	}
	if _, compileErr := storekit.CompilePredicate(
		storekit.Predicate{Field: retired, Op: storekit.OpEq, Value: "strategic"},
		engine.Fields,
		func(any) int { return 1 },
	); compileErr != nil {
		t.Errorf("a segment naming the retired %s no longer evaluates: %v", retired, compileErr)
	}
}

// Every name in retiredCoreFields has to BE a core field of that resource, or the
// entry silently excludes nothing and the field it was meant to retire stays on
// offer. A typo is otherwise invisible.
func TestEveryRetiredCoreFieldNamesARealCoreField(t *testing.T) {
	for resource, retired := range retiredCoreFields {
		engine, present := segmentEngines[resource]
		if !present {
			t.Errorf("retiredCoreFields names resource %q, which has no engine", resource)
			continue
		}
		for name := range retired {
			if _, isCore := engine.Fields[name]; !isCore {
				t.Errorf("retiredCoreFields[%q] names %q, which is not a core field of that resource — the exclusion does nothing", resource, name)
			}
		}
	}
}

// The read is gated, and gated on the object whose filters it describes. A
// principal with no list grant learns nothing about a workspace's custom fields.
func TestReadingTheVocabularyNeedsTheListReadGrant(t *testing.T) {
	ungranted := grantCtx("tag", principal.ObjectGrant{Read: true})
	_, _, err := (&Store{}).FilterVocabulary(ungranted, "person")
	if err == nil {
		t.Fatal("a principal without list:read read the filter vocabulary")
	}
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("err = %v, want permission denied", err)
	}
}

// A resource with no engine is distinguishable from one whose vocabulary is
// empty: the handler turns the first into a 404, and an empty field list would
// instead claim the type has nothing to filter on.
func TestAResourceWithNoEngineIsNotAnEmptyVocabulary(t *testing.T) {
	fields, ok, err := (&Store{}).FilterVocabulary(readerCtx(), "activity")
	if err != nil {
		t.Fatalf("filterVocabulary: %v", err)
	}
	if ok {
		t.Error("activity reported an engine; it is not a predicate-leaf resource")
	}
	if fields != nil {
		t.Errorf("fields = %v, want nil so the caller cannot mistake it for an empty vocabulary", fields)
	}
}

// The handler's 404 branch is unreachable only while every resource the contract
// admits has an engine. This is what keeps it that way — and what makes the
// branch honest rather than dead: it fires the moment the two disagree.
func TestEveryResourceTheContractAdmitsHasAnEngine(t *testing.T) {
	for _, admitted := range []crmcontracts.GetFilterVocabularyParamsResource{
		crmcontracts.GetFilterVocabularyParamsResourcePerson,
		crmcontracts.GetFilterVocabularyParamsResourceOrganization,
		crmcontracts.GetFilterVocabularyParamsResourceDeal,
		crmcontracts.GetFilterVocabularyParamsResourceLead,
		crmcontracts.GetFilterVocabularyParamsResourceProject,
	} {
		if _, present := segmentEngines[string(admitted)]; !present {
			t.Errorf("the contract admits resource %q but no engine serves it, so the operation 404s on a value it advertises", admitted)
		}
	}
	if len(segmentEngines) != 5 {
		t.Errorf("segmentEngines has %d resources; the list above enumerates the contract's 5 and must gain any new one", len(segmentEngines))
	}
}

// Every operator OperatorsFor can answer, so a caller iterating them is
// iterating the engine's set rather than a copy.
func everyOperator() []string {
	seen := map[string]bool{}
	ops := []string{}
	for _, admitted := range operatorsByTypeForTest() {
		for _, op := range admitted {
			if !seen[op] {
				seen[op] = true
				ops = append(ops, op)
			}
		}
	}
	return ops
}

// operatorsByTypeForTest reaches the matrix through the exported accessor, one
// call per filterable type, so this file holds no copy of it.
func operatorsByTypeForTest() [][]string {
	types := []storekit.FieldType{storekit.FieldID}
	for _, declared := range fieldcatalog.Types() {
		types = append(types, storekit.FieldType(declared))
	}
	out := make([][]string, 0, len(types))
	for _, t := range types {
		// A base-table field: this gate is about the type matrix, and a linked
		// field answers a deliberately narrower set of its own.
		out = append(out, storekit.OperatorsFor(storekit.Field{Type: t}))
	}
	return out
}

// operandFor supplies an operand of the shape each operator requires, so a
// compile that fails does so about the OPERATOR rather than the value.
//
//craft:ignore naked-any the return IS a Predicate.Value, which storekit declares as any because a filter operand is a decoded JSON scalar or array — a concrete type here could not be assigned to the field under test
func operandFor(op string) any {
	switch op {
	case storekit.OpExists:
		return true
	case storekit.OpIn:
		return []any{"x"}
	default:
		return "x"
	}
}

// isOperatorRefusal separates "this type does not admit this operator" from
// "this operand is the wrong shape for this field". Only the first answers
// whether the vocabulary reported the right operator set.
func isOperatorRefusal(err error) bool {
	if err == nil {
		return false
	}
	var pred *storekit.PredicateError
	if !errors.As(err, &pred) {
		return false
	}
	return pred.Code == storekit.CodeFilterOpNotAllowed
}
