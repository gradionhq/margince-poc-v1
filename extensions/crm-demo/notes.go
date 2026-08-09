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
// It is NOT sufficient on its own, which is why the canonical-key pass runs
// first: encoding/json matches field names case-insensitively, so `BODY` and
// `bOdY` both satisfy DisallowUnknownFields and both set Body. A closed schema
// that admits three spellings of a key is not closed — and the spelling that
// wins between two case-variants in one document is whichever comes last, which
// is a way to change a mutation's value past a reviewer reading the first one.
func decode[T any](in json.RawMessage) (T, error) {
	var out T
	if err := rejectNonCanonicalKeys[T](in); err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(string(in)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("crm-demo: the arguments are not the declared shape: %w", err)
	}
	return out, nil
}

// rejectNonCanonicalKeys refuses a top-level key that is not byte-for-byte one
// of T's declared JSON names. It answers nothing about nested objects, and
// needs to answer nothing: every request schema this unit declares is flat.
//
// An empty or whitespace-only document is left to the decoder, so "no body at
// all" keeps reading as the decoder's error rather than becoming this one's.
func rejectNonCanonicalKeys[T any](in json.RawMessage) error {
	if len(bytes.TrimSpace(in)) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in, &raw); err != nil {
		// Not an object, or not JSON: the decoder below says so better than a
		// key check can, and says it about the shape rather than about a key.
		return nil
	}
	declared := declaredJSONNames[T]()
	for key := range raw {
		if !declared[key] {
			return fmt.Errorf("crm-demo: the arguments are not the declared shape: unknown field %q — a declared name is matched byte for byte, so a case-variant of one is not one of them", key)
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
