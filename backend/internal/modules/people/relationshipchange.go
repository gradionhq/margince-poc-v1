// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What CHANGED about a contact's relationship, gathered for the pure
// derivation in shared/kernel/relstrength.
//
// No table backs this and none should. The arithmetic that produces the
// current score is a fold over the person's own interactions, so folding the
// same curve over a window that ends in the past recovers what the score WAS —
// and the difference is the change. A stored change log would be a second copy
// of a fact the activities already carry, with its own erasure surface, its own
// replay-safety question and its own way of disagreeing with the timeline
// beside it.
//
// The cost is one extra pass over one person's interactions, which is the same
// order as the timeline query rendered next to it.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// PersonRelationshipChangesTx reports what has happened to this contact's
// relationship, inside a caller's transaction.
//
// Row-scoped exactly like the score it explains: a contact the caller cannot
// read yields a not-found, never an empty list, because an empty list is a
// claim about a record whose existence is being hidden.
func (s *Store) PersonRelationshipChangesTx(
	ctx context.Context,
	tx pgx.Tx,
	personID ids.PersonID,
	now time.Time,
) ([]relstrength.Change, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
		return nil, err
	}
	in, err := changeInputs(ctx, tx, personID, now)
	if err != nil {
		return nil, err
	}
	return relstrength.Changes(in, now), nil
}

// changeInputs gathers the present fold, the same fold as it stood one
// comparison window ago, and the two timestamps a returning reply is measured
// between.
func changeInputs(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time) (relstrength.ChangeInputs, error) {
	asOf := now.AddDate(0, 0, -relstrength.ComparisonDays)

	var in relstrength.ChangeInputs
	// One pass over the person's qualifying interactions answers both windows.
	// The earlier fold's own last-interaction is the last one BEFORE asOf, not
	// the overall one — inheriting today's recency would make every band look
	// unchanged and the whole derivation silent.
	if err := tx.QueryRow(ctx, `
		SELECT max(a.occurred_at),
		       count(*) FILTER (WHERE a.occurred_at >= $2),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'inbound'),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'outbound'),
		       max(a.occurred_at) FILTER (WHERE a.occurred_at < $3),
		       count(*) FILTER (WHERE a.occurred_at >= $4 AND a.occurred_at < $3),
		       count(*) FILTER (WHERE a.occurred_at >= $4 AND a.occurred_at < $3 AND a.direction = 'inbound'),
		       count(*) FILTER (WHERE a.occurred_at >= $4 AND a.occurred_at < $3 AND a.direction = 'outbound'),
		       max(a.occurred_at) FILTER (WHERE a.direction = 'inbound')
		  FROM activity a
		  JOIN activity_link l ON l.activity_id = a.id AND l.person_id = $1
		 WHERE a.kind IN `+strengthKinds+` AND a.archived_at IS NULL`,
		personID,
		now.AddDate(0, 0, -relStrengthWindowDays),
		asOf,
		asOf.AddDate(0, 0, -relStrengthWindowDays),
	).Scan(
		&in.Current.LastInteraction, &in.Current.Count90d, &in.Current.Inbound90d, &in.Current.Outbound90d,
		&in.Previous.LastInteraction, &in.Previous.Count90d, &in.Previous.Inbound90d, &in.Previous.Outbound90d,
		&in.LatestInbound,
	); err != nil {
		return relstrength.ChangeInputs{}, fmt.Errorf("people: reading a contact's relationship history: %w", err)
	}

	if in.LatestInbound == nil {
		return in, nil
	}
	// The far side of the silence their reply broke. A separate statement
	// because it is bounded by a value the first one returns.
	if err := tx.QueryRow(ctx, `
		SELECT max(a.occurred_at)
		  FROM activity a
		  JOIN activity_link l ON l.activity_id = a.id AND l.person_id = $1
		 WHERE a.kind IN `+strengthKinds+` AND a.archived_at IS NULL
		   AND a.occurred_at < $2`,
		personID, *in.LatestInbound,
	).Scan(&in.PrecedingInteraction); err != nil {
		return relstrength.ChangeInputs{}, fmt.Errorf("people: reading what preceded a contact's last reply: %w", err)
	}
	return in, nil
}
