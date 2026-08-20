// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The core picklist value sets against the contract that owns them.
//
// A filter builder offers a picklist's values instead of asking a reader to type
// one, and the sets it offers are declared beside the engine (collections'
// vocab.go) while the contract enumerates them in api/crm.yaml. Drift between the
// two is silent and runs both ways: a value the contract dropped stays on offer
// and selects nothing, and a value it gained is one no builder shows — which is
// the free-text failure the whole surface exists to remove, arriving one enum
// member at a time.
//
// Here rather than in the module, for the reason the root's other contract gates
// carry: this walks the authoritative 3.1 document rather than the generated Go,
// so a contract-only edit cannot slip past it, and collections does not take a
// YAML dependency to hold a claim about a document it does not own.
//
// Reading the document also lets the null be dropped deliberately. oapi-codegen
// emits a `<nil>` member for a nullable enum (OrganizationSizeBandLessThannil is
// real), so a set derived from the generated constants would carry a value no
// human should be offered; a null in the contract is the COLUMN's nullability,
// and `exists: false` is how a filter asks for empty.
//
// The COMPLETENESS half — that every core picklist offers values at all, that a
// retired one offers none, and that no offered value is empty or `<nil>` — is
// collections' own TestEveryCorePicklistOffersItsValues. This holds the sets it
// finds to the document, which is why an empty set is skipped here rather than
// asserted twice.

import (
	"context"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// contractDocument is the OpenAPI document these sets mirror.
const contractDocument = "api/crm.yaml"

// Where each offered set lives in the contract, keyed `<resource>.<field>`.
//
// The property is named separately from the field because the two can differ: an
// account's relationship is `relationship_types` on the schema (an account may
// hold several) and `relationship_type` as a filter leaf, which selects accounts
// that hold AT LEAST the named one.
//
// A picklist with no entry here FAILS rather than being skipped, so a core
// picklist added tomorrow is covered the day it lands — by forcing whoever adds
// it to say where the contract admits its values.
var picklistInContract = map[string]struct{ schema, property string }{
	"organization.lifecycle":         {"Organization", "lifecycle"},
	"organization.size_band":         {"Organization", "size_band"},
	"organization.relationship_type": {"Organization", "relationship_types"},
	"deal.status":                    {"Deal", "status"},
	"deal.forecast_category":         {"Deal", "forecast_category"},
	"lead.status":                    {"Lead", "status"},
	"project.phase":                  {"Project", "phase"},
	// A deal filters on its customer's account through a link leaf, so two of the
	// sets above appear a second time under the deal engine. Both entries name the
	// Organization property they reach, which is the point: a link leaf offering a
	// different set from the field it reads is exactly the drift this catches.
	"deal.organization_lifecycle": {"Organization", "lifecycle"},
	"deal.organization_size_band": {"Organization", "size_band"},
}

func TestEveryOfferedPicklistMatchesTheContractsValues(t *testing.T) {
	doc := loadContractDocument(t)
	compared := 0
	for _, resource := range vocabularyResources(t, doc) {
		engine, ok, err := (&collections.Store{}).SegmentEngine(context.Background(), resource)
		if err != nil {
			t.Fatalf("segment engine for %s: %v", resource, err)
		}
		if !ok {
			t.Errorf("the contract admits resource %q and no engine serves it", resource)
			continue
		}
		for name, field := range engine.Fields {
			if field.Type != storekit.FieldPicklist || len(field.Options) == 0 {
				continue
			}
			where, declared := picklistInContract[resource+"."+name]
			if !declared {
				t.Errorf("%s.%s offers values and nothing says where the contract admits them, so nothing holds the two together", resource, name)
				continue
			}
			compared++
			compareValueSets(t, resource+"."+name, field.Options, contractEnum(t, doc, where.schema, where.property))
		}
	}
	if compared == 0 {
		t.Fatal("no picklist was compared, so this gate checked nothing")
	}
}

// compareValueSets reports each direction separately, because the two failures
// read nothing alike to whoever has to fix them.
func compareValueSets(t *testing.T, field string, offered []string, admitted map[string]bool) {
	t.Helper()
	offers := map[string]bool{}
	for _, value := range offered {
		offers[value] = true
		if !admitted[value] {
			t.Errorf("%s offers %q and the contract does not admit it, so picking it composes a clause that selects nothing", field, value)
		}
	}
	for value := range admitted {
		if !offers[value] {
			t.Errorf("%s: the contract admits %q and the field does not offer it, so a builder cannot compose a clause the engine accepts", field, value)
		}
	}
	if len(admitted) == 0 {
		t.Errorf("%s: the contract enum read as empty, so this compared nothing", field)
	}
}

// vocabularyResources reads the resources GET /filters/vocabulary admits, so a
// sixth one is swept by existing rather than by being added to a list here.
func vocabularyResources(t *testing.T, doc map[string]any) []string {
	t.Helper()
	operation := descendContract(t, doc, "paths", "/filters/vocabulary", "get")
	params, ok := operation["parameters"].([]any)
	if !ok {
		t.Fatal("GET /filters/vocabulary declares no parameters, so the resource set is unreadable")
	}
	for _, raw := range params {
		param, isMap := raw.(map[string]any)
		if !isMap || param["name"] != "resource" {
			continue
		}
		schema, isMap := param["schema"].(map[string]any)
		if !isMap {
			t.Fatal("the resource parameter carries no schema")
		}
		return enumStrings(t, schema, "the resource parameter")
	}
	t.Fatal("GET /filters/vocabulary declares no resource parameter")
	return nil
}

// contractEnum answers one property's enum, minus null.
//
// An array-typed property (relationship_types) keeps its enum on `items`, so both
// spellings are read: the admitted values are the same set either way, and which
// one a schema uses is not this gate's business.
func contractEnum(t *testing.T, doc map[string]any, schema, property string) map[string]bool {
	t.Helper()
	node := descendContract(t, doc, "components", "schemas", schema, "properties", property)
	if _, direct := node["enum"]; !direct {
		items, isMap := node["items"].(map[string]any)
		if !isMap {
			t.Fatalf("%s.%s has neither an enum nor items to read one from", schema, property)
		}
		node = items
	}
	admitted := map[string]bool{}
	for _, value := range enumStrings(t, node, schema+"."+property) {
		admitted[value] = true
	}
	return admitted
}

// enumStrings reads a schema node's enum as strings. A null member is dropped —
// it is the column's nullability, not a value to offer — and anything else
// non-string is named rather than skipped, since it would be a contract shape
// this gate cannot read.
func enumStrings(t *testing.T, node map[string]any, subject string) []string {
	t.Helper()
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatalf("%s carries no enum", subject)
	}
	var values []string
	for _, member := range raw {
		text, isText := member.(string)
		if !isText {
			if member != nil {
				t.Errorf("%s enum carries a non-string member %#v", subject, member)
			}
			continue
		}
		values = append(values, text)
	}
	return values
}

func descendContract(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	node := doc
	for _, key := range path {
		next, ok := node[key].(map[string]any)
		if !ok {
			t.Fatalf("the contract has no %s under %v", key, path)
		}
		node = next
	}
	return node
}

func loadContractDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(contractDocument)
	if err != nil {
		t.Fatalf("reading %s: %v", contractDocument, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", contractDocument, err)
	}
	return doc
}
