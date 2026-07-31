// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Who was in the interaction (ACT-DDL-3 / ADR-0078). The activity row records
// that a message happened; activity_link records which RECORDS it concerns.
// Neither says which of OUR people was in it, and that is the whole reason
// "who on our team knows this contact" cannot be answered today.
//
// Capture is the one place that knows. The connector principal carries the
// granting human's id — the mailbox owner, per-user-per-provider from
// capture_connection — so the our-side participant is a fact at ingest, not an
// inference from a `captured_by` string that connector mail never sets to a
// human in the first place.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Participant roles. The set is closed at the database (the ACT-DDL-3 CHECK);
// these constants are the Go spelling of it, so a typo is a compile error
// rather than a constraint violation at 3am.
const (
	roleFrom = "from"
	roleTo   = "to"
)

// stampCaptureParticipants records the two ends of a captured message: the
// mailbox owner whose connection produced it, and the counterparty it was
// exchanged with.
//
// The counterparty lands as an ADDRESS, not a person: capture creates the
// person after this transaction commits (the tiered creation gate may also
// decide not to create one at all), so the address is the honest answer at
// this point. promoteParticipantToPerson upgrades the row later, when and if
// an identity resolves. Recording the address now rather than waiting is what
// keeps a suppressed or deferred counterparty from vanishing from the record
// of who was in the conversation.
//
// Direction decides the roles and nothing else: on an outbound message our
// user is the sender, on an inbound one they are the recipient. That
// distinction is what lets the edge derivation tell a real exchange from a
// hundred unanswered sends.
func stampCaptureParticipants(
	ctx context.Context,
	tx pgx.Tx,
	activityID ids.ActivityID,
	ownerUserID ids.UUID,
	direction string,
	counterpartyEmail string,
) error {
	ourRole, theirRole := roleFrom, roleTo
	if direction == connector.DirectionInbound {
		ourRole, theirRole = roleTo, roleFrom
	}

	if ownerUserID != ids.Nil {
		if err := insertParticipant(ctx, tx, activityID, ourRole, &ownerUserID, nil, ""); err != nil {
			return fmt.Errorf("capture: stamping the mailbox owner as a participant: %w", err)
		}
	}
	// Normalized the same way person_email is, so the promotion below and the
	// erasure lookup both match without a runtime case fold.
	address := strings.ToLower(strings.TrimSpace(counterpartyEmail))
	if address != "" {
		if err := insertParticipant(ctx, tx, activityID, theirRole, nil, nil, address); err != nil {
			return fmt.Errorf("capture: stamping the counterparty as a participant: %w", err)
		}
	}
	return nil
}

// insertParticipant writes one participant row, idempotently. Capture's sync
// loop is at-least-once and its whole write path is keyed on the source
// natural key, so a replay must add nothing — hence ON CONFLICT DO NOTHING
// against the ACT-DDL-3 uniqueness index rather than a prior SELECT, which
// would race with a concurrent replay of the same message.
func insertParticipant(
	ctx context.Context,
	tx pgx.Tx,
	activityID ids.ActivityID,
	role string,
	userID *ids.UUID,
	personID *ids.PersonID,
	address string,
) error {
	// The user arm rides a SELECT over app_user for the same reason the logged
	// path does: a principal's UserID need not name a workspace member, and
	// the composite FK would reject it — failing an ingest we have already
	// read off the wire, over a participant row that is a nicety rather than
	// the point of the write.
	_, err := tx.Exec(ctx, `
		INSERT INTO activity_participant (workspace_id, activity_id, user_id, person_id, address, role)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, NULLIF($4, ''), $5
		 WHERE $2::uuid IS NULL
		    OR EXISTS (SELECT 1 FROM app_user u WHERE u.id = $2)
		ON CONFLICT DO NOTHING`,
		activityID, userID, personID, address, role)
	return err
}

// actorUserID is the mailbox owner behind the acting connector principal —
// the granting human the registry stamped onto it (capture_connection is
// per-user-per-provider). It answers ids.Nil when no actor is bound, which the
// caller treats as "no our-side participant" rather than an error: the sink
// has already refused a non-connector principal by the time this runs, so a
// zero here means a code path that built a principal without a grantor, and
// losing one participant row is a better outcome than failing a message we
// have already read off the wire.
func actorUserID(ctx context.Context) ids.UUID {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ids.Nil
	}
	return actor.UserID
}
