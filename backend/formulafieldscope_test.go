// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The negative-scope half of the formula-field boundary proof (RD-AC-7): a
// formula field is a database-GENERATED artifact, never a runtime-authored
// one, so NO contract operation may accept a writable formula_sql in its
// request body — ComputedField.formula_sql (crm.yaml) is a response-only
// display field, never echoed back as an editable one. This walks the
// AUTHORITATIVE contract itself (api/crm.yaml, not the generated stubs), so
// a future ticket that bolts a "define computed field" endpoint onto the
// contract fails here before a single line of handler code exists.

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadContract parses the authoritative OpenAPI 3.1 document into a plain
// map tree — this test walks $ref/properties/items/allOf by hand rather
// than pulling in an OpenAPI-aware library, since the shapes it needs
// (requestBody → content → schema, and schema composition) are a handful
// of map lookups.
func loadContract(t *testing.T) map[string]any {
	t.Helper()
	src, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading api/crm.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parsing api/crm.yaml: %v", err)
	}
	return doc
}

// httpMethods are the OpenAPI path-item keys that carry an operation (and
// so may carry a requestBody); the other path-item keys (parameters,
// summary, description, …) are not operations.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// resolveSchemaRef follows one components/schemas $ref — the only $ref
// shape crm.yaml uses — to its schema map.
func resolveSchemaRef(doc map[string]any, ref string) (map[string]any, bool) {
	const prefix = "#/components/schemas/"
	name, ok := strings.CutPrefix(ref, prefix)
	if !ok {
		return nil, false
	}
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return nil, false
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return nil, false
	}
	schema, ok := schemas[name].(map[string]any)
	return schema, ok
}

// schemaWritesFormulaSQL walks one schema tree (following $ref, properties,
// array items, and allOf/oneOf/anyOf composition) and reports whether it
// reaches a "formula_sql" property that is not marked readOnly — i.e. one a
// client could set on write. seen breaks $ref cycles.
func schemaWritesFormulaSQL(doc map[string]any, schema map[string]any, seen map[string]bool) bool {
	if schema == nil {
		return false
	}
	if ref, ok := schema["$ref"].(string); ok {
		if seen[ref] {
			return false
		}
		seen[ref] = true
		resolved, ok := resolveSchemaRef(doc, ref)
		return ok && schemaWritesFormulaSQL(doc, resolved, seen)
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, propAny := range properties {
			prop, _ := propAny.(map[string]any)
			if name == "formula_sql" {
				if readOnly, _ := prop["readOnly"].(bool); readOnly {
					continue
				}
				return true
			}
			if schemaWritesFormulaSQL(doc, prop, seen) {
				return true
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if schemaWritesFormulaSQL(doc, items, seen) {
			return true
		}
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		list, ok := schema[key].([]any)
		if !ok {
			continue
		}
		for _, itemAny := range list {
			if item, ok := itemAny.(map[string]any); ok && schemaWritesFormulaSQL(doc, item, seen) {
				return true
			}
		}
	}
	return false
}

// TestContract_noOperationWritesFormulaSQL is the RD-AC-7 fitness function:
// no path operation's request body may carry a writable formula_sql
// property, derived from the live contract rather than a point check on
// today's operation list — an operation added tomorrow is covered for free.
func TestContract_noOperationWritesFormulaSQL(t *testing.T) {
	doc := loadContract(t)
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("api/crm.yaml has no top-level paths map — the contract failed to parse as expected")
	}
	// Vacuous-pass guard: the contract has dozens of paths; finding almost
	// none means this test stopped seeing the real document, not that the
	// API shrank.
	if len(paths) < 30 {
		t.Fatalf("found only %d paths in api/crm.yaml — the contract walk no longer sees the schema", len(paths))
	}

	checked := 0
	for path, methodsAny := range paths {
		methods, ok := methodsAny.(map[string]any)
		if !ok {
			continue
		}
		for method, opAny := range methods {
			if !httpMethods[method] {
				continue
			}
			op, ok := opAny.(map[string]any)
			if !ok {
				continue
			}
			reqBody, ok := op["requestBody"].(map[string]any)
			if !ok {
				continue
			}
			content, ok := reqBody["content"].(map[string]any)
			if !ok {
				continue
			}
			for _, mediaAny := range content {
				media, ok := mediaAny.(map[string]any)
				if !ok {
					continue
				}
				schema, ok := media["schema"].(map[string]any)
				if !ok {
					continue
				}
				checked++
				if schemaWritesFormulaSQL(doc, schema, map[string]bool{}) {
					t.Errorf("%s %s: request body schema accepts a writable formula_sql — formula fields are DATABASE-GENERATED only (RD-AC-7); no operation may author or edit one", strings.ToUpper(method), path)
				}
			}
		}
	}
	if checked < 30 {
		t.Fatalf("found only %d request-body schemas in api/crm.yaml — the contract walk no longer sees the schema", checked)
	}
}

// TestSchemaWritesFormulaSQL_detectsAWritableProperty is a self-test of the
// detector TestContract_noOperationWritesFormulaSQL relies on: without it, a
// detector that always returned false would make the fitness function
// vacuously green forever. It exercises schemaWritesFormulaSQL directly
// against a synthetic schema shaped like a hypothetical future
// "define/edit computed field" request body — proving the gate actually
// fires the moment such an operation is added, not just that today's
// contract happens to lack one.
func TestSchemaWritesFormulaSQL_detectsAWritableProperty(t *testing.T) {
	doc := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"ComputedField": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key":         map[string]any{"type": "string"},
						"formula_sql": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	writable := map[string]any{"$ref": "#/components/schemas/ComputedField"}
	if !schemaWritesFormulaSQL(doc, writable, map[string]bool{}) {
		t.Fatal("schemaWritesFormulaSQL did not detect a writable formula_sql reached through a $ref — the RD-AC-7 gate would not fire on a future authoring op")
	}

	readOnlyDoc := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"ComputedField": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"formula_sql": map[string]any{"type": "string", "readOnly": true},
					},
				},
			},
		},
	}
	if schemaWritesFormulaSQL(readOnlyDoc, writable, map[string]bool{}) {
		t.Fatal("schemaWritesFormulaSQL flagged a formula_sql property explicitly marked readOnly — it should only fire on a genuinely writable one")
	}
}
