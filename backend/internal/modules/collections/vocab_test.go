// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// The segment vocabulary's own obligations: every type that can be tagged can be
// filtered by tag, and the tag leaf reaches the join the same way for each.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// Derived, not listed: a fifth taggable type fails here rather than shipping
// without a tag filter, which is the failure nobody would notice.
func TestEveryTaggableTypeCanBeFilteredByTag(t *testing.T) {
	for _, entity := range taggableEntityTypes() {
		engine, ok := segmentEngines[entity]
		if !ok {
			t.Fatalf("%s is taggable but has no segment engine", entity)
		}
		field, ok := engine.Fields["tag"]
		if !ok {
			t.Fatalf("%s is taggable but carries no tag filter field", entity)
		}
		if field.Link == "" {
			t.Errorf("%s's tag field is not a link leaf; it cannot reach taggable", entity)
		}
		if !strings.Contains(field.Link, "tg.entity_type = '"+entity+"'") {
			t.Errorf("%s's tag field does not bind its own entity_type: %q", entity, field.Link)
		}
		if strings.Contains(field.Link, "workspace_id") {
			t.Errorf("%s's tag field names taggable.workspace_id, which migration 0228 dropped", entity)
		}
		if count := strings.Count(field.Link, "%s"); count != 1 {
			t.Errorf("%s's tag field has %d %%s verbs in its Link template, want exactly 1: %q", entity, count, field.Link)
		}
	}
}

// A project is a taggable record (taggable's own CHECK admits it) and its
// list membership must offer the same filter every other taggable type does.
func TestProjectIsFilterableByTag(t *testing.T) {
	field, ok := segmentEngines[projectEntity].Fields["tag"]
	if !ok {
		t.Fatal("project is taggable but carries no tag filter field")
	}
	if !strings.Contains(field.Link, "tg.entity_type = '"+projectEntity+"'") {
		t.Errorf("project's tag field does not bind its own entity_type: %q", field.Link)
	}
}

// A stub rather than the real catalog: this is the MERGE's contract, and the
// catalog read itself is proven against Postgres in the customfields suite.
type stubFilterable struct {
	cols map[string][]fieldcatalog.Column
	err  error
}

func (s stubFilterable) FilterableColumns(_ context.Context, object string) ([]fieldcatalog.Column, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cols[object], nil
}

func TestSegmentEngineMergesCustomColumnsWithCoreFields(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_qa_owner", Type: fieldcatalog.TypeText}},
	}})
	engine, ok, err := store.SegmentEngine(context.Background(), "person")
	if err != nil || !ok {
		t.Fatalf("segmentEngine: ok=%v err=%v", ok, err)
	}
	if _, present := engine.Fields["owner_id"]; !present {
		t.Error("the core vocabulary was lost in the merge")
	}
	custom, present := engine.Fields["cf_qa_owner"]
	if !present {
		t.Fatal("the custom column did not join the vocabulary")
	}
	if custom.Expr != `t."cf_qa_owner"` {
		t.Errorf("custom Expr = %q, want the quoted column on the base alias", custom.Expr)
	}
	if custom.Type != storekit.FieldText {
		t.Errorf("custom Type = %q, want text", custom.Type)
	}
}

// The static map is shared by every request in the process. Merging into it in
// place would leak one workspace's custom vocabulary into every later request —
// including requests from other installations of the same binary.
func TestSegmentEngineDoesNotMutateTheStaticVocabulary(t *testing.T) {
	before := len(segmentEngines["person"].Fields)
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_leaked", Type: fieldcatalog.TypeText}},
	}})
	if _, _, err := store.SegmentEngine(context.Background(), "person"); err != nil {
		t.Fatalf("segmentEngine: %v", err)
	}
	if got := len(segmentEngines["person"].Fields); got != before {
		t.Fatalf("the static vocabulary grew from %d to %d fields", before, got)
	}
	if _, leaked := segmentEngines["person"].Fields["cf_leaked"]; leaked {
		t.Error("a request's custom column landed in the process-wide vocabulary")
	}
}

// An unwired catalog is the port's documented pass-through, not a failure: a
// deployment that never mounted the module, and every unit test, filters on core
// fields exactly as before.
func TestSegmentEngineWithoutACatalogServesCoreFields(t *testing.T) {
	engine, ok, err := (&Store{}).SegmentEngine(context.Background(), "person")
	if err != nil || !ok {
		t.Fatalf("segmentEngine: ok=%v err=%v", ok, err)
	}
	if _, present := engine.Fields["owner_id"]; !present {
		t.Error("core vocabulary missing without a catalog")
	}
}

// A catalog that cannot answer must not be reported as a filter the caller got
// wrong: the missing field would compile to filter_field_not_allowed, telling a
// user their saved list is invalid when the truth is a failed read.
func TestSegmentEngineReportsACatalogFailureAsItsOwn(t *testing.T) {
	boom := errors.New("catalog unreachable")
	store := (&Store{}).WithFieldCatalog(stubFilterable{err: boom})
	_, _, err := store.SegmentEngine(context.Background(), "person")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the catalog's own error", err)
	}
	var perr *storekit.PredicateError
	if errors.As(err, &perr) {
		t.Error("a catalog failure was dressed up as a predicate validation error")
	}
}

// A resource with no engine is not an error to resolve — it is a question the
// caller answers (a 422 for a client-supplied entity_type, an invariant break
// for a stored one).
func TestSegmentEngineHasNoEngineForAnUnknownResource(t *testing.T) {
	_, ok, err := (&Store{}).SegmentEngine(context.Background(), "activity")
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if ok {
		t.Error("activity resolved an engine; it is not a predicate-leaf resource")
	}
}

// A catalogue row named after a core column is a Go-side convention violation
// (cf_ prefixing), not a DDL impossibility — the merge has to survive one
// without retyping the core field underneath it. owner_id is a real core
// field on "person" (storekit.FieldID); a colliding catalogue entry typed
// text would otherwise replace it, admitting a substring operator against a
// uuid column.
func TestSegmentEngineCoreFieldWinsACatalogNameCollision(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {{Name: "owner_id", Type: fieldcatalog.TypeText}},
	}})
	engine, ok, err := store.SegmentEngine(context.Background(), "person")
	if err != nil || !ok {
		t.Fatalf("segmentEngine: ok=%v err=%v", ok, err)
	}
	field, present := engine.Fields["owner_id"]
	if !present {
		t.Fatal("owner_id was dropped from the vocabulary entirely, not just left as core's")
	}
	if field.Type != storekit.FieldID {
		t.Errorf("owner_id typed as %q, want %q — the colliding catalogue column overrode the core field", field.Type, storekit.FieldID)
	}
	if field.Expr != colOwnerID {
		t.Errorf("owner_id Expr = %q, want the core field's %q", field.Expr, colOwnerID)
	}
}

// cf_* names cannot collide with a core field, but the guarantee is worth a test
// rather than a convention: a collision would silently shadow a core column.
func TestNoCoreFieldNameCanBeACustomColumnName(t *testing.T) {
	for resource, engine := range segmentEngines {
		for name := range engine.Fields {
			if strings.HasPrefix(name, "cf_") {
				t.Errorf("%s's core vocabulary declares %q, which a custom column could shadow", resource, name)
			}
		}
	}
}

// Derived from the port's closed set, not from this file's own map: a seventh
// catalog type fails here rather than shipping as a column nobody can filter
// on. Mapping alone is not enough — a type that resolved to a FieldType the
// predicate engine admits no operator for would sit in the vocabulary and
// refuse every filter written against it — so each mapping is compiled, with
// `exists`, the one operator every type in the matrix carries.
func TestEveryCustomFieldTypeIsFilterable(t *testing.T) {
	for _, declared := range []string{
		fieldcatalog.TypeText, fieldcatalog.TypeNumber, fieldcatalog.TypeDate,
		fieldcatalog.TypeCurrency, fieldcatalog.TypePicklist, fieldcatalog.TypeBoolean,
	} {
		field, ok := customField(fieldcatalog.Column{Name: "cf_probe", Type: declared})
		if !ok {
			t.Errorf("custom-field type %q has no filter type, so a column of that type is unfilterable", declared)
			continue
		}
		_, err := storekit.CompilePredicate(
			storekit.Predicate{Field: "cf_probe", Op: storekit.OpExists, Value: true},
			map[string]storekit.Field{"cf_probe": field},
			func(any) int { return 1 },
		)
		if err != nil {
			t.Errorf("custom-field type %q maps to %q, which admits no filter: %v", declared, field.Type, err)
		}
	}
}

// The whole point of omitting rather than failing: one column this engine has
// no operators for must cost that column its filter and nothing else. Failing
// cost the entire resolution, so list validation, membership evaluation and
// filtered export all answered 500 for the record type — including for lists
// that never named the field.
func TestAnUnmappableCustomColumnCostsOnlyItself(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {
			{Name: "cf_known", Type: fieldcatalog.TypeText},
			{Name: "cf_from_the_future", Type: "geo"},
		},
	}})

	engine, ok, err := store.SegmentEngine(context.Background(), "person")

	if err != nil || !ok {
		t.Fatalf("one unmappable column broke the whole resolution: ok=%v err=%v", ok, err)
	}
	if _, present := engine.Fields["cf_known"]; !present {
		t.Error("the mappable sibling column was lost with it")
	}
	if _, present := engine.Fields["owner_id"]; !present {
		t.Error("the core vocabulary was lost with it")
	}
	if _, present := engine.Fields["cf_from_the_future"]; present {
		t.Error("the unmappable column entered the vocabulary, where it can only refuse every operator")
	}
}

// Omission is not silence. A predicate that actually NAMES the omitted field is
// refused by name — so a saved segment on a field that stopped being mappable
// says so, rather than quietly matching a different set of rows.
func TestAPredicateOnAnOmittedColumnIsRefusedByName(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_from_the_future", Type: "geo"}},
	}})
	engine, _, err := store.SegmentEngine(context.Background(), "person")
	if err != nil {
		t.Fatalf("segmentEngine: %v", err)
	}

	_, err = storekit.CompilePredicate(
		storekit.Predicate{Field: "cf_from_the_future", Op: storekit.OpEq, Value: "x"},
		engine.Fields,
		func(any) int { return 1 },
	)

	var perr *storekit.PredicateError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a PredicateError naming the field", err)
	}
	if perr.Code != storekit.CodeFilterFieldNotAllowed {
		t.Errorf("code = %q, want %q", perr.Code, storekit.CodeFilterFieldNotAllowed)
	}
	if perr.Field != "cf_from_the_future" {
		t.Errorf("field = %q, want the offending column named", perr.Field)
	}
}
