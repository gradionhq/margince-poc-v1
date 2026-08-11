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
// The name is not on the activity. It is on the person the message was WITH,
// which activity_participant records with the role they held, so this reads the
// participants rather than inventing a second notion of "who this was with".

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// One person, not a list. A reply is written to somebody, and the counterparty
// is read from the message's PARTICIPANTS by role — the sender of an inbound
// message first, then an addressee, then anyone else on it — falling back to the
// activity link only for rows that carry no participants. A group thread is a
// real shape this does not model yet; it degrades to greeting the most likely
// counterparty rather than to greeting nobody.
func (s *Store) ReplyRecipientFor(ctx context.Context, id ids.ActivityID) (ReplyRecipient, error) {
	// Naming a person is a person read, so it takes the person read GRANT as
	// well as the row scope. A caller permitted to read activities but not
	// people would otherwise be told a name through this door that the people
	// surface refuses them.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return ReplyRecipient{}, err
	}

	var out ReplyRecipient
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The activity read applies the link-walk scope. Reaching a person
		// through an activity the caller cannot see would answer a name their
		// own scope withholds.
		if _, err := readActivity(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}

		// ONE statement: the row scope, the archived test and the name are
		// decided together. Split across two, a person archived or flipped to
		// owner-private between them still yields their name under READ
		// COMMITTED — a race that is small, real, and needless when the scope
		// composes into the predicate.
		args := []any{id}
		scope, err := auth.ScopeClauseFor(ctx, "person", "p", func(v any) int {
			args = append(args, v)
			return len(args)
		})
		if err != nil {
			return err
		}
		// The counterparty comes from the PARTICIPANTS, which record who was
		// on the message and in what role, and only falls back to the link
		// when an activity carries none. A link says what a message is about;
		// a CC'd colleague and the person who wrote it are both linked, and
		// picking whichever was linked first addresses the reply to whoever
		// happens to sort earliest.
		//
		// Rank: the sender of an inbound message is who we are answering, then
		// a "to" recipient (our own outbound, where the addressee is the
		// counterparty), then anyone else on it, then the bare link.
		q := `
			SELECT p.full_name, coalesce(p.first_name, '')
			  FROM person p
			  JOIN (
			       SELECT person_id,
			              CASE role WHEN 'from' THEN 1 WHEN 'to' THEN 2 ELSE 3 END AS rank,
			              created_at, id
			         FROM activity_participant
			        WHERE activity_id = $1 AND person_id IS NOT NULL
			       UNION ALL
			       SELECT person_id, 4 AS rank, created_at, id
			         FROM activity_link
			        WHERE activity_id = $1 AND person_id IS NOT NULL
			  ) c ON c.person_id = p.id
			 WHERE p.archived_at IS NULL`
		if scope != "" {
			q += ` AND (` + scope + `)`
		}
		q += ` ORDER BY c.rank, c.created_at, c.id LIMIT 1`

		err = tx.QueryRow(ctx, q, args...).Scan(&out.FullName, &out.FirstName)
		if errors.Is(err, pgx.ErrNoRows) {
			// Linked to nobody, or to a person out of scope. Both mean no
			// name, which the floor renders as an unnamed greeting rather
			// than a refused draft.
			return nil
		}
		return err
	})
	if err != nil {
		return ReplyRecipient{}, err
	}
	return out, nil
}

// GreetingName is the one name a floor draft greets by, or empty.
//
// The same resolution every drafting transport uses, so the HTTP fallback, the
// MCP tool and the model drafter's own degrade path cannot greet three
// different ways. A failure is empty rather than an error: this is a greeting,
// and a draft with no name is a draft.
func (s *Store) GreetingName(ctx context.Context, id ids.ActivityID) string {
	recipient, err := s.ReplyRecipientFor(ctx, id)
	if err != nil {
		return ""
	}
	if recipient.FirstName != "" {
		return recipient.FirstName
	}
	return recipient.FullName
}
