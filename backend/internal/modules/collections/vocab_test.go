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
