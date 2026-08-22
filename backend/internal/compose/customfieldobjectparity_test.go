// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The objects a custom field may attach to are declared twice: the engine's
// own allowlist (customfields.FieldObjects) decides what the store accepts,
// and the contract's `object` enums tell a client what it may send. The two
// drifted once — the contract advertised `activity`, which the store refuses,
// and withheld `project`, which it serves — and nothing noticed, because the
// unit lane compares neither against the other. This does, on every place the
// contract spells the enum: the record, the create body, and the list filter.

import (
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
)

// customFieldObjectEnums reads every `object` enum the contract declares for a
// custom field, keyed by where it sits.
func customFieldObjectEnums(t *testing.T) map[string][]string {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(contractFile)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	out := map[string][]string{}
	for _, schemaName := range []string{"CustomField", "CreateCustomFieldRequest"} {
		schema := doc.Components.Schemas[schemaName]
		if schema == nil || schema.Value == nil {
			t.Fatalf("%s is absent from the contract", schemaName)
		}
		prop := schema.Value.Properties["object"]
		if prop == nil || prop.Value == nil {
			t.Fatalf("%s has no object property", schemaName)
		}
		out[schemaName+".object"] = enumStrings(t, schemaName, prop.Value.Enum)
	}
	list := doc.Paths.Find("/custom-fields")
	if list == nil || list.Get == nil {
		t.Fatal("GET /custom-fields is absent from the contract")
	}
	for _, param := range list.Get.Parameters {
		if param.Value != nil && param.Value.Name == "object" {
			out["listCustomFields?object"] = enumStrings(t, "listCustomFields", param.Value.Schema.Value.Enum)
		}
	}
	return out
}

func enumStrings(t *testing.T, where string, raw []any) []string {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("%s declares object without a closed enum, so a client cannot know what it may send", where)
	}
	values := make([]string, 0, len(raw))
	for _, member := range raw {
		value, ok := member.(string)
		if !ok {
			t.Fatalf("%s has a non-string enum member %#v", where, member)
		}
		values = append(values, value)
	}
	return values
}

func TestEveryContractCustomFieldObjectEnumIsExactlyTheEngineAllowlist(t *testing.T) {
	want := slices.Sorted(slices.Values(customfields.FieldObjects))
	enums := customFieldObjectEnums(t)
	if len(enums) != 3 {
		t.Fatalf("found %d object enums in the contract, want the record, the create body and the list filter", len(enums))
	}
	for where, got := range enums {
		if sorted := slices.Sorted(slices.Values(got)); !slices.Equal(sorted, want) {
			t.Errorf("%s declares %v, the engine accepts %v — a client is promised an object the store "+
				"refuses, or refused one it serves", where, sorted, want)
		}
	}
}
