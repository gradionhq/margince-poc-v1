// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package schema builds JSON Schema values for constraining structured model
// output (the model.Request.ResponseSchema field). Callers compose
// Object/Array/String/… and render with Must instead of hand-writing a JSON
// string, so every structured-output schema is compile-checked, always valid
// JSON, and built one way across the codebase.
//
// Object produces a CLOSED object (additionalProperties:false) with no
// numeric/string constraints — the strictest common denominator across the
// model provider adapters (Anthropic rejects numeric bounds; strict
// json_schema requires closed objects; Ollama/vLLM accept the same shape).
// Value-range and cross-field checks belong in the caller's own validation of
// the result, never in the schema, which only pins the SHAPE at generation.
package schema

import "encoding/json"

// Node is one JSON Schema node. The zero value is not useful; build nodes with
// the constructors below.
type Node struct {
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	// additionalProperties is JSON Schema's spec keyword — camelCase by the
	// spec, not a style choice; snake_case would be an invalid keyword.
	AdditionalProperties *bool           `json:"additionalProperties,omitempty"` //nolint:tagliatelle // JSON Schema spec keyword, must be camelCase
	Properties           map[string]Node `json:"properties,omitempty"`
	Items                *Node           `json:"items,omitempty"`
	Required             []string        `json:"required,omitempty"`
}

// Describe attaches the JSON Schema `description` annotation — guidance the
// model reads for what a field means. It returns a copy so it chains onto any
// node: schema.String().Describe("the customer's full legal name"). All
// supported providers (Ollama, vLLM, Anthropic) accept the standard keyword.
func (n Node) Describe(desc string) Node {
	n.Description = desc
	return n
}

// The four Node.Type values this package's vocabulary spans — shared with
// validate.go's ValidateJSON so the builder and the validator never drift
// on the type-name strings they switch over.
const (
	typeObject = "object"
	typeArray  = "array"
	typeString = "string"
	typeNumber = "number"
)

// String is a JSON string leaf.
func String() Node { return Node{Type: typeString} }

// Number is a JSON number leaf (integer or float).
func Number() Node { return Node{Type: typeNumber} }

// Array is a list whose every item matches items.
func Array(items Node) Node { return Node{Type: typeArray, Items: &items} }

// Enum is a string leaf constrained to one of values (JSON Schema `enum`).
// All supported providers constrain generation to the given set.
func Enum(values ...string) Node { return Node{Type: typeString, Enum: values} }

// Object is a closed object: props are its properties and required names the
// ones that must be present (typically all of them, for extraction).
func Object(props map[string]Node, required ...string) Node {
	closed := false
	return Node{Type: typeObject, AdditionalProperties: &closed, Properties: props, Required: required}
}

// Must renders a node to the wire bytes for ResponseSchema. It panics only on
// a programmer error — a Node cannot fail to marshal — so it is safe in a
// package-level var initializer.
func Must(n Node) json.RawMessage {
	raw, err := json.Marshal(n)
	if err != nil {
		panic("schema: rendering node: " + err.Error())
	}
	return raw
}
