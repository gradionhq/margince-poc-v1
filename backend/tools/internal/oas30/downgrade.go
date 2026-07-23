// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package oas30 downgrades an OpenAPI 3.1 document into the 3.0.3 subset
// oapi-codegen's kin-openapi backend consumes — the ONE transform shared by
// contract-overlay (the codegen-time CLI that writes the build-dir overlay)
// and gen-payloads (which re-derives the same 3.0-safe shape at generate
// time to diff against generated payload types), so the two pipelines can
// never disagree on what "3.0-safe" means:
//   - openapi: 3.1.x -> 3.0.3
//   - type: [T, 'null'] -> type: T + nullable: true
//   - schema-level examples: [x, …] -> example: x (3.0 has no plural form)
//   - const: X -> enum: [X] (3.0 has no const; the single-value enum is exact)
//
// Any OTHER 3.1-only construct with no faithful 3.0 equivalent
// (unsupported31Keywords) fails loudly rather than degrading silently —
// codegen must never emit a 3.0.3-labeled doc that quietly lost part of the
// schema. Data values (example/default/enum) pass through opaque: a member
// named "type" or "openapi" inside an example is data, not a keyword.
package oas30

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Bytes parses src as an OpenAPI 3.1 YAML document, downgrades it in place,
// and re-marshals it back to bytes.
func Bytes(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}
	if err := Node(&doc); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling downgraded doc: %w", err)
	}
	return out, nil
}

// unsupported31Keywords are JSON-Schema-2020-12 / OAS-3.1 schema keywords
// that OAS 3.0.3 (kin-openapi) cannot express. Left in place they would be
// SILENTLY dropped from the generated contract — a 3.0.3-labeled doc that
// quietly lost part of the payload schema — so the downgrade REFUSES rather
// than degrade silently. Extend this list as new 3.1 constructs appear; the
// point is that adding one to a source spec fails the build loudly, never
// passes a half-expressed contract into codegen.
var unsupported31Keywords = map[string]struct{}{
	"prefixItems": {}, "unevaluatedProperties": {}, "unevaluatedItems": {},
	"contains": {}, "minContains": {}, "maxContains": {},
	"if": {}, "then": {}, "else": {},
	"dependentSchemas": {}, "dependentRequired": {},
	"propertyNames": {}, "contentSchema": {},
	"$defs": {}, "$dynamicRef": {}, "$dynamicAnchor": {}, "$anchor": {}, "$vocabulary": {},
}

// arbitraryKeyContainers are mapping keys whose child mapping's keys are
// author-chosen NAMES (schema names, property names, status codes, media
// types, …), never schema keywords. The 3.1-keyword scan must NOT treat
// those child keys as keywords — a property legitimately named "const" is not
// the const keyword — so it pauses one level and resumes inside each value.
var arbitraryKeyContainers = map[string]struct{}{
	"properties": {}, "definitions": {},
	"schemas": {}, "responses": {}, "headers": {}, "content": {}, "paths": {},
	"callbacks": {}, "securitySchemes": {}, "links": {}, "examples": {},
	"mapping": {}, "variables": {}, "encoding": {}, "scopes": {},
	"requestBodies": {}, "parameters": {},
}

// opaqueValueKeys hold arbitrary DATA, not a schema: their subtree passes
// through untouched. An `example`/`default`/`enum` value may itself contain a
// member named "type", "openapi", or "examples" (it is data, not a
// type-union, a version, or a plural-examples form to rewrite), so the walker
// must not descend into it interpreting those as schema keywords (the
// example-corruption bug).
var opaqueValueKeys = map[string]struct{}{
	"example": {}, "default": {}, "enum": {},
}

// Node downgrades a parsed OpenAPI 3.1 YAML node tree to 3.0.3 in place. The
// document root is a schema-keyword position (scanKeys=true).
func Node(n *yaml.Node) error {
	return walk(n, true)
}

// walk downgrades one node in place. scanKeys is true when this node sits in
// a position whose mapping keys are SCHEMA KEYWORDS (so a 3.1-only keyword
// must fail loudly); it is false inside an arbitrary-key container, whose keys
// are author-chosen names to leave alone.
func walk(n *yaml.Node, scanKeys bool) error {
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if err := walk(c, scanKeys); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		// A sequence's elements sit in the same keyword position as the
		// sequence itself (allOf/anyOf/oneOf hold schemas); scalars have no
		// keys, so scanKeys is harmless for them.
		for _, c := range n.Content {
			if err := walk(c, scanKeys); err != nil {
				return err
			}
		}
		return nil
	}

	// Mapping content alternates key, value.
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]

		if !scanKeys {
			// Author-chosen names (property names, schema names, status codes,
			// media types): do NOT interpret the key. Each value is a schema
			// (or schema-bearing object) again, so keyword scanning resumes.
			if err := walk(val, true); err != nil {
				return err
			}
			continue
		}

		switch key.Value {
		case "openapi":
			if val.Kind == yaml.ScalarNode {
				val.Value = "3.0.3"
			}
			continue
		case "type":
			if val.Kind == yaml.SequenceNode {
				if err := rewriteTypeUnion(n, val); err != nil {
					return err
				}
			}
			continue
		case "const":
			// 3.0 has no const; its faithful equivalent is a single-value
			// enum (const: X ⇔ enum: [X]), so downgrade rather than drop it.
			constValue := *val
			key.Value = "enum"
			*val = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{&constValue}}
			continue
		case "examples":
			// Only the schema-level plural form (a sequence) is 3.1-only;
			// media-type examples (a mapping) exist in 3.0 and stay. The
			// chosen example is opaque data — never descended into.
			if val.Kind == yaml.SequenceNode {
				if len(val.Content) > 0 {
					key.Value = "example"
					*val = *val.Content[0]
				}
				continue
			}
		}

		if _, unsupported := unsupported31Keywords[key.Value]; unsupported {
			return fmt.Errorf("oas30: schema keyword %q is OpenAPI 3.1-only and has no 3.0.3 equivalent — the downgrade cannot express it, so it refuses rather than silently drop it", key.Value)
		}
		if _, opaque := opaqueValueKeys[key.Value]; opaque {
			// Opaque data value — leave the whole subtree untouched.
			continue
		}
		_, arbitrary := arbitraryKeyContainers[key.Value]
		// Inside an arbitrary-key container the CHILD mapping's keys are
		// names, not keywords; one level deeper (each value) is a schema
		// again, so keyword scanning resumes there.
		if err := walk(val, !arbitrary); err != nil {
			return err
		}
	}
	return nil
}

// rewriteTypeUnion turns type: [T, 'null'] into type: T + nullable: true on
// the mapping that holds it.
func rewriteTypeUnion(mapping, union *yaml.Node) error {
	var concrete string
	sawNull := false
	for _, t := range union.Content {
		if t.Value == "null" {
			sawNull = true
			continue
		}
		if concrete != "" {
			return fmt.Errorf("type union %v has two concrete types; 3.0 cannot express it", nodeValues(union))
		}
		concrete = t.Value
	}
	if concrete == "" || !sawNull {
		return fmt.Errorf("type union %v is not the [T, null] shape", nodeValues(union))
	}

	*union = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: concrete}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nullable"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
	)
	return nil
}

func nodeValues(n *yaml.Node) []string {
	vals := make([]string, len(n.Content))
	for i, c := range n.Content {
		vals[i] = c.Value
	}
	return vals
}
