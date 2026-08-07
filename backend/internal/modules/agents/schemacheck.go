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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
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
	// null reaches here only where the schema said something is present: an
	// array element, a map value, a required member. encoding/json would decode
	// it into a zero of almost any Go type without complaint, so it has to be
	// refused explicitly or a hole would satisfy every scalar rule below. The
	// ONE legitimate null — an optional member spelled out rather than omitted —
	// never gets this far; checkObject passes over it.
	if isNull(value) {
		return fmt.Sprintf("%s: declared %s, got null", pathOr(at), schema.Type)
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

// checkObject holds what an object schema promises, in the three separate
// claims it is made of: every required member has a value, every member the
// schema DESCRIBES has the shape it described, and every member it does not
// describe is admitted on whatever terms `additionalProperties` set.
func checkObject(schema *jsonSchema, value json.RawMessage, at string) string {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(value, &members); err != nil || members == nil {
		return fmt.Sprintf("%s: declared an object, got %s", pathOr(at), summarize(value))
	}
	if defect := checkRequired(schema, members, at); defect != "" {
		return defect
	}
	if defect := checkDeclaredMembers(schema, members, at); defect != "" {
		return defect
	}
	return checkExtraMembers(schema, members, at)
}

// checkRequired: a required member is required to have a VALUE, not merely a
// key. `null` there is the shape of a bug — a producer that built the field and
// left it empty — and a nil Go slice arrives as exactly that.
func checkRequired(schema *jsonSchema, members map[string]json.RawMessage, at string) string {
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
	return ""
}

// checkDeclaredMembers walks the members the schema names. An OPTIONAL one
// spelled out as null is the single place a null is legitimate: it is how a
// producer says "absent" without omitting the key.
func checkDeclaredMembers(schema *jsonSchema, members map[string]json.RawMessage, at string) string {
	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	for name, member := range schema.Properties {
		raw, present := members[name]
		if !present || (!required[name] && isNull(raw)) {
			continue
		}
		if defect := checkValue(member, raw, at+"."+name); defect != "" {
			return defect
		}
	}
	return ""
}

// checkExtraMembers applies `additionalProperties` to the members `properties`
// does NOT name — that is what "additional" means, and applying it to a
// declared member would hold that member to two schemas at once.
//
// The BOOLEAN arm closes the object. A hand-written schema that closes itself
// is making a promise this server should keep: a result carrying MORE than it
// declared is the same defect as one carrying less, read from the other side.
func checkExtraMembers(schema *jsonSchema, members map[string]json.RawMessage, at string) string {
	if schema.AdditionalProperties == nil {
		return ""
	}
	closed := schema.AdditionalProperties.Closed
	if closed != nil && *closed {
		return ""
	}
	for name, raw := range members {
		if _, declared := schema.Properties[name]; declared {
			continue
		}
		if closed != nil {
			return fmt.Sprintf("%s: member %q is not declared, and the schema is closed", pathOr(at), name)
		}
		if defect := checkValue(schema.AdditionalProperties.Schema, raw, at+"."+name); defect != "" {
			return defect
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
	// json.Number is a STRING type, so unmarshalling into it accepts a quoted
	// JSON token as readily as a bare one — `"7"` would parse and then satisfy
	// Int64. The token is read with UseNumber instead, which decodes a JSON
	// number as json.Number and a JSON string as a string, so the two stay
	// apart the way the wire keeps them apart.
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var token any //craft:ignore naked-any the token is whatever the document holds at this node, which is the question being asked
	if err := decoder.Decode(&token); err != nil {
		return fmt.Sprintf("%s: declared an integer, got %s", pathOr(at), summarize(value))
	}
	number, isNumber := token.(json.Number)
	if !isNumber {
		return fmt.Sprintf("%s: declared an integer, got %s", pathOr(at), summarize(value))
	}
	// Int64 parses the literal AS WRITTEN. Decoding into float64 and testing for
	// a whole value would lose the distinction past 2^53, where a large fraction
	// rounds to a whole number and passes a gate meant to refuse it.
	if _, err := number.Int64(); err != nil {
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

// summaryBytes bounds what a defect line quotes back.
const summaryBytes = 32

// summarize describes what was found without quoting it back at length: a result
// is this server's own output, but it can carry captured text, and a defect line
// goes to a log a person reads.
func summarize(value json.RawMessage) string {
	trimmed := strings.TrimSpace(string(value))
	switch {
	case trimmed == "":
		return "nothing"
	case len(trimmed) <= summaryBytes:
		return trimmed
	}
	// Cut on a RUNE boundary. The comment above says a result can carry captured
	// text, and captured text is exactly where a multi-byte rune sits — a byte
	// slice through one writes invalid UTF-8 into a line a person reads.
	cut := trimmed[:summaryBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
