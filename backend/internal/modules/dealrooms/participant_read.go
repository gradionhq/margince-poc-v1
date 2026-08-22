// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The object a participant read gates on. Deciding who is in a room is part of
// running the room, so it takes the room's own grant rather than one of its own.
const participantObject = "deal_room_participant"

// participantColumns projects a participant together with the state of whatever
// credential currently stands for them.
//
// The delivery facts come from the standing invitation joined below, and
// last_seen_at from whichever session was most recent. A participant with
// neither reads as nulls, which the scanner turns into the `none` delivery
// state rather than an absent field.
const participantColumns = `p.id, p.room_id, p.full_name, p.email, p.capability,
	p.invited_by, p.revoked_at, p.source, p.captured_by, p.created_at, p.updated_at,
	live.expires_at, live.sent_at, live.delivered_at, live.failed_at, live.consumed_at,
	(SELECT max(s.last_seen_at) FROM deal_room_session s
	  WHERE s.participant_id = p.id)`

// participantFrom joins each participant to its standing credential, if any.
//
// "Standing" is the same predicate the uq_deal_room_invitation_live index uses,
// so this reads exactly the row that index permits at most one of. A consumed or
// superseded invitation is deliberately outside it: the first means they are
// already in, the second that a newer credential replaced it.
const participantFrom = `deal_room_participant p
	LEFT JOIN LATERAL (
	    SELECT i.expires_at, i.sent_at, i.delivered_at, i.failed_at, i.consumed_at
	      FROM deal_room_invitation i
	     WHERE i.participant_id = p.id
	       AND i.superseded_at IS NULL
	  ORDER BY i.attempt_no DESC
	     LIMIT 1
	) live ON TRUE`

// ListParticipants returns a room's roster.
func (s *Store) ListParticipants(ctx context.Context, roomID ids.DealRoomID, activeOnly bool) ([]crmcontracts.DealRoomParticipant, storekit.Page, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	var out []crmcontracts.DealRoomParticipant
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Reading the room first IS the scope gate: it applies the parent
		// deal's visibility clause, so a roster cannot be read past a room the
		// caller cannot see.
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		var err error
		out, err = participantRows(ctx, tx, roomID, activeOnly)
		return err
	})
	// The roster is small and bounded by how many people a seller invites, so it
	// answers whole rather than paged. The envelope still carries a page object
	// because every list response in this contract does.
	return out, storekit.Page{}, err
}

func participantRows(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, activeOnly bool) ([]crmcontracts.DealRoomParticipant, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := storekit.SQLf("p.room_id = $%d", arg(roomID))
	if activeOnly {
		where += " AND p.revoked_at IS NULL"
	}

	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM %s WHERE %s ORDER BY p.created_at, p.id`,
		participantColumns, participantFrom, where), args...)
	if err != nil {
		return nil, fmt.Errorf("list deal room participants: %w", err)
	}
	defer rows.Close()

	var out []crmcontracts.DealRoomParticipant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deal room participant: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room participants: %w", err)
	}
	return out, nil
}

// readParticipant returns one participant of one room.
//
// The room id is part of the predicate rather than merely checked afterwards, so
// a participant id from another room reads as absent instead of leaking that it
// exists somewhere else.
func readParticipant(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, id ids.DealRoomParticipantID) (crmcontracts.DealRoomParticipant, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM %s WHERE p.id = $%d AND p.room_id = $%d`,
		participantColumns, participantFrom, arg(id), arg(roomID)), args...)

	out, err := scanParticipant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoomParticipant{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.DealRoomParticipant{}, fmt.Errorf("read deal room participant: %w", err)
	}
	return out, nil
}

func scanParticipant(row rowScanner) (crmcontracts.DealRoomParticipant, error) {
	var (
		out        crmcontracts.DealRoomParticipant
		id, roomID ids.UUID
		invitedBy  *ids.UUID
		capability string
		capturedBy string
		delivery   deliveryFacts
	)
	if err := row.Scan(&id, &roomID, &out.FullName, &out.Email, &capability,
		&invitedBy, &out.RevokedAt, &out.Source, &capturedBy, &out.CreatedAt, &out.UpdatedAt,
		&delivery.expiresAt, &delivery.sentAt, &delivery.deliveredAt, &delivery.failedAt,
		&delivery.consumedAt, &out.LastSeenAt); err != nil {
		return crmcontracts.DealRoomParticipant{}, err
	}
	out.Id = openapi_types.UUID(id)
	out.RoomId = openapi_types.UUID(roomID)
	out.Capability = crmcontracts.DealRoomParticipantCapability(capability)
	out.CapturedBy = &capturedBy
	out.CredentialExpiresAt = delivery.expiresAt
	out.DeliveryState = delivery.state(out.RevokedAt != nil)
	if invitedBy != nil {
		u := openapi_types.UUID(*invitedBy)
		out.InvitedBy = &u
	}
	return out, nil
}
