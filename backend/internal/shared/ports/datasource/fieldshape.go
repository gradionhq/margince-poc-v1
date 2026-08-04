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
// both false and unactionable.
//
// So the path is recovered by DECODING, not by matching the wrapper's sentence.
// A library's prose is a message nobody promised to keep; re-running the decode
// one key at a time asks the same decoder the same question and believes only
// its verdict.

import (
	"bytes"
	"encoding/json"
	"errors"
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
	// shape sketches the object the field accepts — one ITEM of it when the
	// field is an array, the value itself when the field is an object. Empty
	// for a scalar field, which Want already describes completely.
	//
	// Unexported with perItem because only Error() reads them: they are how the
	// sentence is built, not a second way to ask what a field takes. The two a
	// transport genuinely needs — Field and Got — are the two it is given.
	shape string
	// perItem reports whether shape describes one element of an array rather
	// than the whole value: the difference between "each item is" and "it takes".
	perItem bool
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
	if e.shape != "" {
		if e.perItem {
			b.WriteString("; each item is ")
		} else {
			b.WriteString("; it takes ")
		}
		b.WriteString(e.shape)
	}
	return b.String()
}

func (e *FieldShapeError) Unwrap() error { return e.cause }

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
// re-decoding each declared field's value on its own and taking the first, in
// sorted order, that fails.
//
// The first that FAILS ON ITS OWN, not the one the decoder blamed: nothing here
// compares the two, so a payload with several bad fields may name a different one
// than encoding/json stopped at. Both are wrong, so either is worth reporting —
// but a reader building on "the same one" would be building on a guarantee this
// does not give. It does guarantee the same answer every time for the same
// payload, which is what a caller fixing one field needs.
//
// It also assumes a field's value decodes independently of its siblings, which is
// what makes a per-field probe mean anything. True of every generated
// UnmarshalJSON in this contract — they are all the additionalProperties
// field-by-field shape — and it would stop being true of a discriminated union,
// where every single-field probe fails and this would blame an arbitrary key.
//
// Nil means the refusal is better said by the generic restatement: the payload
// is not an object at all (where "the payload must be a JSON object" is exactly
// right), the target is not a struct, or no declared field fails alone.
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
	for _, key := range declaredFieldKeys(target, keys) {
		field, _ := fieldByJSONName(target, key)
		probe := reflect.New(field.Type).Interface()
		probeErr := decode(keys[key], probe)
		if probeErr == nil {
			continue
		}
		want, shape, perItem := wantedShape(field.Type)
		refusal := &FieldShapeError{Field: key, Want: want, shape: shape, perItem: perItem, cause: probeErr}
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

// declaredFieldKeys narrows a payload's keys to the ones the target actually
// declares, sorted.
//
// Narrowing BEFORE the probes is what bounds the work. A payload's key count is
// the caller's to choose — a type with an additionalProperties catch-all accepts
// any cf_ key, and the tool surface admits them by design — so probing per KEY
// lets a refused call cost one decode per key sent, and a refusal is free to
// repeat. Per declared FIELD it is bounded by the contract instead. Nothing is
// lost: a key the target does not declare could only be skipped anyway, since
// the shape it wanted is not a field's and there is nothing honest to name.
//
// Sorted because map iteration is not: a payload with two bad fields must name
// the same one on every identical request, or the refusal is not reproducible
// and a caller fixing it cannot tell progress from churn.
func declaredFieldKeys(target reflect.Type, keys map[string]json.RawMessage) []string {
	declared := make([]string, 0, len(keys))
	for key := range keys {
		if _, found := fieldByJSONName(target, key); found {
			declared = append(declared, key)
		}
	}
	slices.Sort(declared)
	return declared
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

// strictProbe is the provider seam's own decoder, so a localization run for
// StrictDecode uses the decoder that actually failed.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
func strictProbe(single json.RawMessage, probe any) error {
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
		wire, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if wire == name && wire != "" && wire != "-" {
			return field, true
		}
		if found, ok := promotedFieldByJSONName(field, wire, name); ok {
			return found, true
		}
	}
	return reflect.StructField{}, false
}

// promotedFieldByJSONName follows an EMBEDDED struct, which encoding/json treats
// as promoting its fields into the outer one. Split out so the walk above reads
// as the two cases it has — this field, or a field this one promotes — rather
// than as one loop carrying the pointer-deref of the second.
func promotedFieldByJSONName(field reflect.StructField, wire, name string) (reflect.StructField, bool) {
	if !field.Anonymous || wire != "" {
		return reflect.StructField{}, false
	}
	embedded := derefType(field.Type)
	if embedded == nil || embedded.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	return fieldByJSONName(embedded, name)
}
