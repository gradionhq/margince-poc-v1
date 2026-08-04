// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

// Which field had the wrong shape, and what shape it wanted.
//
// encoding/json answers neither question for the request types this seam
// decodes into. oapi-codegen generates an UnmarshalJSON for every contract type
// carrying additionalProperties — every record body that takes custom fields —
// and it decodes field-by-field through a fresh json.Unmarshal per
// json.RawMessage. A fresh Unmarshal has an empty error context, so
// json.UnmarshalTypeError.Field is always "" and the path survives only in that
// wrapper's own prose. A transport reading the empty Field as "the whole body
// was the wrong shape" then tells a caller their object is a string, which is
// both false and unactionable: a real session read it as a transport bug and
// filed one.
//
// So the path is recovered by DECODING, not by matching the wrapper's sentence.
// A library's prose is a message nobody promised to keep; re-running the decode
// one key at a time asks the same decoder the same question and believes only
// its verdict.

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// FieldShapeError is this package's OWN refusal of a field whose VALUE shape the
// target cannot hold — the sibling of UnknownFieldError, which refuses the KEY.
//
// Typed for the same reason: it travels wrapped in FieldDecodeError alongside
// decoder failures whose text is Go internals, and it is one of the two causes
// there whose words are the caller's to read. It names the caller's own key, the
// shape that key accepts, and the shape they sent — nothing of the program that
// read it.
type FieldShapeError struct {
	// Field is the caller's own key, quoted back rather than translated.
	Field string
	// Want and Got name WIRE shapes, never Go types: "an array of objects",
	// "a string". The Go type name is the leak this seam exists to stop.
	Want string
	// Got is empty when the value's SHAPE was right and its content was
	// refused anyway — an unparseable UUID, a key a nested object does not
	// declare. Naming a shape the caller got right would send them to change
	// the one thing that was correct, so that sentence says only that the
	// value was not accepted, and Want stays as the standing description of
	// what the field holds.
	Got string
	// Shape sketches the object the field accepts — one ITEM of it when the
	// field is an array, the value itself when the field is an object. Empty
	// for a scalar field, which Want already describes completely.
	Shape string
	// PerItem reports whether Shape describes one element of an array rather
	// than the whole value: the difference between "each item is" and "it takes".
	PerItem bool
	// cause keeps the decoder's original reachable for an operator's log.
	// Withholding a message is not the same as losing it.
	cause error
}

func (e *FieldShapeError) Error() string {
	var b strings.Builder
	// "must be", matching the wording every other decode refusal on both
	// surfaces already uses for the same mistake. A second phrasing for one
	// mistake reads as a second mistake.
	b.WriteString("`")
	b.WriteString(e.Field)
	b.WriteString("` must be ")
	b.WriteString(e.Want)
	if e.Got != "" {
		b.WriteString(", not ")
		b.WriteString(e.Got)
	} else {
		b.WriteString(" but the value sent was not accepted")
	}
	if e.Shape != "" {
		if e.PerItem {
			b.WriteString("; each item is ")
		} else {
			b.WriteString("; it takes ")
		}
		b.WriteString(e.Shape)
	}
	return b.String()
}

func (e *FieldShapeError) Unwrap() error { return e.cause }

// maxShapeSketch bounds the rendered object sketch. The sketch is reflected off
// OUR types rather than echoed from the caller, so this is not the echo bound —
// it is a budget split. The whole refusal is bounded again at the transport, and
// a wide struct's sketch would otherwise consume that budget and cut the field
// name and the wanted shape off the front, deleting the half a caller acts on.
const maxShapeSketch = 160

// ProbeDecoder re-runs a decode over one key's worth of a payload.
//
// It MUST be the same decoder whose failure is being explained. The two
// transports differ — the provider seam disallows unknown fields, the REST body
// decode refuses keys earlier and more precisely through RejectNonCanonicalKeys
// — and a probe run under the other one answers about a decode nobody performed:
// it would name a key the real decoder accepted, sending the caller to fix a
// field that was never the problem.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
type ProbeDecoder func(raw json.RawMessage, into any) error

// LocalizeFieldFault names the ONE key whose value the target could not hold, by
// decoding each key alone into a fresh target and believing the first verdict
// that reproduces the failure.
//
// Nil means the refusal is better said by the generic restatement: the payload
// is not an object at all (where "the payload must be a JSON object" is exactly
// right), the target is not a struct, or no single key reproduces the failure.
//
// A SHAPE mismatch (json.UnmarshalTypeError) fills Got and the sentence names
// both shapes. Any other per-key failure — an unparseable UUID, a key a nested
// object does not declare — leaves Got empty: those values arrived in the right
// shape, so naming a shape would point the caller at the half they got right.
// What the field holds is still worth saying, because it is what they have to
// re-read to fix it.
//
// Exported for the SAME reason RejectNonCanonicalKeys is: both transports owe a
// caller the field name, and a per-transport copy of this walk is one that
// drifts. The REST body decode and the provider seam call this one function, so
// one mistake gets one sentence on both surfaces.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
func LocalizeFieldFault(raw json.RawMessage, into any, err error, decode ProbeDecoder) *FieldShapeError {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && strings.Contains(typeErr.Field, ".") {
		// A DOTTED path reaches inside a nested value (`links.entity_id`) where
		// this walk only reaches top-level keys, so the decoder's answer is the
		// more precise one and must survive. encoding/json builds that path by
		// joining its field stack on ".", and no contract field name contains
		// one, so the separator is the test.
		//
		// A bare top-level name is NOT deferred to, even though the decoder
		// supplies one: at that point it reports the type it was filling when it
		// failed, which for an array is the ELEMENT type — `links` reads as
		// "must be an object" when the field is an array of them, and a caller
		// who believes it sends an object where an array belongs. The walk below
		// reads the FIELD's own type, so it describes the field.
		return nil
	}
	target := structTypeOf(into)
	if target == nil {
		return nil
	}
	keys, isObject := topLevelKeys(raw)
	if !isObject {
		return nil
	}
	// Sorted because map iteration is not: a payload with two bad fields must
	// name the same one on every identical request, or the refusal is not
	// reproducible and a caller fixing it cannot tell progress from churn.
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		single, rebuilt := oneKeyPayload(key, keys[key])
		if !rebuilt {
			return nil
		}
		probe := reflect.New(target).Interface()
		probeErr := decode(single, probe)
		if probeErr == nil {
			continue
		}
		field, found := fieldByJSONName(target, key)
		if !found {
			// A catch-all key: the type accepts it, so the shape it wanted is
			// not a field's and there is nothing honest to name.
			continue
		}
		want, shape, perItem := wantedShape(field.Type)
		refusal := &FieldShapeError{Field: key, Want: want, Shape: shape, PerItem: perItem, cause: probeErr}
		// Got only when it CONTRADICTS Want. A fault inside an item of a
		// correctly-shaped array reports the array's own shape, and "must be an
		// array of objects, not an array of objects" is a sentence that tells
		// the caller their value is wrong for being right.
		if sent := sentShape(keys[key]); errors.As(probeErr, &typeErr) && sent != want {
			refusal.Got = sent
		}
		return refusal
	}
	return nil
}

// topLevelKeys splits a payload into its top-level keys, reporting whether it
// was a JSON object at all. The failure is a RESULT, not an error to propagate:
// a payload that is not an object has no field to localize, and the generic
// restatement — "the payload must be a JSON object" — is already the exactly
// right sentence for it.
func topLevelKeys(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, false
	}
	return keys, keys != nil
}

// oneKeyPayload rebuilds a single-key object to re-decode, reporting whether it
// could. Failure means the raw value did not survive a round trip it just came
// out of, so this walk cannot answer anything about it — the caller falls back
// to the decoder's own restatement rather than inventing a field name.
func oneKeyPayload(key string, value json.RawMessage) (json.RawMessage, bool) {
	single, err := json.Marshal(map[string]json.RawMessage{key: value})
	if err != nil {
		return nil, false
	}
	return single, true
}

// StrictProbe is the provider seam's own decoder, exposed so a localization run
// for StrictDecode uses the decoder that actually failed.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
func StrictProbe(single json.RawMessage, probe any) error {
	dec := json.NewDecoder(bytes.NewReader(single))
	dec.DisallowUnknownFields()
	return dec.Decode(probe)
}

// structTypeOf walks a decode target to the struct behind any pointers.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
func structTypeOf(into any) reflect.Type {
	t := reflect.TypeOf(into)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	return t
}

// fieldByJSONName finds the struct field a wire key binds to, following
// encoding/json's own promotion rule for embedded structs — the same walk
// collectCanonicalKeys does, and for the same reason: a key that reaches a
// promoted field is a real field, and reporting it as a catch-all key would
// describe the wrong shape.
func fieldByJSONName(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		wire := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			wire = tag[:comma]
		}
		if wire == name && wire != "" && wire != "-" {
			return field, true
		}
		if field.Anonymous && wire == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				if found, ok := fieldByJSONName(embedded, name); ok {
					return found, true
				}
			}
		}
	}
	return reflect.StructField{}, false
}

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

// objectSketch renders the keys an object takes, as the caller would type them:
// `{"domain": string, "is_primary"?: boolean}`. A `?` marks a key the shape does
// not require — a pointer or an omitempty tag, which is how the generated
// contract spells optional.
//
// It renders ONE level. A nested object stays the word `object` rather than
// expanding, because a sketch that recurses grows without a bound the reader
// asked for, and the field this refusal is about is the one at the top.
func objectSketch(t reflect.Type) string {
	var parts []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		wire, opts, _ := strings.Cut(tag, ",")
		if wire == "" || wire == "-" {
			continue
		}
		key := `"` + wire + `"`
		if field.Type.Kind() == reflect.Pointer || strings.Contains(opts, "omitempty") {
			key += "?"
		}
		parts = append(parts, key+": "+leafShape(field.Type))
	}
	if len(parts) == 0 {
		return ""
	}
	sketch := "{" + strings.Join(parts, ", ") + "}"
	if len(sketch) > maxShapeSketch {
		return sketch[:maxShapeSketch] + "…}"
	}
	return sketch
}

// leafShape is the one-word shape a sketch shows for a key's value. It stops at
// `object` and `array` rather than descending, which is what keeps objectSketch
// one level deep.
func leafShape(t reflect.Type) string {
	want, _, _ := wantedShape(t)
	return strings.TrimPrefix(strings.TrimPrefix(want, "an "), "a ")
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
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
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
// "a string" becomes "strings". The irregular one is the only one that matters —
// "true or false" has no plural a sentence can use.
func pluralize(phrase string) string {
	bare := strings.TrimPrefix(strings.TrimPrefix(phrase, "an "), "a ")
	switch bare {
	case "null":
		return "nulls"
	case "true or false", "value this field accepts":
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
