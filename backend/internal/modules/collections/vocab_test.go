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

// The ownership dial is ONE dial, so an engine that offers half of it offers a
// broken one: a saved view can say "owned by this person" and a manager's view
// — "owned by my team" — is the form that actually gets saved.
//
// Derived from the engines themselves rather than from a written-out list of
// resources, so a sixth engine that carries `owner_id` cannot quietly ship
// without the team half.
func TestEveryEngineWithAnOwnerAlsoFiltersByTeam(t *testing.T) {
	checked := 0
	for resource, engine := range segmentEngines {
		_, owned := engine.Fields[ownerIDField]
		team, teamed := engine.Fields[ownerTeamIDField]
		// Both directions. An engine offering the team half without the person
		// half is equally half a dial, and asserting only the one direction would
		// let that ship.
		if owned && !teamed {
			t.Errorf("%s filters by owner and not by owner's team: half an ownership dial", resource)
			continue
		}
		if teamed && !owned {
			t.Errorf("%s filters by owner's team and not by owner: half an ownership dial", resource)
			continue
		}
		if !owned {
			continue
		}
		checked++
		// Definitionally identical to the shared leaf, which is the enforceable
		// invariant: whatever the join is, every engine runs the same one. This
		// cannot tell a copy from the original — storekit.Field is a comparable
		// struct of three strings, so an identical literal passes — and catching
		// the copy itself would be a lint job, not an assertion.
		if team != ownerTeamField {
			t.Errorf("%s's team leaf differs from the shared one: %+v", resource, team)
		}
	}
	// Without this the loop asserts nothing the day ownerIDField stops matching
	// any engine's key, and the test keeps passing over an empty sweep.
	if checked == 0 {
		t.Fatal("no engine carries an owner leaf, so this gate checked nothing")
	}
}

// Every link template takes exactly one comparison. A template with any other
// number of verbs formats wrong — fmt writes %!(EXTRA …) or a bare %!s(MISSING)
// straight into the query text — so this is a syntax error waiting on whichever
// leaf a filter happens to name.
//
// Swept over every engine's every field rather than asserted per leaf, so a new
// link field is covered the day it is added.
func TestEveryLinkTemplateTakesExactlyOneComparison(t *testing.T) {
	swept := 0
	for resource, engine := range segmentEngines {
		for name, field := range engine.Fields {
			if field.Link == "" {
				continue
			}
			swept++
			if count := strings.Count(field.Link, "%s"); count != 1 {
				t.Errorf("%s.%s has %d %%s verbs in its link template, want exactly 1: %q",
					resource, name, count, field.Link)
			}
			if field.Expr == "" {
				t.Errorf("%s.%s is a link leaf with no column to compare inside it", resource, name)
			}
		}
	}
	if swept == 0 {
		t.Fatal("no link fields found, so this gate checked nothing")
	}
}

// The team leaf reaches the same rows the record lists already answer for
// `owner_team_id`, and reaches them the way the link mechanism can carry.
func TestTheTeamLeafJoinsMembershipOnTheOwnerColumn(t *testing.T) {
	if ownerTeamField.Link == "" {
		t.Fatal("the team leaf is not a link leaf, so it cannot reach team_membership")
	}
	for _, want := range []string{"team_membership", "tm.user_id = " + colOwnerID} {
		if !strings.Contains(ownerTeamField.Link, want) {
			t.Errorf("the team leaf does not carry %q: %q", want, ownerTeamField.Link)
		}
	}
	if count := strings.Count(ownerTeamField.Link, "%s"); count != 1 {
		t.Errorf("the team leaf has %d %%s verbs, want exactly 1: %q", count, ownerTeamField.Link)
	}
	// An id, so a malformed team fails validation (422) rather than execution.
	if ownerTeamField.Type != storekit.FieldID {
		t.Errorf("the team leaf is typed %s, want id", ownerTeamField.Type)
	}
}

// A picklist leaf compares text, so a value outside the contract's enum compiles
// and selects nothing. The list parameter for the same fact 422s a typo instead,
// and that divergence is deliberate rather than overlooked: the engine holds no
// per-field enum, and every picklist leaf here behaves this way — status,
// lifecycle, size_band, forecast_category, phase and the custom ones alike.
//
// Gated rather than explained, because a comment claiming it cannot notice the
// day someone adds validation to one leaf and leaves the rest.
func TestAPicklistLeafComparesAnUnrecognisedValueRatherThanRefusingIt(t *testing.T) {
	engine, ok, err := (&Store{}).SegmentEngine(context.Background(), "organization")
	if err != nil || !ok {
		t.Fatalf("segmentEngine: ok=%v err=%v", ok, err)
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql, err := storekit.CompilePredicate(
		storekit.Predicate{Field: "relationship_type", Op: storekit.OpEq, Value: "custmer"},
		engine.Fields, arg,
	)
	if err != nil {
		t.Fatalf("an unrecognised picklist value was refused: %v", err)
	}
	if len(args) != 1 || args[0] != "custmer" {
		t.Errorf("the value did not travel as a bind parameter: %#v", args)
	}
	// The typo reaches SQL as a parameter, never as text — which is what makes
	// "selects nothing" safe rather than merely unhelpful.
	if strings.Contains(sql, "custmer") {
		t.Errorf("the operand was inlined into the query text: %q", sql)
	}
}

// An account's relationship to us is multi-valued, and a withdrawn one is a fact
// it no longer carries.
func TestTheRelationshipLeafExcludesWithdrawnRows(t *testing.T) {
	field, ok := segmentEngines["organization"].Fields["relationship_type"]
	if !ok {
		t.Fatal("organizations cannot be filtered by relationship type")
	}
	if !strings.Contains(field.Link, "rt.archived_at IS NULL") {
		t.Errorf("the relationship leaf keeps selecting on withdrawn rows: %q", field.Link)
	}
	if !strings.Contains(field.Link, "rt.organization_id = t.id") {
		t.Errorf("the relationship leaf does not correlate to the account: %q", field.Link)
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
	// retired names columns present in cols that an admin has retired: still
	// filterable, no longer active. Spelled as the difference rather than as a
	// second column list, because that IS the difference the real catalogue's two
	// readers express — and a stub that let the two sets disagree arbitrarily
	// could describe a state the catalogue cannot produce.
	retired map[string]bool
	err     error
}

func (s stubFilterable) FilterableColumns(_ context.Context, object string) ([]fieldcatalog.Column, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cols[object], nil
}

func (s stubFilterable) ActiveColumns(_ context.Context, object string) ([]fieldcatalog.Column, error) {
	if s.err != nil {
		return nil, s.err
	}
	active := make([]fieldcatalog.Column, 0, len(s.cols[object]))
	for _, column := range s.cols[object] {
		if !s.retired[column.Name] {
			active = append(active, column)
		}
	}
	return active, nil
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

// Derived from the port's own closed set, never a copy of it: a list restated
// here would pass unchanged the day a seventh type is added to fieldcatalog,
// which is the one moment this test exists to fail.
//
// Mapping alone is not enough either — a type that resolved to a FieldType the
// predicate engine admits no operator for would sit in the vocabulary and
// refuse every filter written against it — so each mapping is compiled, with
// `exists`, the one operator every type in the matrix carries.
func TestEveryCustomFieldTypeIsFilterable(t *testing.T) {
	for _, declared := range fieldcatalog.Types() {
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
// no operators for costs that column its filter and nothing else. A refusal
// costs the entire resolution instead — list validation, membership evaluation
// and filtered export all fail for the record type, including for lists that
// never name the field.
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
