// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

// The notepad itself: the three operations over ext_crm_demo_note, which are
// what make the migrations layer and the RBAC object observable. None of them
// names a workspace — the Runtime pins the transaction to the invocation's
// tenant before the first statement runs, and the table's policy holds whatever
// SQL these functions write.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
func listNotes(ctx context.Context, rt extension.Runtime, _ json.RawMessage) (json.RawMessage, error) {
	// The argument document is ignored rather than decoded: the contract
	// declares an empty object with additionalProperties: false, so there is
	// nothing to read and nothing that could fail.
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
	case len(body) > maxNoteBody:
		return nil, fmt.Errorf("crm-demo: a note is at most %d characters, this one is %d", maxNoteBody, len(body))
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
	if strings.TrimSpace(args.ID) == "" {
		return nil, errors.New("crm-demo: removing a note needs its id")
	}
	var affected int64
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// $1::uuid, not $1: the id arrives as contract text, and an unparseable
		// one is a 22P02 from Postgres rather than a silent no-op.
		affected, err = tx.Exec(ctx, `DELETE FROM `+noteTable+` WHERE id = $1::uuid`, args.ID)
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
func decode[T any](in json.RawMessage) (T, error) {
	var out T
	dec := json.NewDecoder(strings.NewReader(string(in)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("crm-demo: the arguments are not the declared shape: %w", err)
	}
	return out, nil
}
