// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The deliverable ADDRESS a reply goes to, as distinct from the name it greets.
//
// ReplyRecipientFor beside this answers a NAME, and a name is all a greeting
// needs — an activity with no resolvable person yields an unnamed greeting and
// that is a correct draft. An address cannot degrade the same way: a send with
// no addressee is not a quieter send, it is a message with nowhere to go, so
// this refuses where the greeting shrugs.
//
// It exists because something has to compose a send with no human at the
// keyboard. Every other caller supplies `to` explicitly — the SPA prefills it,
// an agent reads a draft and passes the address back on the send call — but an
// automation drafts and stages in one pass with nobody in the middle to name
// the recipient, and a staged draft that hides who it will go to is not
// something a human can meaningfully approve.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// NoReplyAddressError refuses to compose a reply that has nowhere to go.
//
// It is a 422 with a field fault rather than a 500: the activity is real and
// the caller is entitled to it, there is simply no counterparty address on it —
// a manually logged call, or a thread whose participants were never captured.
// An automation instance pointed at such a record is misconfigured, and the
// operator can act on that.
type NoReplyAddressError struct{}

func (e *NoReplyAddressError) Error() string {
	return "the message being replied to records no counterparty address to answer; " +
		"a reply can only be composed for a thread that carries one"
}

// FieldFault names the field the caller has to correct.
func (e *NoReplyAddressError) FieldFault() (field, code, message string) {
	return "to", "no_reply_address", e.Error()
}

// ReplyAddressFor answers the single address a reply to this activity is sent
// to, refusing when the thread carries none.
//
// WHOSE address, precisely. The counterparty's — never one of our own. That
// distinction is why this cannot simply reuse ReplyRecipientFor's ranking: on an
// OUTBOUND message the `from` participant is us, so a rank that puts `from`
// first would answer with our own address and cheerfully mail ourselves. A
// participant carrying user_id is one of this installation's own people, and is
// excluded here for exactly that reason.
//
// Among what remains the rank is the same shape as the greeting's, and for the
// same reason: on an inbound message the sender is who we are answering, and on
// our own outbound the addressee is. Then anyone else who was on it.
//
// One address, not a list. A group thread is the same shape ReplyRecipientFor
// declines to model, and answering all of them is a different product decision
// from answering the counterparty — this addresses the most likely one and a
// human sees it before anything sends.
func (s *Store) ReplyAddressFor(ctx context.Context, id ids.ActivityID) (string, error) {
	// Reaching a person's address is a person read, exactly as reaching their
	// name is: a caller who may read activities but not people must not be told
	// through this door what the people surface withholds.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return "", err
	}

	var address string
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The link-walk scope decides whether this caller may see the anchor at
		// all. Reading a counterparty off an activity they cannot reach would
		// answer an address their own scope withholds.
		if _, err := readActivityContent(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}

		args := []any{id}
		scope, err := auth.ScopeClauseFor(ctx, "person", "p", func(v any) int {
			args = append(args, v)
			return len(args)
		})
		if err != nil {
			return err
		}

		// Two sources, ranked together rather than tried in sequence. The
		// participant's OWN address is preferred over the person's primary
		// email because it is the address that actually corresponded: a
		// contact who wrote from a second mailbox gets answered where they
		// wrote from, which is what a reply means. The person's primary email
		// is the fallback for a participant recorded by identity alone.
		//
		// The person row-scope applies only to the arm that reads a person. A
		// bare participant address is on the activity the caller already
		// reached and names no person row to be scoped against.
		// The tiebreak columns carry BOTH levels, and they have to. The
		// participant arm ranks by which participant; the person-email arm can
		// return several rows for ONE participant, and those are separated by
		// is_primary and position — the record's own ordering. Ranking the
		// second arm on the participant's created_at/id alone leaves every one
		// of a contact's addresses tied, so "the primary email" becomes whichever
		// row the planner happened to emit: a personal or long-retired address on
		// a business thread, chosen by nothing.
		q := `
			WITH counterparty AS (
			     SELECT person_id, address,
			            CASE role WHEN 'from' THEN 1 WHEN 'to' THEN 2 ELSE 3 END AS rank,
			            created_at, id
			       FROM activity_participant
			      WHERE activity_id = $1
			        AND user_id IS NULL
			)
			SELECT addr FROM (
			     SELECT c.address AS addr, c.rank, 1 AS source,
			            true AS primary_first, 0 AS position, c.created_at, c.id
			       FROM counterparty c
			      WHERE c.address IS NOT NULL AND c.address <> ''
			     UNION ALL
			     SELECT e.email AS addr, c.rank, 2 AS source,
			            e.is_primary AS primary_first, e.position, c.created_at, c.id
			       FROM counterparty c
			       JOIN person p ON p.id = c.person_id
			       JOIN person_email e ON e.person_id = p.id AND e.archived_at IS NULL
			      WHERE c.person_id IS NOT NULL
			        AND p.archived_at IS NULL`
		if scope != "" {
			q += ` AND (` + scope + `)`
		}
		q += `
			) ranked
			 ORDER BY rank, source, primary_first DESC, position, created_at, id, addr
			 LIMIT 1`

		err = tx.QueryRow(ctx, q, args...).Scan(&address)
		if errors.Is(err, pgx.ErrNoRows) {
			return &NoReplyAddressError{}
		}
		if err != nil {
			return fmt.Errorf("resolve the address a reply answers: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if address == "" {
		// Belt and braces against a row that satisfies the predicate with
		// whitespace: an empty addressee reaches the send's own refusal, but it
		// would arrive there as "no recipients" rather than as the honest
		// answer that this thread has no counterparty to answer.
		return "", &NoReplyAddressError{}
	}
	return address, nil
}
