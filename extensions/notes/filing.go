// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// filingSource is what the activity's provenance says about where it came from.
//
// The contract leaves `source` free-form except for the reserved `mirror:`
// namespace, and the value matters to a human reading a timeline: it is the
// only place the row says a unit filed it rather than a person typing into the
// activity screen. The AUDIT row carries the same fact structurally (the core
// stamps evidence.extension from the invocation); this is the copy the timeline
// itself can show.
const filingSource = "extension:notes"

// fileNote writes one note AND files it to a record: the unit's own row and a
// core activity against the subject the caller names, in one transaction.
//
// This is what the governed port is for, and the shape is the point rather than
// the feature. Everything about the activity — that the caller was allowed to
// create one, that the subject exists and is theirs to see, the audit row, the
// outbox event, the captured_by stamp — belongs to the core write the port
// delegates to. What this unit contributes is its own row and the decision to
// pair them.
//
// The pairing is atomic in both directions, which is why the activity is
// created FIRST: the note's receipt column can then be written in the insert
// that creates it, so there is no window where a note exists without its
// receipt. If the note's own insert fails afterwards, the transaction takes the
// activity with it — a filed note that lost its note would be a timeline entry
// nobody can trace back.
func fileNote(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := decode[struct {
		Body        string `json:"body"`
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
	}](in)
	if err != nil {
		return nil, err
	}
	body, err := noteBody(args.Body)
	if err != nil {
		return nil, err
	}
	subject := crm.CreateActivityRequestLinksEntityType(args.SubjectType)
	if !subject.Valid() {
		// The contract's enum already refuses this at the seam, so reaching it
		// means the schema and this handler disagree. Naming the value rather
		// than the set: the set is in the schema the caller was handed.
		return nil, fmt.Errorf("notes: %q is not a record a note can be filed to", args.SubjectType)
	}
	if !isCanonicalUUID(args.SubjectID) {
		return nil, fmt.Errorf("notes: %q is not a record id — an id is a canonical UUID, as the contract declares", args.SubjectID)
	}

	authorID, authorIsAgent := callerAuthor(rt.Caller())
	var n note
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		filed, err := tx.Core().Activities().Create(ctx, crm.CreateActivityRequest{
			Kind:   crm.CreateActivityRequestKindNote,
			Body:   &body,
			Source: filingSource,
		}.LinkTo(subject, args.SubjectID))
		if err != nil {
			return err
		}
		var scanErr error
		n, scanErr = scanNote(tx.QueryRow(ctx,
			`INSERT INTO `+noteTable+` (workspace_id, kind, body, author_user_id, author_is_agent, filed_activity_id)
			 VALUES (`+callerWorkspace+`, $1, $2, $3::uuid, $4::boolean, $5::uuid)
			 RETURNING `+noteColumns, string(kindNote), body, authorID, authorIsAgent, filed.Id).Scan)
		if scanErr != nil {
			return scanErr
		}
		// The note's OWN ledger row, beside the activity's. The port wrote one
		// for the activity above — that record's history belongs to the core —
		// and this is the notepad's: one write recorded once on each side of
		// the seam, so either can be read without the other.
		//
		// The RECORD the note was filed to rides in the evidence rather than
		// the image, because the note's own columns say which ACTIVITY it
		// reached and not which record that activity sits on.
		detail, err := json.Marshal(struct {
			SubjectType string `json:"subject_type"`
			SubjectID   string `json:"subject_id"`
		}{SubjectType: args.SubjectType, SubjectID: args.SubjectID})
		if err != nil {
			return err
		}
		payload, err := notePayload(n)
		if err != nil {
			return err
		}
		return recordNote(ctx, tx, extension.AuditCreate, eventNoteFiled, nil, &n, detail, payload)
	})
	if err != nil {
		return nil, filingRefusal(err)
	}
	return json.Marshal(n)
}

// filingRefusal turns the port's refusal classes into what this unit says about
// them. It keeps the sentinel — a caller inspecting with errors.Is still can —
// and adds the one thing the port cannot know: which of ITS operations was
// refused, and what the person driving it should do next.
func filingRefusal(err error) error {
	switch {
	case errors.Is(err, extension.ErrNotFound):
		return fmt.Errorf("%w: that record is not there, or is not yours to see", err)
	case errors.Is(err, extension.ErrForbidden):
		return fmt.Errorf("%w: filing a note writes a core activity, which needs the activity permission as well as this unit's own", err)
	case errors.Is(err, extension.ErrOverlayUnsupported):
		return fmt.Errorf("%w: this workspace's records live in the connected system, so a note cannot be filed to one — add it to the notepad instead", err)
	}
	return err
}
