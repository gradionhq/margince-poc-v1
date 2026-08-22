// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The buyer's one self-service recovery: asking for a fresh link by email.
//
// Anonymous and deliberately uninformative. The caller learns nothing from the
// response — the handler answers 202 whether the address is known or not — and
// the mail goes to the address itself, which is the one party entitled to know
// they are in a room.
//
// It reissues ONLY where no working credential stands. A reissue retires the
// previous link, and this path is anonymous: if it retired a live link, anyone
// who knew a buyer's address could kill their access at will, and a relay
// failure after the retire would leave them with nothing. So a buyer whose link
// still works gets no new one (and no signal that it still works); a buyer
// whose link lapsed or was used gets a fresh one.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// linkRequestPrincipal is the actor a self-service reissue is attributed to.
// No human acted on the seller's side, and the buyer is not yet authenticated
// when they ask, so the installation itself records the reissue — the same
// posture the other anonymous edges take.
var linkRequestPrincipal = principal.Principal{
	Type: principal.PrincipalSystem,
	ID:   "system:deal_room_link_request",
}

// ReissueByEmail mints a fresh credential for every live participant with this
// address whose room can still be entered, retiring whatever stood before. The
// caller binds linkRequestPrincipal first; this refuses any other actor, so a
// buyer session can never reach it.
//
// "Can still be entered" is the exchange rule — every state but archived —
// because a buyer of a closed room is still entitled to read it. The returned
// list is for the handler to MAIL, and for nothing else: it never reaches the
// response body.
func (s *Store) ReissueByEmail(ctx context.Context, email string) ([]IssuedInvitation, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return nil, err
	}
	if actor.ID != linkRequestPrincipal.ID {
		return nil, apperrors.ErrPermissionDenied
	}
	by := actor.ID
	var out []IssuedInvitation
	err = s.tx(ctx, func(tx pgx.Tx) error {
		seats, err := liveSeatsFor(ctx, tx, email)
		if err != nil {
			return err
		}
		for _, seat := range seats {
			issued, err := reissueFor(ctx, tx, seat, by)
			if err != nil {
				return err
			}
			out = append(out, issued)
		}
		return nil
	})
	return out, err
}

// liveSeatsFor finds every (participant, room) this address may still enter and
// currently holds no exchangeable credential for.
func liveSeatsFor(ctx context.Context, tx pgx.Tx, email string) ([]crmcontracts.DealRoomParticipant, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM %s
		   JOIN deal_room r ON r.id = p.room_id
		  WHERE p.email = $%d AND p.revoked_at IS NULL AND NOT p.preview
		    AND r.archived_at IS NULL AND r.state <> 'archived'
		    AND NOT EXISTS (SELECT 1 FROM deal_room_invitation live
		                     WHERE live.participant_id = p.id AND live.consumed_at IS NULL
		                       AND live.superseded_at IS NULL AND live.expires_at > now())
		  ORDER BY p.created_at`,
		participantColumns, participantFrom, arg(email)), args...)
	if err != nil {
		return nil, fmt.Errorf("find deal room seats by email: %w", err)
	}
	defer rows.Close()
	var out []crmcontracts.DealRoomParticipant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room seats by email: %w", err)
	}
	return out, nil
}

// reissueFor is the resend the seller's side performs, minus the seat checks
// that a seller's session would carry — the participant row itself is the
// authority here, and it was found by the address the mail will go to.
func reissueFor(ctx context.Context, tx pgx.Tx, seat crmcontracts.DealRoomParticipant, by string) (IssuedInvitation, error) {
	participantID := ids.From[ids.DealRoomParticipantKind](ids.UUID(seat.Id))
	roomID := ids.From[ids.DealRoomKind](ids.UUID(seat.RoomId))
	if err := lockParticipant(ctx, tx, participantID); err != nil {
		return IssuedInvitation{}, err
	}
	raw, digest, err := mintCredential()
	if err != nil {
		return IssuedInvitation{}, err
	}
	expiresAt, err := issueCredential(ctx, tx, participantID, digest, by)
	if err != nil {
		return IssuedInvitation{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "invite", participantObject, participantID.UUID,
		nil, map[string]any{fieldRoomID: roomID.UUID, "resent": true, "self_service": true})
	if err != nil {
		return IssuedInvitation{}, fmt.Errorf("audit deal room link request: %w", err)
	}
	// The event is the resend's: a fresh credential on request, and the room's
	// identity read by its own id rather than through the seller's scoped read.
	var room crmcontracts.DealRoom
	room.Id = seat.RoomId
	if err := tx.QueryRow(ctx, `SELECT deal_id FROM deal_room WHERE id = $1`, roomID).Scan(&room.DealId); err != nil {
		return IssuedInvitation{}, fmt.Errorf("read deal room for its reissue event: %w", err)
	}
	if err := emitCredentialReissued(ctx, tx, auditID, room, participantID, reasonResent); err != nil {
		return IssuedInvitation{}, err
	}
	current, err := readParticipant(ctx, tx, roomID, participantID)
	if err != nil {
		return IssuedInvitation{}, err
	}
	return IssuedInvitation{Participant: current, Credential: raw, ExpiresAt: expiresAt}, nil
}

// NoteLinkRequest stamps every live seat with this address as having asked
// for a link, whether or not a credential can then be mailed: the seat that
// still holds a live link gets no mail, an installation without a relay mails
// nothing at all, and in both the seller is the only one who can hand that
// buyer a link by hand — so the ask has to be visible to them. The caller
// binds linkRequestPrincipal first; any other actor is refused.
//
// Audited per seat with the timestamp before and after, and no event: a
// buyer asking for a link is not a change to the room's record.
func (s *Store) NoteLinkRequest(ctx context.Context, email string) error {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	if actor.ID != linkRequestPrincipal.ID {
		return apperrors.ErrPermissionDenied
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		seats, err := stampLinkRequest(ctx, tx, email)
		if err != nil {
			return err
		}
		for _, st := range seats {
			if _, err := storekit.Audit(ctx, tx, "update", participantObject, st.id,
				map[string]any{fieldRoomID: st.roomID, "link_requested_at": st.before},
				map[string]any{fieldRoomID: st.roomID, "link_requested_at": st.after}); err != nil {
				return err
			}
		}
		return nil
	})
}

// linkRequestStamp is one seat's stamp, before and after, for its audit image.
type linkRequestStamp struct {
	id, roomID ids.UUID
	before     *time.Time
	after      time.Time
}

func stampLinkRequest(ctx context.Context, tx pgx.Tx, email string) ([]linkRequestStamp, error) {
	rows, err := tx.Query(ctx, `
		UPDATE deal_room_participant p SET link_requested_at = now()
		  FROM deal_room r, deal_room_participant was
		 WHERE r.id = p.room_id AND was.id = p.id
		   AND p.email = $1 AND p.revoked_at IS NULL AND NOT p.preview
		   AND r.archived_at IS NULL AND r.state <> 'archived'
		 RETURNING p.id, p.room_id, was.link_requested_at, p.link_requested_at`, email)
	if err != nil {
		return nil, fmt.Errorf("note a deal room link request: %w", err)
	}
	defer rows.Close()
	var seats []linkRequestStamp
	for rows.Next() {
		var st linkRequestStamp
		if err := rows.Scan(&st.id, &st.roomID, &st.before, &st.after); err != nil {
			return nil, fmt.Errorf("scan a deal room link request: %w", err)
		}
		seats = append(seats, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("note a deal room link request: %w", err)
	}
	return seats, nil
}
