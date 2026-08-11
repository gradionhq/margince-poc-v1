// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Naming who a reply is TO.
//
// A drafter that knows who is sending and not who is receiving greets the only
// name it has, which is the sender's — producing a message addressed to its own
// author. That is not hypothetical: the certification judge floored a draft for
// it, and it is the one defect holding the reply site back from certified.
//
// The name is not on the activity. It is on the person the activity is linked
// to, which is the same link that files the message on their page, so this
// reads the link rather than inventing a second notion of "who this was with".

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ReplyRecipient is who a reply to an activity is addressed to.
//
// Both fields may be empty, and that is an answer rather than a failure: an
// activity linked to no person, or to one this caller cannot read, leaves the
// drafter with no name — and a draft that opens "Hallo," is correct there,
// where one that guesses is not.
type ReplyRecipient struct {
	// FullName as recorded, for the drafter to greet by.
	FullName string
	// FirstName is what a greeting actually uses. Split here rather than in
	// the prompt: a model asked to shorten a name will shorten
	// "Dr. Anne-Marie Weiß-Konrad" differently on every call.
	FirstName string
}

// ReplyRecipientFor names the person a reply to this activity is written to.
//
// It carries the row-scope gate the same way every other read here does: the
// activity is gated by the link-walk, and the person by their own visibility,
// so an activity the caller cannot reach and a person they cannot read both
// answer the empty recipient rather than leaking a name.
//
// One person, not a list. A reply is written to somebody, and where an activity
// links several people the first by link order is the one the timeline itself
// treats as the counterparty. A group thread is a real shape this does not model
// yet; it degrades to greeting the first person rather than to greeting nobody.
func (s *Store) ReplyRecipientFor(ctx context.Context, id ids.ActivityID) (ReplyRecipient, error) {
	var out ReplyRecipient
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The activity read applies the link-walk scope. Reaching a person
		// through an activity the caller cannot see would answer a name their
		// own scope withholds.
		if _, err := readActivity(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}

		var personID ids.UUID
		err := tx.QueryRow(ctx, `
			SELECT l.person_id
			  FROM activity_link l
			 WHERE l.activity_id = $1 AND l.person_id IS NOT NULL
			 ORDER BY l.created_at, l.id
			 LIMIT 1`, id).Scan(&personID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // linked to no person: no name, which is an answer
		}
		if err != nil {
			return err
		}

		// The person carries capture privacy, so their own gate decides
		// whether this caller may be told the name. Out of scope reads as
		// ErrNotFound, and here that means an unnamed greeting rather than a
		// refused draft.
		if err := auth.EnsureVisibleLive(ctx, tx, "person", personID); err != nil {
			if errors.Is(err, apperrors.ErrNotFound) {
				return nil
			}
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT full_name, coalesce(first_name, '') FROM person WHERE id = $1`,
			personID).Scan(&out.FullName, &out.FirstName)
	})
	if err != nil {
		return ReplyRecipient{}, err
	}
	return out, nil
}
