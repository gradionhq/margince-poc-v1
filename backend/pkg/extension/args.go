// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Reading a handler's arguments strictly is part of the published extension
// surface: every unit decodes the same JSON against the same declared schemas,
// and the four holes below are the same four for all of them.
//
//margince:extension-surface

package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DecodeArgs reads a handler's arguments into T, refusing every document the
// operation's published schema does not describe.
//
// A unit's contract declares each request schema with
// additionalProperties: false, and NOTHING between a client and a handler
// enforces it — so this is where that promise is kept. encoding/json's
// DisallowUnknownFields is necessary and not sufficient: the decoder matches
// field names case-INSENSITIVELY, tolerates a member repeated, accepts `null`
// where an object is declared, and stops reading after the first value. Each
// admits a document the schema does not, and three of the four decide which
// value a mutation stores.
//
// It is published rather than written once per unit because it is a SECURITY
// property with a fixed answer: a second copy in the next unit is a second copy
// that can be subtly weaker, and the failure — a caller sending `bdy` and being
// told the write succeeded — is silent in every one of them.
//
// T is a struct whose json tags name the operation's declared arguments. A
// non-struct T declares no names, so every member is unknown and the document
// is refused, which is the safe direction for a programming error.
func DecodeArgs[T any](in json.RawMessage) (T, error) {
	var out T
	if err := checkArgumentObject[T](in); err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(string(in)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("extension: the arguments are not the declared shape: %w", err)
	}
	// EOF after the first value. encoding/json decodes ONE value and stops, so
	// `{} {"body":"x"}` reads as the empty object and the rest is discarded —
	// two documents where the contract declares one, with the operation acting
	// on whichever the decoder happened to reach first.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return out, errors.New("extension: the arguments carry a second JSON value — an operation's contract declares one object")
	}
	return out, nil
}

// checkArgumentObject holds the two things encoding/json will not: that the
// document IS an object, and that every member name is byte-for-byte one T
// declares, appearing once.
//
// It scans tokens rather than unmarshalling into a map, and that is the whole
// reason it exists rather than being three lines: a map COLLAPSES duplicates,
// so `{"body":"first","body":"second"}` arrives as one entry and the check sees
// nothing while encoding/json quietly keeps the last — a way to put one value
// past a reviewer reading the first. The scan sees both.
func checkArgumentObject[T any](in json.RawMessage) error {
	if len(bytes.TrimSpace(in)) == 0 {
		// Left to the decoder: "no arguments at all" is its error to give, and
		// it words it better than a key check can.
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	open, err := dec.Token()
	if err != nil {
		return nil // not JSON; the decoder below says so about the shape
	}
	// `null` is a valid JSON document and unmarshals into a struct as a no-op,
	// so an operation whose schema requires an object would accept it and act
	// on the zero value.
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return errors.New("extension: the arguments are not the declared shape: a JSON object is required")
	}
	declared, seen := declaredJSONNames[T](), map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil // malformed; again the decoder's message, not this one's
		}
		key, ok := token.(string)
		if !ok {
			return nil
		}
		switch {
		case !declared[key]:
			return fmt.Errorf("extension: the arguments are not the declared shape: unknown field %q — a declared name is matched byte for byte, so a case-variant of one is not one of them", key)
		case seen[key]:
			return fmt.Errorf("extension: the arguments are not the declared shape: field %q appears twice — which copy wins is a decoder's choice, not the contract's", key)
		}
		seen[key] = true
		// The value, whatever it is, so the next token read is the next KEY.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
	}
	return nil
}

// declaredJSONNames reads T's json tags.
func declaredJSONNames[T any]() map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		return names
	}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// IsCanonicalUUID reports whether s is the hyphenated 8-4-4-4-12 hex form.
//
// Published beside the decoder because it answers the same kind of question and
// has the same trap: a unit that accepted a looser spelling would hand the
// database a value it refuses, three frames from where the argument arrived.
//
// Hand-written rather than a dependency: this surface is stdlib-only, and a
// unit pulling in a UUID library to check thirty-six bytes would be spending a
// supply-chain decision on it. Deliberately strict — no braces, no urn: prefix
// — because the ids a unit handles are the ones Postgres printed, and those are
// exactly this shape.
func IsCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
