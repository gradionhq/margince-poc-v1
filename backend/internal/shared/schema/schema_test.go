// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// decode renders a node and unmarshals it to a generic map so assertions read
// keys directly — no tagged struct, so the JSON Schema camelCase keywords stay
// literal here rather than fighting the snake_case tag linter.
func decode(t *testing.T, n schema.Node) map[string]any {
	t.Helper()
	raw := schema.Must(n)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Must produced invalid JSON: %v (%s)", err, raw)
	}
	return m
}

func TestObjectIsClosedWithItsRequiredAndProperties(t *testing.T) {
	m := decode(t, schema.Object(
		map[string]schema.Node{"name": schema.String(), "age": schema.Number()},
		"name",
	))

	if m["type"] != "object" {
		t.Fatalf("type = %v, want object", m["type"])
	}
	// A closed object is the whole point: additionalProperties must be present
	// and explicitly false, not omitted.
	ap, ok := m["additionalProperties"]
	if !ok || ap != false {
		t.Fatalf("additionalProperties = %v (present=%v), want explicit false", ap, ok)
	}
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Fatalf("missing property name: %v", props)
	}
	req, _ := m["required"].([]any)
	if len(req) != 1 || req[0] != "name" {
		t.Fatalf("required = %v, want [name]", req)
	}
}

func TestScalarLeavesMarshalToTheirType(t *testing.T) {
	cases := map[string]schema.Node{
		"string": schema.String(),
		"number": schema.Number(),
	}
	for want, node := range cases {
		m := decode(t, node)
		if m["type"] != want {
			t.Errorf("%s leaf marshalled type = %v", want, m["type"])
		}
		if _, extra := m["additionalProperties"]; extra {
			t.Errorf("%s leaf carried additionalProperties", want)
		}
	}
}

func TestEnumIsAStringLeafConstrainedToItsValues(t *testing.T) {
	m := decode(t, schema.Enum("new", "working", "won"))
	if m["type"] != "string" {
		t.Fatalf("enum type = %v, want string", m["type"])
	}
	vals, ok := m["enum"].([]any)
	if !ok || len(vals) != 3 || vals[0] != "new" || vals[2] != "won" {
		t.Fatalf("enum values wrong: %v", m["enum"])
	}
	// A non-enum leaf must omit the key.
	if _, present := decode(t, schema.String())["enum"]; present {
		t.Fatal("plain string leaf emitted an enum key")
	}
}

func TestDescribeAttachesTheDescriptionAnnotation(t *testing.T) {
	m := decode(t, schema.String().Describe("the customer's full legal name"))
	if m["description"] != "the customer's full legal name" {
		t.Fatalf("description = %v", m["description"])
	}
	// A node without Describe must omit the key entirely.
	if _, present := decode(t, schema.String())["description"]; present {
		t.Fatal("undescribed node emitted a description key")
	}
}

func TestArrayOfClosedObjectKeepsTheItemObjectClosed(t *testing.T) {
	// The shipped shape: an array whose items are closed objects. Pin that the
	// nested object still emits additionalProperties:false end-to-end.
	m := decode(t, schema.Array(schema.Object(
		map[string]schema.Node{"k": schema.String()}, "k",
	)))
	items, ok := m["items"].(map[string]any)
	if !ok {
		t.Fatalf("items missing: %v", m)
	}
	if ap, ok := items["additionalProperties"]; !ok || ap != false {
		t.Fatalf("nested object item not closed: %v", items)
	}
}

func TestArrayCarriesItemSchemaAndScalarsHaveNoExtraKeys(t *testing.T) {
	m := decode(t, schema.Array(schema.Number()))

	if m["type"] != "array" {
		t.Fatalf("type = %v, want array", m["type"])
	}
	items, ok := m["items"].(map[string]any)
	if !ok || items["type"] != "number" {
		t.Fatalf("items wrong: %v", m["items"])
	}
	// A scalar leaf must not carry additionalProperties — that keyword is only
	// meaningful (and only accepted by strict providers) on objects.
	if _, present := items["additionalProperties"]; present {
		t.Fatalf("scalar leaf carried additionalProperties: %v", items)
	}
}
