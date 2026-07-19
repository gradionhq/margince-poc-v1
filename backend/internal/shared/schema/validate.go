// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package schema

// ValidateJSON checks that value parses as JSON conforming to schemaJSON.
// This is deliberately NOT a general JSON Schema implementation: it walks
// only the restricted vocabulary Object/Array/String/Number/Enum builds
// (type, properties+required, additionalProperties, items, enum) — the one
// shape every provider adapter in this codebase can both emit a
// ResponseSchema for and produce output against. A corpus's json_schema
// check exercises exactly that shape, so a bespoke draft-07/2020-12
// validator would cover ground this codebase never uses. Extend the
// vocabulary here only alongside a new Node constructor above.
import (
	"encoding/json"
	"fmt"
	"slices"
)

// ValidateJSON parses schemaJSON into a Node (the same wire shape Must
// renders) and reports the first way value's parsed JSON diverges from it.
// A nil error means value is valid JSON conforming to the schema.
func ValidateJSON(schemaJSON json.RawMessage, value string) error {
	var node Node
	if err := json.Unmarshal(schemaJSON, &node); err != nil {
		return fmt.Errorf("schema: parsing schema: %w", err)
	}
	var data any
	if err := json.Unmarshal([]byte(value), &data); err != nil {
		return fmt.Errorf("schema: value is not valid JSON: %w", err)
	}
	return node.validate(data, "$")
}

// validate checks v against n at path (a dotted/bracketed JSON-pointer-ish
// trail used only for the error message, e.g. "$.facts[2].name").
func (n Node) validate(v any, path string) error {
	switch n.Type {
	case typeObject:
		return n.validateObject(v, path)
	case typeArray:
		return n.validateArray(v, path)
	case typeString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: want string, got %T", path, v)
		}
		if len(n.Enum) > 0 && !slices.Contains(n.Enum, s) {
			return fmt.Errorf("%s: %q is not one of %v", path, s, n.Enum)
		}
		return nil
	case typeNumber:
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s: want number, got %T", path, v)
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, n.Type)
	}
}

func (n Node) validateObject(v any, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: want object, got %T", path, v)
	}
	for _, req := range n.Required {
		if _, present := obj[req]; !present {
			return fmt.Errorf("%s: missing required property %q", path, req)
		}
	}
	if n.AdditionalProperties != nil && !*n.AdditionalProperties {
		for key := range obj {
			if _, known := n.Properties[key]; !known {
				return fmt.Errorf("%s: unexpected property %q", path, key)
			}
		}
	}
	for key, sub := range n.Properties {
		val, present := obj[key]
		if !present {
			continue // absence of an optional property is not itself a violation
		}
		if err := sub.validate(val, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func (n Node) validateArray(v any, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s: want array, got %T", path, v)
	}
	if n.Items == nil {
		return nil
	}
	for i, item := range arr {
		if err := n.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}
