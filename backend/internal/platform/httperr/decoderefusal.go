// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// What a caller is told when their JSON did not decode — a REST body, a
// provider-seam field patch, or an MCP tool's arguments.
//
// A decode failure is the one refusal whose words are written by encoding/json
// rather than by us, and those words describe OUR program: the Go struct being
// filled, the Go type of the field, the reference layout `2006-01-02` that no
// caller ever typed. The wire field and the shape we wanted are both the only
// half a caller can act on and the only half that is theirs to see.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// genericDecodeDetail answers a decode failure no branch below recognises. It
// names no field, because at that point we do not know which one — and a guess
// sends the caller to change an input that was never wrong.
const genericDecodeDetail = "the payload could not be decoded; check each value's type and " +
	"format against this operation's schema"

// RestateDecodeError restates a JSON decode failure in the caller's own
// vocabulary, keeping the original in the chain so whoever logs it still reads
// the decoder's own words.
//
// Nil means only one thing: no branch below could name the shape. It never means
// the error is already safe to show. A third-party value unmarshaler answers here
// too — google/uuid's `invalid UUID length: 6` names no field and describes our
// program — so a caller that treats nil as "travels unchanged" ships a library's
// sentence. A site whose error may instead be a refusal WE wrote must recognise
// that refusal by its own TYPE before asking this, and mask whatever is left.
//
// One restatement for every surface, because the decoder text reaches a client
// by three routes — the REST body decode, the provider seam's field decode, and
// the MCP tool surface's argument decode — and a per-route habit is one every
// new route can skip.
func RestateDecodeError(err error) error {
	detail, named := decodeDetail(err)
	if !named {
		return nil
	}
	return &restatedDecodeError{detail: detail, cause: err}
}

// SafeDecodeError answers a decode failure in words the caller may read, and
// reports whether the original's own words were WITHHELD to do it. It is
// RestateDecodeError plus the answer for the shapes no branch can name: those
// get genericDecodeDetail, and `withheld` is the surface's cue that it owes the
// original to a log rather than to the caller.
//
// The pairing lives here because all three decode surfaces owe both halves — a
// shape we can name is restated, and one we cannot is masked and logged — and a
// surface that only asks RestateDecodeError reads nil as "safe to show" and
// ships whatever a third-party value unmarshaler wrote about this program.
func SafeDecodeError(err error) (safe error, withheld bool) {
	if restated := RestateDecodeError(err); restated != nil {
		return restated, false
	}
	return &restatedDecodeError{detail: genericDecodeDetail, cause: err}, true
}

// restatedDecodeError carries the caller-facing sentence while keeping the
// decoder's original reachable: Error() is what any surface may show, Unwrap is
// what an operator's log and an errors.As on a typed cause still reach.
type restatedDecodeError struct {
	detail string
	cause  error
}

func (e *restatedDecodeError) Error() string { return e.detail }
func (e *restatedDecodeError) Unwrap() error { return e.cause }

// decodeDetail is the shape-by-shape translation. Each branch is a shape whose
// caller-facing half we can name exactly; anything else reports false rather
// than string-matching a library's prose, which is a message nobody promised to
// keep.
func decodeDetail(err error) (string, bool) {
	// Ours already: this refusal names the caller's own value and the form it
	// must take, with nothing of the program that read it.
	var badID *ids.ParseError
	if errors.As(err, &badID) {
		return boundFaultText(badID.Error()), true
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return unmarshalTypeDetail(typeErr), true
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("the payload is not valid JSON at byte %d; send one well-formed JSON object",
			syntaxErr.Offset), true
	}

	var timeErr *time.ParseError
	if errors.As(err, &timeErr) {
		return boundFaultText(strconv.Quote(timeErr.Value)) + " is not " + expectedTimeFormat(timeErr.Layout), true
	}

	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "the payload ends before its JSON value is complete; send one complete JSON object", true
	case errors.Is(err, io.EOF):
		return "the payload is empty; send a JSON object carrying this operation's fields", true
	}
	return "", false
}

// unmarshalTypeDetail names the wire field and the shape it accepts.
//
// Field carries the json path, which is how the CALLER's own body spells the
// key — so it is quoted back rather than translated, and it is bounded because
// the caller chose its length.
func unmarshalTypeDetail(e *json.UnmarshalTypeError) string {
	if e.Field != "" {
		return "`" + boundFaultText(e.Field) + "` must be " + friendlyJSONType(e.Type) +
			", not " + jsonKindPhrase(e.Value)
	}
	// An empty Field is TWO different failures. One is the whole body arriving
	// as the wrong shape, where naming the Go struct we tried to fill would be
	// exactly the leak this file exists to stop.
	//
	// Type is only ever nil for an error some other package constructed, which is
	// why friendlyJSONType below guards it too: an unmarshal error carrying no
	// type names no shape, and the fallback sentence is the honest answer for it.
	if target := deref(e.Type); target != nil &&
		(target.Kind() == reflect.Struct || target.Kind() == reflect.Map) {
		return "the payload must be a JSON object, not " + jsonKindPhrase(e.Value)
	}
	// The other is a value whose own UnmarshalJSON decoded it in a nested step,
	// which discards the path — the shape we wanted is all that survives, and
	// naming a field here would name the wrong one.
	return "a value in the payload must be " + friendlyJSONType(e.Type) + ", not " +
		jsonKindPhrase(e.Value) + "; check each value's type against this operation's schema"
}

// friendlyJSONType names the WIRE shape a Go type accepts, and never the type
// itself — the type name IS the leak. The three named cases come first because
// their Go representation describes the program rather than the wire: a UUID is
// a byte array, a date is a struct, and a caller sends both as strings.
func friendlyJSONType(t reflect.Type) string {
	t = deref(t)
	if t == nil {
		return "a value this field accepts"
	}
	switch t.Name() {
	case "UUID":
		return "a UUID string"
	case "Time":
		return "an RFC 3339 timestamp"
	case "Date":
		return "a date in YYYY-MM-DD form"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a value this field accepts"
	}
}

// jsonKindPhrase renders encoding/json's word for what the caller actually
// sent. Only the two container words take "an".
func jsonKindPhrase(kind string) string {
	if kind == "array" || kind == "object" {
		return "an " + kind
	}
	return "a " + kind
}

// expectedTimeFormat names the format a timestamp field accepts WITHOUT the Go
// layout that describes it: `2006-01-02` is a reference date, and a caller who
// reads it as an example sends a year that is not theirs.
func expectedTimeFormat(layout string) string {
	switch layout {
	case time.RFC3339, time.RFC3339Nano:
		return "an RFC 3339 timestamp (for example 2026-01-31T09:00:00Z)"
	case time.DateOnly:
		return "a date in YYYY-MM-DD form"
	default:
		return "in the format this field's schema declares"
	}
}

// deref walks a type to the value behind any pointers; contract fields are
// pointers wherever the schema makes them optional.
func deref(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// fieldDecodeRefusal renders the provider seam's field-decode refusal, and
// reports whether it WITHHELD the cause's own words to do so.
//
// That seam wraps two provenances in one type — its own key refusal, which quotes
// the caller's keys, and whatever encoding/json and the value unmarshalers
// underneath it produced, which describes this program — so the split is by TYPE.
// A library's prose is not a contract, so matching on it is not a boundary: it is
// how `invalid UUID length: 6` reached a client on the one route the restatement
// was supposed to close.
//
// Both answers come from one function so the sentence shown and the words kept
// cannot disagree about which was which.
func fieldDecodeRefusal(cause error) (detail string, causeWithheld bool) {
	var ourKeyRefusal *datasource.UnknownFieldError
	if errors.As(cause, &ourKeyRefusal) {
		return boundFaultText(ourKeyRefusal.Error()) + fieldDecodeAdvice, false
	}
	// The seam's other own refusal: the key was right and its VALUE was not.
	// It carries the field name, which is the half every branch below is
	// missing — the generated per-field unmarshalers decode through a fresh
	// json.Unmarshal, so the decoder's own error names no path.
	var ourShapeRefusal *datasource.FieldShapeError
	if errors.As(cause, &ourShapeRefusal) {
		return fieldShapeDetail(ourShapeRefusal)
	}
	if restated := RestateDecodeError(cause); restated != nil {
		return restated.Error() + fieldDecodeAdvice, false
	}
	return genericDecodeDetail, true
}

// fieldShapeDetail renders the seam's field-shape refusal, and reports whether
// it WITHHELD the cause's own words.
//
// A shape mismatch is self-sufficient: it names the field, the shape that field
// takes and the shape that arrived, which is the whole of what the generic
// advice would send a caller to look up. It takes no advice tail.
//
// A value refused for a reason OTHER than its shape is the interesting case. The
// branches in decodeDetail already say those well — a malformed timestamp is
// quoted back with the format it needed — but they say it about an unnamed
// value, because the decoder lost the path. Pairing the two gives the caller
// both halves of one mistake. What nothing can name still gets the field name,
// which is the actionable half on its own, and its cause goes to a log: the
// third-party unmarshaler's `invalid UUID length: 6` describes this program.
func fieldShapeDetail(refusal *datasource.FieldShapeError) (detail string, causeWithheld bool) {
	if refusal.Got != "" {
		return boundFaultText(refusal.Error()), false
	}
	if restated := RestateDecodeError(refusal.Unwrap()); restated != nil {
		return "`" + boundFaultText(refusal.Field) + "` " + restated.Error() + fieldDecodeAdvice, false
	}
	return boundFaultText(refusal.Error()), true
}

// fieldDecodeAdvice closes a field-decode refusal with what to DO about it, which
// a decoder message never says on its own. genericDecodeDetail carries its own
// advice, so it does not take this.
const fieldDecodeAdvice = " — check the field names and value types against this operation's request schema"

// withheldFieldDecodeCause answers the words a field-decode refusal kept back
// from the caller, for the surface to log. Withholding a message is not the same
// as losing it, and the shape nothing could name is the one an operator most
// needs to see: it is the one saying a decode failure exists that this file does
// not yet translate.
func withheldFieldDecodeCause(err error) error {
	var badFields *datasource.FieldDecodeError
	if !errors.As(err, &badFields) {
		return nil
	}
	if _, causeWithheld := fieldDecodeRefusal(badFields.Cause); !causeWithheld {
		return nil
	}
	// A localized refusal is a sentence WE wrote over the decoder's, so logging
	// it back would record our own words as the withheld ones and leave the
	// operator with no trace of what the library actually refused — the exact
	// loss this function exists to prevent, dressed as a log line that looks
	// like it worked.
	var shaped *datasource.FieldShapeError
	if errors.As(badFields.Cause, &shaped) {
		return shaped.Unwrap()
	}
	return badFields.Cause
}
