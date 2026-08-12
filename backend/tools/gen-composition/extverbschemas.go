// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Reading an operation's SCHEMAS out of the merged contract: where a method's
// arguments live, and how a declared schema becomes the standalone JSON literal
// the composed program carries.
//
// Split from extverbs.go, which reads an operation's IDENTITY and GOVERNANCE.
// The two answer different questions and fail in different ways — a bad route is
// a namespace violation, an unresolvable $ref is an argument shape no client can
// use — and together they were over the file-length cap.

import (
	"encoding/json"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// argumentSchema reads the operation's argument shape from wherever the method
// says it lives, and refuses it in the other place.
//
// The refusals are the point rather than tidiness: this seam reads a body OR a
// query, never both, so arguments declared on the side it does not read would be
// published to every client and then silently dropped on every call. That is the
// defect the old body-only reader had in mirror image — it refused GET outright
// with "a route whose arguments never arrive" — and it is worth a named
// generation failure at the declaration's own position.
func argumentSchema(method string, body, params *yaml.Node) (json.RawMessage, error) {
	if extension.CarriesBody(method) {
		if !params.IsZero() {
			return nil, fmt.Errorf("the operation declares %s and also declares parameters — a %s carries its arguments in the body, so the parameters would be published and never read. Move them into the requestBody schema, or declare the operation GET", method, method)
		}
		return requestSchema(body)
	}
	if !body.IsZero() {
		return nil, fmt.Errorf("the operation declares %s and also declares a requestBody — a %s carries no body, so the schema would be published and never read. Declare the arguments as query parameters, or declare the operation POST", method, method)
	}
	return querySchema(params)
}

// querySchema assembles a bodyless operation's input schema from its declared
// query parameters: one property per parameter, `required` from the parameters
// that say so, and `additionalProperties: false` because the serving seam
// refuses a query key nothing declared.
//
// The assembled object is what a model is shown as the tool's argument shape and
// what the seam decodes against, so it is built from the SAME declaration a
// human reads in the contract rather than restated. An operation with no
// parameters yields the empty object, which is the honest shape for a list or a
// status probe — not nil, because "takes no arguments" is a fact worth
// publishing rather than a gap a default fills in.
func querySchema(params *yaml.Node) (json.RawMessage, error) {
	schema := struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties,omitempty"`
		Required             []string                   `json:"required,omitempty"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}{Type: "object"}
	if params.IsZero() {
		return json.Marshal(schema)
	}
	if params.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("the operation's parameters is not a list")
	}
	schema.Properties = make(map[string]json.RawMessage, len(params.Content))
	for _, param := range params.Content {
		var decl struct {
			Name     string `yaml:"name"`
			In       string `yaml:"in"`
			Required bool   `yaml:"required"`
		}
		if err := param.Decode(&decl); err != nil {
			return nil, fmt.Errorf("the operation declares a parameter this reader cannot decode: %w", err)
		}
		// Query only. `path` is refused because an extension route may carry no
		// template (see extension.Verb's route grammar), and `header`/`cookie`
		// because a tool's arguments are its arguments — transport metadata is not
		// something a model may be handed as an input field.
		if decl.In != "query" {
			return nil, fmt.Errorf("parameter %q is declared in %q — a bodyless extension operation takes its arguments from the query string only", decl.Name, decl.In)
		}
		if decl.Name == "" {
			return nil, fmt.Errorf("the operation declares a query parameter with no name")
		}
		if _, dup := schema.Properties[decl.Name]; dup {
			// json.Marshal would silently keep one of the two, and the published
			// schema would describe an argument set no reader of the contract wrote.
			return nil, fmt.Errorf("the operation declares the query parameter %q twice", decl.Name)
		}
		node := yamlChild(param, "schema")
		if node == nil {
			return nil, fmt.Errorf("query parameter %q declares no schema — the seam coerces a query value against its declared type, so an untyped one could not be decoded", decl.Name)
		}
		// Through jsonSchema for the $ref refusal, which matters here for the same
		// reason it matters for a body: the emitted literal is a standalone schema
		// and this generator resolves nothing.
		encoded, err := jsonSchema("parameter "+decl.Name, node)
		if err != nil {
			return nil, err
		}
		schema.Properties[decl.Name] = encoded
		if decl.Required {
			schema.Required = append(schema.Required, decl.Name)
		}
	}
	// Sorted, because `required` is a LIST and YAML declaration order would
	// otherwise reach the emitted literal and the manifest digest — making a
	// reordering of the contract look like a changed argument contract.
	slices.Sort(schema.Required)
	return json.Marshal(schema)
}

// requestSchema reads the operation's inline JSON request schema. A $ref is
// refused BY NAME rather than resolved: this generator does not walk
// references, and a silently unresolved one would advertise `{"$ref": …}` to a
// model as the tool's argument shape.
func requestSchema(body *yaml.Node) (json.RawMessage, error) {
	if body.IsZero() {
		return nil, fmt.Errorf("the operation declares no requestBody — a body-carrying extension operation is a tool invocation and its arguments are the body")
	}
	schema := yamlChild(yamlChild(yamlChild(body, "content"), "application/json"), "schema")
	if schema == nil {
		return nil, fmt.Errorf("the operation's requestBody declares no application/json schema")
	}
	return jsonSchema("requestBody", schema)
}

// responseSchema reads the 200 response's inline JSON schema. Absent is
// allowed — a tool may return nothing describable — but a $ref is refused for
// the same reason as the request's.
func responseSchema(responses *yaml.Node) (json.RawMessage, error) {
	schema := yamlChild(yamlChild(yamlChild(yamlChild(responses, "200"), "content"), "application/json"), "schema")
	if schema == nil {
		return nil, nil
	}
	return jsonSchema("response 200", schema)
}

func jsonSchema(where string, node *yaml.Node) (json.RawMessage, error) {
	// Recursive, not a check on the root. The emitted literal IS the standalone
	// schema an MCP client hands a model, so a `$ref` anywhere inside it — one
	// property, one array's items, one branch of a oneOf — arrives as an
	// argument shape nothing can resolve: the client has no document to resolve
	// it against, and this generator does not walk references to inline it. The
	// root-only check refused the obvious spelling and passed every nested one,
	// which is the shape a real fragment is more likely to have.
	if path, found := findRef(node, ""); found {
		return nil, fmt.Errorf("%s schema declares a $ref at %s — declare an extension operation's schema inline; this generator does not resolve references, and an unresolved one would be advertised to a model as the argument shape", where, path)
	}
	var doc any
	if err := node.Decode(&doc); err != nil {
		return nil, err
	}
	// Marshalled with sorted keys by encoding/json's map handling, so the
	// emitted literal and the hashed bytes are stable across YAML key order.
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s schema is not expressible as JSON: %w", where, err)
	}
	return encoded, nil
}

// schemaDataKeywords hold INSTANCE data, not subschemas. A `$ref` under any of
// them is a property of the example or the default value being described — a
// perfectly ordinary object member that happens to be spelled `$ref` — and
// refusing it would refuse a schema that references nothing.
var schemaDataKeywords = map[string]bool{
	"example": true, "examples": true, "default": true, "const": true, "enum": true,
}

// namedSubschemaKeywords hold subschemas keyed by an AUTHOR-CHOSEN name. The
// level below them is a set of names, so `properties.$ref` is a property called
// `$ref` and not a reference; the level below THAT is a schema again.
//
// `dependentSchemas` belongs here for exactly the reason `properties` does: in
// 2020-12 (the dialect an openapi 3.1 contract carries) it maps PROPERTY NAMES
// to schemas, so a schema conditioned on a property literally named `$ref` was
// being refused as an unresolved reference — a correct fragment rejected for
// the name one of its properties happens to have.
var namedSubschemaKeywords = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true, "definitions": true,
	"dependentSchemas": true,
}

// findRef walks a SCHEMA node for a `$ref` at any depth, returning the path to
// the first one so the refusal names a position rather than a document. The
// path is written the way a reader would say it out loud —
// `.properties.deal.$ref`, `.allOf[0].$ref` — because the point of naming it is
// that the author can go to the line.
//
// It is a schema walk rather than a document walk, and the distinction is what
// keeps it from refusing correct fragments: a bare recursive search for the key
// `$ref` also finds a PROPERTY named `$ref` and a `$ref` member inside an
// `example`, neither of which is a reference to anything. The two keyword sets
// above are what tell those apart.
func findRef(node *yaml.Node, path string) (string, bool) {
	if node == nil {
		return "", false
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1]
			switch {
			case key == "$ref":
				return path + ".$ref", true
			case schemaDataKeywords[key]:
				continue
			case namedSubschemaKeywords[key] && value.Kind == yaml.MappingNode:
				for j := 0; j+1 < len(value.Content); j += 2 {
					if found, ok := findRef(value.Content[j+1], path+"."+key+"."+value.Content[j].Value); ok {
						return found, true
					}
				}
			default:
				if found, ok := findRef(value, path+"."+key); ok {
					return found, true
				}
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if found, ok := findRef(child, fmt.Sprintf("%s[%d]", path, i)); ok {
				return found, true
			}
		}
	}
	return "", false
}
