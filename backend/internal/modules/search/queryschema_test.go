// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// readSchema fetches and decodes the published document for one caller.
func readSchema(ctx context.Context, t *testing.T, resource *QuerySchemaResource) querySchemaDoc {
	t.Helper()
	contents, err := resource.ReadResource(ctx, QuerySchemaURI)
	if err != nil {
		t.Fatal(err)
	}
	if contents.MIMEType != "application/json" || contents.URI != QuerySchemaURI {
		t.Fatalf("resource came back as %+v", contents)
	}
	var doc querySchemaDoc
	if err := json.Unmarshal([]byte(contents.Text), &doc); err != nil {
		t.Fatalf("published document is not JSON: %v", err)
	}
	return doc
}

// SEARCH-AC-17's other half: the operator is DECLARED in the published
// vocabulary, with the code it answers. Omitting it would send a caller to a
// text match on a city name, which quietly returns a different answer.
func TestThePublishedVocabularyDeclaresWithinRadiusAsUnavailable(t *testing.T) {
	doc := readSchema(readerFor("organization"), t, NewQuerySchemaResource(NewVocabularyResolver()))

	i := slices.IndexFunc(doc.Unavailable, func(u querySchemaUnavailable) bool { return u.Op == OpWithinRadius })
	if i < 0 {
		t.Fatalf("%q is not declared unavailable; a caller cannot tell it apart from an operator that works", OpWithinRadius)
	}
	if doc.Unavailable[i].Answers != CodeDistanceRankingUnavailable {
		t.Errorf("the declaration says it answers %q; want %q", doc.Unavailable[i].Answers, CodeDistanceRankingUnavailable)
	}

	org := targetIn(t, doc, "organization")
	place := slices.IndexFunc(org.Fields, func(f querySchemaField) bool { return f.Name == "address" })
	if place < 0 || !slices.Contains(org.Fields[place].Ops, OpWithinRadius) {
		t.Error("the operator is declared unavailable but no field publishes it, so it can never be reached")
	}
	city := slices.IndexFunc(org.Fields, func(f querySchemaField) bool { return f.Name == "address.city" })
	if city < 0 || !slices.Contains(org.Fields[city].Ops, OpEq) {
		t.Error("city is not published as an exact predicate; it works today and must say so")
	}
}

func targetIn(t *testing.T, doc querySchemaDoc, name string) querySchemaTarget {
	t.Helper()
	i := slices.IndexFunc(doc.Targets, func(target querySchemaTarget) bool { return target.Target == name })
	if i < 0 {
		t.Fatalf("published document has no %q target", name)
	}
	return doc.Targets[i]
}

// The document is composed per caller, from the same resolver the validator
// uses — so what it advertises is exactly what a plan may say, and it never
// advertises a record type the caller cannot read.
func TestThePublishedVocabularyIsComposedPerCaller(t *testing.T) {
	resource := NewQuerySchemaResource(NewVocabularyResolver())

	wide := readSchema(readerFor("deal", "organization"), t, resource)
	if len(wide.Targets) != 2 {
		t.Fatalf("a caller reading two record types sees %d targets", len(wide.Targets))
	}

	narrow := readSchema(readerFor("organization"), t, resource)
	for _, target := range narrow.Targets {
		if target.Target == "deal" {
			t.Error("the published document advertises a record type the caller cannot read")
		}
	}
	for _, target := range narrow.Targets {
		for _, r := range target.Relations {
			if r.Target == "deal" {
				t.Errorf("%s publishes a hop into a record type the caller cannot read", target.Target)
			}
		}
	}
}

// Everything the document advertises is something a plan is actually allowed
// to say. The two come from one resolver, and this is the test that keeps
// them one: an advertised field the validator refuses is a refusal that reads
// like a bug.
func TestEveryPublishedFieldAndOperatorValidates(t *testing.T) {
	ctx := readerFor("deal", "organization")
	doc := readSchema(ctx, t, NewQuerySchemaResource(NewVocabularyResolver()))
	validator := NewPlanValidator(NewVocabularyResolver())

	for _, target := range doc.Targets {
		for _, field := range target.Fields {
			for _, op := range field.Ops {
				plan := Plan{
					Version: PlanVersion, Target: target.Target,
					Where: []Predicate{operandFor(field, op)},
				}
				if _, err := validator.Validate(ctx, plan); err != nil {
					t.Errorf("%s.%s %s is published but refused: %v", target.Target, field.Name, op, err)
				}
			}
		}
		for _, relation := range target.Relations {
			plan := Plan{Version: PlanVersion, Target: target.Target, Traverse: &Traversal{Relation: relation.Name}}
			if _, err := validator.Validate(ctx, plan); err != nil {
				t.Errorf("%s hop %q is published but refused: %v", target.Target, relation.Name, err)
			}
		}
	}
}

// operandFor builds an operand of the shape a published field's kind takes.
func operandFor(field querySchemaField, op string) Predicate {
	operand := json.RawMessage(`"x"`)
	switch FieldKind(field.Kind) {
	case KindNumber:
		operand = json.RawMessage(`1`)
	case KindBoolean:
		operand = json.RawMessage(`true`)
	case KindGeo:
		operand = json.RawMessage(`{"center":"Stuttgart","radius_km":50}`)
	case KindText, KindID, KindDate, KindTimestamp:
	}
	if op == OpIn {
		return Predicate{Field: field.Name, Op: op, Values: json.RawMessage("[" + string(operand) + "]")}
	}
	return Predicate{Field: field.Name, Op: op, Value: operand}
}

// An unknown URI is not found — the same answer a URI the caller cannot see
// gets, so the resource surface hides existence exactly as the record surface
// does.
func TestAnUnknownResourceURIIsNotFound(t *testing.T) {
	_, err := NewQuerySchemaResource(NewVocabularyResolver()).
		ReadResource(readerFor("deal"), "margince://schema/something-else")
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("unknown URI answered %v; want ErrNotFound", err)
	}
}

// The catalogue advertises the document with everything a client needs to
// decide whether to fetch it.
func TestTheResourceIsAdvertisedWithItsIdentity(t *testing.T) {
	published := NewQuerySchemaResource(NewVocabularyResolver()).Resources(readerFor("deal"))
	if len(published) != 1 {
		t.Fatalf("the search module publishes %d resources; want 1", len(published))
	}
	r := published[0]
	if r.URI != QuerySchemaURI || r.Name == "" || r.Title == "" || r.Description == "" || r.MIMEType == "" {
		t.Errorf("resource advertised incompletely: %+v", r)
	}
}

// The workspace's own columns are published too, so a caller never has to
// discover a custom field by guessing at it.
func TestTheWorkspacesOwnColumnsArePublished(t *testing.T) {
	catalog := stubCatalog{columns: map[string][]fieldcatalog.Column{
		"deal": {{Name: "cf_renewal_risk", Type: fieldcatalog.TypePicklist}},
	}}
	doc := readSchema(readerFor("deal"), t,
		NewQuerySchemaResource(NewVocabularyResolver().WithFieldCatalog(catalog)))

	deal := targetIn(t, doc, "deal")
	if !slices.ContainsFunc(deal.Fields, func(f querySchemaField) bool { return f.Name == "cf_renewal_risk" }) {
		t.Error("an active custom field is askable but not published, so a caller can only find it by guessing")
	}
}

// A catalog fault refuses the read rather than publishing a silently smaller
// vocabulary — a document missing the fields it should carry teaches a caller
// a surface that is not there.
func TestACatalogFaultRefusesThePublishedRead(t *testing.T) {
	boom := errors.New("catalog unavailable")
	_, err := NewQuerySchemaResource(NewVocabularyResolver().WithFieldCatalog(stubCatalog{err: boom})).
		ReadResource(readerFor("deal"), QuerySchemaURI)
	if !errors.Is(err, boom) {
		t.Fatalf("read returned %v; want the catalog fault", err)
	}
}

// The grammar section states the three things v1 admits, so the shape of a
// plan does not have to be inferred from the field lists.
func TestThePublishedDocumentStatesTheGrammar(t *testing.T) {
	doc := readSchema(readerFor("deal"), t, NewQuerySchemaResource(NewVocabularyResolver()))
	if doc.Version != PlanVersion {
		t.Errorf("published version is %q; want %q", doc.Version, PlanVersion)
	}
	for name, clause := range map[string]string{
		"predicates":      doc.Grammar.Predicates,
		"semantic target": doc.Grammar.SemanticTarget,
		"traversal":       doc.Grammar.Traversal,
		"limit":           doc.Grammar.Limit,
		"refusal":         doc.Grammar.Refusal,
	} {
		if clause == "" {
			t.Errorf("the published grammar says nothing about %s", name)
		}
	}
}
