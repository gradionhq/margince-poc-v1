// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Whether a result keeps the promise its tool advertised.
//
// WHY THIS EXISTS AT ALL. A declared schema nothing checks is a comment. The
// surface now tells a client the exact shape of every result, and the only thing
// that can make that true at runtime is reading the result against it — at the
// one place a real result and its declared schema are both in hand.
//
// WHAT IT CHECKS, AND WHAT IT DELIBERATELY DOES NOT. The subset of JSON Schema
// this surface EMITS: types, required members, array items, and a map's value
// shape. Not `format`, not `enum`, not `minimum` — nothing that would need a
// full validator. That is a deliberate line: this is a self-check on output this
// server produced, so it exists to catch a handler and a schema that have parted
// company, not to police a payload from outside. A general validator would be a
// dependency and a second, larger thing to be right about.
//
// It answers a DEFECT STRING rather than an error because the caller logs it and
// carries on with the text block: the defect is ours, not the caller's, and the
// message has to name where in the document it is or a reader cannot find it.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ResultDefect reports the first way value fails schema, or "" when it keeps
// it. A schema this cannot parse is reported rather than passed: an unreadable
// schema is a defect in the same family as an unsatisfied one.
//
// Exported because the dispatcher is not its only caller: the conformance suite
// invokes every tool against a real database and holds each answer to the schema
// its tool advertised, and it has to ask that question the same way the server
// asks it — a second spelling would be a second definition of "conforms".
func ResultDefect(schema, value json.RawMessage) string {
	var declared jsonSchema
	if err := json.Unmarshal(schema, &declared); err != nil {
		return "the advertised schema is not readable: " + err.Error()
	}
	return checkValue(&declared, value, "")
}

// checkValue walks one node of the declared schema against the same node of the
// document. `at` is the path so far, so a failure names the member rather than
// the shape.
func checkValue(schema *jsonSchema, value json.RawMessage, at string) string {
	// A schema with no type states nothing about this node, which is what the
	// two declared exceptions rely on for their inner documents.
	if schema == nil || schema.Type == "" {
		return ""
	}
	// A JSON null satisfies nothing typed. It IS how an absent OPTIONAL member
	// arrives when a producer spells it out rather than omitting it, so it is
	// accepted for one — but checkObject refuses it for a required member
	// BEFORE the walk gets here, because "the key is present" and "the member
	// has a value" are different claims and only the second is what required
	// means.
	if isNull(value) {
		return ""
	}
	switch schema.Type {
	case schemaObject:
		return checkObject(schema, value, at)
	case schemaArray:
		return checkArray(schema, value, at)
	case schemaString:
		return checkScalar[string](value, at, "a string")
	case schemaBoolean:
		return checkScalar[bool](value, at, "a boolean")
	case schemaNumber:
		return checkScalar[float64](value, at, "a number")
	case schemaInteger:
		return checkInteger(value, at)
	}
	return fmt.Sprintf("%s: the advertised schema names an unknown type %q", pathOr(at), schema.Type)
}

// checkObject holds the two promises an object schema makes: every required
// member is present, and every member it describes has the shape it described.
// A member the schema does NOT name passes — every schema here is open, which
// is the claim "at least these".
func checkObject(schema *jsonSchema, value json.RawMessage, at string) string {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(value, &members); err != nil || members == nil {
		return fmt.Sprintf("%s: declared an object, got %s", pathOr(at), summarize(value))
	}
	for _, name := range schema.Required {
		raw, present := members[name]
		if !present {
			return fmt.Sprintf("%s: required member %q is missing", pathOr(at), name)
		}
		if isNull(raw) {
			return fmt.Sprintf("%s: required member %q is null, which is not a value of the declared type",
				pathOr(at), name)
		}
	}
	for name, member := range schema.Properties {
		raw, present := members[name]
		if !present {
			continue
		}
		if defect := checkValue(member, raw, at+"."+name); defect != "" {
			return defect
		}
	}
	// A map: the schema describes its VALUES, and every member is one. The
	// BOOLEAN arm says nothing this check enforces — `false` closes the object,
	// which is a claim about a caller's input rather than about output this
	// server produced, and the surface is open by design anyway.
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		for name, raw := range members {
			if defect := checkValue(schema.AdditionalProperties.Schema, raw, at+"."+name); defect != "" {
				return defect
			}
		}
	}
	return ""
}

func checkArray(schema *jsonSchema, value json.RawMessage, at string) string {
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return fmt.Sprintf("%s: declared an array, got %s", pathOr(at), summarize(value))
	}
	for i, item := range items {
		if defect := checkValue(schema.Items, item, fmt.Sprintf("%s[%d]", at, i)); defect != "" {
			return defect
		}
	}
	return ""
}

// checkScalar decodes into the Go type the declared JSON type maps to. Decoding
// IS the check: encoding/json refuses a string where a number was asked for, and
// it refuses it for the same reason a client would.
func checkScalar[T any](value json.RawMessage, at, want string) string {
	var into T
	if err := json.Unmarshal(value, &into); err != nil {
		return fmt.Sprintf("%s: declared %s, got %s", pathOr(at), want, summarize(value))
	}
	return ""
}

// isNull reports whether a member arrived as the JSON literal null — the one
// value that is a legitimate absence for an optional member and a defect for a
// required one.
func isNull(value json.RawMessage) bool { return strings.TrimSpace(string(value)) == "null" }

// checkInteger holds the one scalar distinction encoding/json does not make for
// us: every JSON number decodes into a float64, so a declared integer receiving
// 1.5 would pass a bare decode. A version, a count and a rank are integers on
// this surface, and a caller told so that receives a fraction has been told
// something false.
func checkInteger(value json.RawMessage, at string) string {
	var number float64
	if err := json.Unmarshal(value, &number); err != nil {
		return fmt.Sprintf("%s: declared an integer, got %s", pathOr(at), summarize(value))
	}
	if number != math.Trunc(number) {
		return fmt.Sprintf("%s: declared an integer, got %s", pathOr(at), summarize(value))
	}
	return ""
}

// pathOr names the node, or the document itself when the failure is at its root.
func pathOr(at string) string {
	if at == "" {
		return "the result"
	}
	return "the result" + at
}

// summarize describes what was found without quoting it back at length: a result
// is this server's own output, but it can carry captured text, and a defect line
// goes to a log a person reads.
func summarize(value json.RawMessage) string {
	trimmed := strings.TrimSpace(string(value))
	switch {
	case trimmed == "":
		return "nothing"
	case len(trimmed) <= 32:
		return trimmed
	}
	return trimmed[:32] + "…"
}
