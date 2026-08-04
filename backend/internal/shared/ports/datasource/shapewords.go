// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

// The words a shape is described in — what a field ACCEPTS, and what the caller
// actually sent.
//
// Split from fieldshape.go, which answers a different question: that file finds
// WHICH field failed, this one says what to tell the caller about it. The rule
// every function here keeps is that a phrase names the WIRE shape and never the
// Go type — the type name is the leak the refusal exists to avoid, and `int64`,
// `uuid.UUID` and `crmcontracts.OrganizationDomainInput` all describe this
// program rather than the request.

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"
)

// maxShapeSketch bounds the rendered object sketch. Not an echo bound — the
// sketch is reflected off OUR types, never echoed from the caller — but a
// readability one: the sketch's job is to be read in a refusal, and a wide
// struct rendered whole stops being a sentence. The transport bounds the finished
// message again, and truncates the TAIL, so this clause is the one that gets cut
// there too; keeping it short is what stops that cut landing mid-key.
const maxShapeSketch = 160

// wantedShape names the WIRE shape a Go field accepts, and sketches the object
// behind it when there is one. It never names the Go type: the type name is the
// leak.
func wantedShape(t reflect.Type) (want, shape string, perItem bool) {
	t = derefType(t)
	if t == nil {
		return anyShape, "", false
	}
	// The named types come first because their Go representation describes the
	// program rather than the wire: a UUID is a byte array, a date is a struct,
	// and a caller sends both as strings.
	if named, ok := namedShape(t.Name()); ok {
		return named, "", false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		elem := derefType(t.Elem())
		if elem != nil && elem.Kind() == reflect.Struct && !isNamedShape(elem.Name()) {
			return "an array of " + pluralize(objectShape), objectSketch(elem), true
		}
		elemWant, _, _ := wantedShape(t.Elem())
		return "an array of " + pluralize(elemWant), "", false
	case reflect.Struct:
		return objectShape, objectSketch(t), false
	case reflect.Map:
		return objectShape, "", false
	default:
		return scalarShape(t.Kind()), "", false
	}
}

// The two shape phrases repeated across this file, named so the array form and
// the bare form cannot drift apart.
const (
	objectShape = "an object"
	anyShape    = "a value this field accepts"
)

// namedShape names the WIRE shape of a type whose Go representation is not it.
func namedShape(name string) (string, bool) {
	switch name {
	case "UUID":
		return "a UUID string", true
	case "Time":
		return "an RFC 3339 timestamp", true
	case "Date":
		return "a date in YYYY-MM-DD form", true
	case "Email":
		return "an email address", true
	default:
		return "", false
	}
}

func isNamedShape(name string) bool {
	_, ok := namedShape(name)
	return ok
}

// scalarShape names the wire shape of a Go kind, never its width: an int64 and
// an int8 both take an integer, and a caller told "int64" has been told about
// this program.
func scalarShape(kind reflect.Kind) string {
	switch kind {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	default:
		return anyShape
	}
}

// objectSketch renders the keys an object takes: `{domain: string,
// is_primary?: boolean}`. A `?` marks a key the shape does not require — a
// pointer or an omitempty tag, which is how the generated contract spells
// optional.
//
// The notation is the one gen-recordfields renders into the create_record and
// update_record descriptions, deliberately and to the character. Those two
// renderers exist separately because this tier cannot reach the generated table
// (shared is stdlib-only, and platform may not import contracts either) — but an
// agent reads the tool description and then this refusal about the SAME field,
// and two spellings of one shape read as two different shapes.
func objectSketch(t reflect.Type) string {
	var parts []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		wire, opts, _ := strings.Cut(tag, ",")
		if wire == "" || wire == "-" {
			continue
		}
		key := wire
		if field.Type.Kind() == reflect.Pointer || strings.Contains(opts, "omitempty") {
			key += "?"
		}
		parts = append(parts, key+": "+leafShape(field.Type))
	}
	if len(parts) == 0 {
		return ""
	}
	return boundSketch("{" + strings.Join(parts, ", ") + "}")
}

// boundSketch truncates on a RUNE boundary, the way the transport's own bound
// does. A byte offset can split a multi-byte rune and put the replacement
// character into a problem body; no contract field name is non-ASCII today,
// which makes this the kind of difference that is invisible until it is not.
func boundSketch(sketch string) string {
	if len(sketch) <= maxShapeSketch {
		return sketch
	}
	cut := maxShapeSketch
	for cut > 0 && !utf8.RuneStart(sketch[cut]) {
		cut--
	}
	return sketch[:cut] + "…}"
}

// leafShape is the one-word shape a sketch shows for a key's value.
//
// It stops at `object` and `array` instead of asking wantedShape, which is what
// keeps a sketch one level deep in WORK as well as in output: wantedShape builds
// the nested sketch before this would discard it, so routing through it walked
// the whole type subtree to print one word — and would recurse without a bound
// on a self-referential schema, on a path any caller can trigger.
func leafShape(t reflect.Type) string {
	t = derefType(t)
	if t == nil {
		return bareShape(anyShape)
	}
	// These four are gen-recordfields' stringShape spellings, verbatim: a
	// sketch and the tool description are read about the same field minutes
	// apart, and "uuid" in one and "UUID string" in the other is a difference a
	// reader has to resolve before either helps them. What still differs is the
	// enum — reflection cannot see the values, so an enum field reads `string`
	// here and its vocabulary in the description. That gap is structural, and
	// the description is the half that carries it.
	switch t.Name() {
	case "UUID":
		return "uuid"
	case "Time":
		return "rfc3339"
	case "Date":
		return "YYYY-MM-DD"
	case "Email":
		return "email"
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return bareShape(scalarShape(t.Kind()))
	}
}

// bareShape drops the article a shape phrase carries for use in a sentence: a
// sketch reads `domain: string`, not `domain: a string`.
func bareShape(phrase string) string {
	return strings.TrimPrefix(strings.TrimPrefix(phrase, "an "), "a ")
}

// sentShape names what the caller actually put on the wire, in the same
// vocabulary wantedShape uses so the two halves of the sentence compare. An
// array is described by its FIRST element, because "an array of strings" is what
// the caller can see they typed — naming only the element that failed to decode
// reads as though a scalar was sent where an array was.
func sentShape(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "nothing"
	}
	switch trimmed[0] {
	case '{':
		return "an object"
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			// Unreachable — the payload already decoded once to get here — but
			// "an empty array" would be a statement about the caller's input,
			// and an impossible branch must not be the one that says something
			// false about it.
			return "an array"
		}
		if len(items) == 0 {
			return "an empty array"
		}
		return "an array of " + pluralize(sentShape(items[0]))
	case '"':
		return "a string"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	default:
		return "a number"
	}
}

// pluralize turns one shape phrase into the plural an array of it takes:
// "a string" becomes "strings". The cases below are the phrases whose plural is
// not the phrase plus an s.
func pluralize(phrase string) string {
	bare := bareShape(phrase)
	switch bare {
	case "null":
		return "nulls"
	case "value this field accepts":
		return "values"
	case "date in YYYY-MM-DD form":
		return "dates"
	case "RFC 3339 timestamp":
		return "RFC 3339 timestamps"
	case "UUID string":
		return "UUID strings"
	case "email address":
		return "email addresses"
	}
	if strings.HasSuffix(bare, "s") {
		return bare
	}
	return bare + "s"
}

// derefType walks a type to the value behind any pointers; contract fields are
// pointers wherever the schema makes them optional.
func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
