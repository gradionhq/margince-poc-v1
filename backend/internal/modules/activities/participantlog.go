// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Participants for a HAND-LOGGED activity (ACT-DDL-3 / ADR-0078).
//
// Capture stamps participants for mail it ingests. A rep who logs a call or a
// meeting themselves goes through this path instead, and it has to record the
// same fact or the two halves of the timeline answer "who was in it"
// differently — which would show up as a colleague's relationship silently
// depending on HOW the conversation was recorded.
//
// Both ends are known here without inference. The human logging it is the
// authenticated principal, and the counterparty is whichever person the caller
// linked — a link the store has already put through the row-scope gate.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// interactionKinds are the activity kinds that represent a real exchange. A
// task or a note is not one: a task is intent and a note is a record of
// thinking, and neither means the two people spoke. Recording participants for
// them would let a rep's own to-do list score as a relationship.
var interactionKinds = map[string]bool{kindEmail: true, kindCall: true, kindMeeting: true}

// The activity kinds and the link entity type this file names, as constants so
// a typo is a compile error rather than a participant row that silently never
// gets written.
const (
	kindEmail        = "email"
	kindCall         = "call"
	kindMeeting      = "meeting"
	linkEntityPerson = "person"
)

// stampLoggedParticipants records who was in a hand-logged interaction: the
// human who logged it, and the people it was linked to.
//
// It is silent for a non-interaction kind and for an activity with no person
// link — an unlinked note is a workspace-shared thought, not a conversation
// with anybody.
func stampLoggedParticipants(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, kind string, direction *string, links []ActivityLinkInput) error {
	if !interactionKinds[kind] {
		return nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		// A logging path with no human behind it (a system backfill, an agent
		// acting on its own) records no our-side participant rather than
		// attributing the conversation to nobody. The person side below still
		// stands, so the timeline keeps what it knows.
		return stampLoggedCounterparties(ctx, tx, activityID, direction, links, ids.Nil)
	}
	return stampLoggedCounterparties(ctx, tx, activityID, direction, links, actor.UserID)
}

// stampLoggedCounterparties writes the rows, with the roles the direction
// implies — our side sends on outbound and receives on inbound, mirroring
// exactly what capture stamps, so a logged call and a captured mail fold
// through the same arithmetic.
func stampLoggedCounterparties(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, direction *string, links []ActivityLinkInput, userID ids.UUID) error {
	// A logged meeting often carries no direction at all — nobody "sends" a
	// meeting. It defaults to the outbound roles, which is what a rep logging
	// their own call means, and the fold treats an undirected interaction as
	// evidence of contact that says nothing about reciprocity.
	ourRole, theirRole := "from", "to"
	if direction != nil && *direction == "inbound" {
		ourRole, theirRole = "to", "from"
	}
	if userID != ids.Nil {
		if err := insertLoggedParticipant(ctx, tx, activityID, ourRole, &userID, nil); err != nil {
			return err
		}
	}
	for _, link := range links {
		if link.EntityType != linkEntityPerson {
			continue
		}
		person := link.EntityID
		if err := insertLoggedParticipant(ctx, tx, activityID, theirRole, nil, &person); err != nil {
			return err
		}
	}
	return nil
}

// insertLoggedParticipant writes one row, idempotently against the ACT-DDL-3
// uniqueness index — the log path has its own replay guard, and this must not
// become a second place that can fail on a retry.
func insertLoggedParticipant(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, role string, userID, personID *ids.UUID) error {
	// The user arm is written through a SELECT over app_user rather than a
	// bare VALUES: a principal's UserID is not guaranteed to name a workspace
	// member. An agent or a service principal can carry an id that belongs to
	// no app_user row, and the composite FK rejects it — which would fail the
	// whole write for a participant row that is a nicety, not the point of the
	// operation. Guarding here rather than pre-checking also avoids a race
	// with a member being archived between the check and the insert.
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_participant (workspace_id, activity_id, user_id, person_id, role)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4
		 WHERE $2::uuid IS NULL
		    OR EXISTS (SELECT 1 FROM app_user u WHERE u.id = $2)
		ON CONFLICT DO NOTHING`, activityID, userID, personID, role); err != nil {
		return fmt.Errorf("activities: recording who was in a logged interaction: %w", err)
	}
	return nil
}
