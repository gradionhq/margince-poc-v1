// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

// The notepad itself: the three operations over ext_crm_demo_note, which are
// what make the migrations layer and the RBAC object observable. None of them
// names a workspace — the Runtime pins the transaction to the invocation's
// tenant before the first statement runs, and the table's policy holds whatever
// SQL these functions write.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// maxNoteBody bounds a note. The contract declares the same bound
// (api/crm.yaml, maxLength: 500) and this is the enforcing copy: the contract's
// schema is advertised to clients and models, and nothing on this seam
// validates a request body against it before the handler runs.
const maxNoteBody = 500

// maxNotesPerRead bounds one list. The screen renders the whole result, and a
// notepad nobody pruned should still answer in one frame.
const maxNotesPerRead = 200

// note is one row, and mirrors the shape the contract's 200 response declares.
type note struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// listNotes answers the workspace's notes, newest first.
func listNotes(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	// Decoded although there is nothing to read. The contract declares an empty
	// object with additionalProperties: false, and this seam validates no body
	// against the schema before the handler runs — so ignoring the document
	// would make this the one operation of the three that accepts whatever it
	// is sent. A closed schema that is closed in the contract and open in the
	// code is worse than an open one, because a client is told otherwise.
	if _, err := decode[struct{}](in); err != nil {
		return nil, err
	}
	var notes []note
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, body, created_at FROM `+noteTable+
				` ORDER BY created_at DESC, id DESC LIMIT $1`, maxNotesPerRead)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				n       note
				created time.Time
			)
			if err := rows.Scan(&n.ID, &n.Body, &created); err != nil {
				return err
			}
			n.CreatedAt = created.UTC().Format(time.RFC3339)
			notes = append(notes, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Never nil: the contract declares `notes` required and an array, and a nil
	// slice marshals to null, which every client would then have to special-case.
	if notes == nil {
		notes = []note{}
	}
	return json.Marshal(struct {
		Notes []note `json:"notes"`
	}{Notes: notes})
}

// addNote writes one note and returns it as stored.
func addNote(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := decode[struct {
		Body string `json:"body"`
	}](in)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(args.Body)
	switch {
	case body == "":
		return nil, errors.New("crm-demo: a note needs a body")
	// Runes, not bytes. The contract advertises maxLength: 500, and JSON Schema
	// counts CHARACTERS — so counting len() here refuses a 200-character note
	// in Vietnamese or Japanese while the schema the client was handed says it
	// fits, and the refusal names a length the author cannot see anywhere.
	case utf8.RuneCountInString(body) > maxNoteBody:
		return nil, fmt.Errorf("crm-demo: a note is at most %d characters, this one is %d", maxNoteBody, utf8.RuneCountInString(body))
	}
	var (
		n       note
		created time.Time
	)
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// RETURNING rather than a second read: the id and the timestamp are the
		// database's, and reading them back in another statement would be a
		// second answer that could differ from the row actually written.
		// kind is written rather than left to the column default. The default
		// would produce the same row, but the heartbeat's prune selects on this
		// column — so which kind a statement creates is exactly the fact a
		// reader of this insert needs, and a default states it somewhere else.
		return tx.QueryRow(ctx,
			`INSERT INTO `+noteTable+` (workspace_id, kind, body) VALUES (`+callerWorkspace+`, $1, $2)
			 RETURNING id::text, body, created_at`, kindNote, body).Scan(&n.ID, &n.Body, &created)
	})
	if err != nil {
		return nil, err
	}
	n.CreatedAt = created.UTC().Format(time.RFC3339)
	return json.Marshal(n)
}

// removeNote deletes one note by id and reports whether it removed anything.
//
// An id this workspace does not hold is `removed: false`, not an error: the
// policy makes another tenant's row invisible rather than forbidden, so "no
// such note here" and "no such note anywhere" are the same answer, and the one
// this unit is entitled to give.
func removeNote(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := decode[struct {
		ID string `json:"id"`
	}](in)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("crm-demo: removing a note needs its id")
	}
	// Checked HERE, before the transaction, and the contract declares the same
	// shape (api/crm.yaml, format: uuid with a pattern). Leaving it to the
	// `::uuid` cast below means a client that sent schema-valid input — the
	// contract said "string" — gets a 22P02 from Postgres instead of the
	// documented `{"removed": …}`, and a refusal class this unit cannot express
	// (issue #657) answers 500. An id that is well-formed and simply not here
	// is still `removed: false`; that is a different question, answered below.
	if !isCanonicalUUID(id) {
		return nil, fmt.Errorf("crm-demo: %q is not a note id — an id is a canonical UUID, as the contract declares", id)
	}
	var affected int64
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		affected, err = tx.Exec(ctx, `DELETE FROM `+noteTable+` WHERE id = $1::uuid`, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Removed bool `json:"removed"`
	}{Removed: affected > 0})
}

// decode reads a handler's arguments strictly.
//
// DisallowUnknownFields, because the contract declares every request schema
// with additionalProperties: false and nothing between the client and this
// function enforces it: a caller sending `bdy` would otherwise write an empty
// note and be told it succeeded.
//
// It is NOT sufficient on its own, which is why checkArgumentObject runs first.
// encoding/json matches field names case-INSENSITIVELY, tolerates a repeated
// member, accepts `null` where an object is declared, and stops reading after
// the first value. Each of those admits a document the published schema does
// not, and three of the four decide which value a mutation stores.
func decode[T any](in json.RawMessage) (T, error) {
	var out T
	if err := checkArgumentObject[T](in); err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(string(in)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("crm-demo: the arguments are not the declared shape: %w", err)
	}
	// EOF after the first value. encoding/json decodes ONE value and stops, so
	// `{} {"body":"x"}` reads as the empty object and the rest is discarded —
	// two documents where the contract declares one, with the operation acting
	// on whichever the decoder happened to reach first.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return out, errors.New("crm-demo: the arguments carry a second JSON value — the contract declares one object")
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
		return errors.New("crm-demo: the arguments are not the declared shape: a JSON object is required")
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
			return fmt.Errorf("crm-demo: the arguments are not the declared shape: unknown field %q — a declared name is matched byte for byte, so a case-variant of one is not one of them", key)
		case seen[key]:
			return fmt.Errorf("crm-demo: the arguments are not the declared shape: field %q appears twice — which copy wins is a decoder's choice, not the contract's", key)
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

// declaredJSONNames reads T's json tags. T is always a struct literal declared
// a few lines above its call site, so a non-struct here is a programming error
// that shows up as an empty set and a refusal of every key.
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

// isCanonicalUUID reports whether s is the hyphenated 8-4-4-4-12 hex form.
//
// Hand-written rather than a dependency: the tier's published surface is
// stdlib-only and a unit that pulled in a UUID library to check thirty-six
// bytes would be spending a supply-chain decision on it. Deliberately strict —
// no braces, no urn: prefix, no uppercase-insensitivity beyond hex digits —
// because the ids this unit hands out are the ones Postgres printed, and those
// are exactly this shape.
func isCanonicalUUID(s string) bool {
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
