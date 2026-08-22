// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// InviteInput admits one named person to a room.
type InviteInput struct {
	FullName   string
	Email      string
	Capability string
	Source     string
}

// IssuedInvitation is a participant and the credential just minted for them.
// The raw credential exists here and in the delivered mail, nowhere else.
type IssuedInvitation struct {
	Participant crmcontracts.DealRoomParticipant
	Credential  string
	ExpiresAt   time.Time
}

// InviteParticipant admits a named person and mints their credential.
//
// Human-only: deciding which outside person may read a deal's material is not a
// judgement an agent makes.
func (s *Store) InviteParticipant(ctx context.Context, roomID ids.DealRoomID, in InviteInput) (IssuedInvitation, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return IssuedInvitation{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return IssuedInvitation{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return IssuedInvitation{}, err
	}
	// Minted before the transaction, so the credential exists in memory whether
	// or not the write lands, and a rollback never leaves a hash whose raw value
	// was handed out.
	raw, digest, err := mintCredential()
	if err != nil {
		return IssuedInvitation{}, err
	}

	var out IssuedInvitation
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = inviteTx(ctx, tx, roomID, in, by, raw, digest)
		return err
	})
	return out, err
}

func inviteTx(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, in InviteInput, by, raw string, digest []byte) (IssuedInvitation, error) {
	room, err := readRoom(ctx, tx, roomID)
	if err != nil {
		return IssuedInvitation{}, err
	}
	if err := ensureDealWritable(ctx, tx, room); err != nil {
		return IssuedInvitation{}, err
	}
	// A finished room admits nobody new. Its content can no longer change, so an
	// invitation into it would hand somebody a link to a room that will never
	// tell them anything further.
	if !publishable(string(room.State)) {
		return IssuedInvitation{}, notAdmitting(string(room.State))
	}

	id := ids.New[ids.DealRoomParticipantKind]()
	email := strings.ToLower(strings.TrimSpace(in.Email))
	_, err = tx.Exec(ctx,
		`INSERT INTO deal_room_participant
		     (id, room_id, full_name, email, capability, invited_by, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, roomID, in.FullName, email, in.Capability, invitingUser(ctx), in.Source, by)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return IssuedInvitation{}, errAlreadyInvited
		}
		return IssuedInvitation{}, fmt.Errorf("insert deal room participant: %w", err)
	}

	expiresAt, err := issueCredential(ctx, tx, id, digest, by)
	if err != nil {
		return IssuedInvitation{}, err
	}

	auditID, err := storekit.Audit(ctx, tx, "invite", participantObject, id.UUID, nil,
		map[string]any{fieldRoomID: roomID.UUID, "email": email, "capability": in.Capability})
	if err != nil {
		return IssuedInvitation{}, fmt.Errorf("audit deal room invite: %w", err)
	}
	invited := crmcontracts.PublicEventDealRoomParticipantInvited{
		DealId:        room.DealId,
		ParticipantId: openapi_types.UUID(id.UUID),
		Capability:    in.Capability,
	}
	// The event names the participant and never the credential: a subscriber
	// able to read the token off the bus would hold everything it grants.
	if err := storekit.EmitEvent(ctx, tx, auditID, roomID.UUID, invited); err != nil {
		return IssuedInvitation{}, fmt.Errorf("emit deal_room.participant_invited: %w", err)
	}

	participant, err := readParticipant(ctx, tx, roomID, id)
	if err != nil {
		return IssuedInvitation{}, err
	}
	return IssuedInvitation{Participant: participant, Credential: raw, ExpiresAt: expiresAt}, nil
}

// issueCredential retires whatever credential stands for this participant and
// records the new one as the next attempt.
//
// Superseding first is what makes "at most one live credential" true rather than
// merely indexed: without it the INSERT would collide with
// uq_deal_room_invitation_live and a resend would fail instead of replacing.
func issueCredential(ctx context.Context, tx pgx.Tx, participantID ids.DealRoomParticipantID, digest []byte, by string) (time.Time, error) {
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_invitation SET superseded_at = now()
		  WHERE participant_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL`,
		participantID); err != nil {
		return time.Time{}, fmt.Errorf("supersede deal room invitation: %w", err)
	}

	expiresAt := time.Now().UTC().Add(invitationTTL)
	// attempt_no comes from the row itself rather than a counter the caller
	// holds, so two resends racing cannot both claim the same number — the
	// unique constraint on (participant_id, attempt_no) refuses the loser.
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_invitation
		     (participant_id, attempt_no, token_hash, expires_at, source, captured_by)
		 VALUES ($1,
		         (SELECT coalesce(max(attempt_no), 0) + 1 FROM deal_room_invitation WHERE participant_id = $1),
		         $2, $3, $4, $5)`,
		participantID, digest, expiresAt, sourceCredential, by); err != nil {
		return time.Time{}, fmt.Errorf("insert deal room invitation: %w", err)
	}
	return expiresAt, nil
}

// sourceCredential is the provenance every invitation row carries: the server
// minted it, no import or connector did.
const sourceCredential = "system"

// invitingUser is the human recorded as having admitted the participant, or nil
// when the actor is not a seat. RequireHuman has already ruled that out on this
// path, so the nil arm exists for the type rather than for a live case.
func invitingUser(ctx context.Context) *ids.UUID {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return nil
	}
	return &p.UserID
}

// ResendInvitation issues a fresh credential and retires the previous one.
//
// Available on a closed or paused room, deliberately: a buyer who lost their
// link to a closed room still needs to reach what they were shown.
func (s *Store) ResendInvitation(ctx context.Context, roomID ids.DealRoomID, participantID ids.DealRoomParticipantID) (IssuedInvitation, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return IssuedInvitation{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return IssuedInvitation{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return IssuedInvitation{}, err
	}
	raw, digest, err := mintCredential()
	if err != nil {
		return IssuedInvitation{}, err
	}

	var out IssuedInvitation
	err = s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		current, err := readParticipant(ctx, tx, roomID, participantID)
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return errRevokedNoResend
		}
		expiresAt, err := issueCredential(ctx, tx, participantID, digest, by)
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "invite", participantObject, participantID.UUID,
			nil, map[string]any{fieldRoomID: roomID.UUID, "resent": true})
		if err != nil {
			return fmt.Errorf("audit deal room resend: %w", err)
		}
		if err := emitCredentialReissued(ctx, tx, auditID, room, participantID, reasonResent); err != nil {
			return err
		}
		participant, err := readParticipant(ctx, tx, roomID, participantID)
		if err != nil {
			return err
		}
		out = IssuedInvitation{Participant: participant, Credential: raw, ExpiresAt: expiresAt}
		return nil
	})
	return out, err
}

// RevokeParticipant takes a person's access away.
//
// Available in EVERY room state including closed and archived. Revocation is a
// security control, and being unable to remove somebody from a room holding your
// signed contract is a real hazard months after the deal closed — so unlike
// every content mutation, this is never frozen along with the room.
func (s *Store) RevokeParticipant(ctx context.Context, roomID ids.DealRoomID, participantID ids.DealRoomParticipantID) (crmcontracts.DealRoomParticipant, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomParticipant{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomParticipant{}, err
	}
	var out crmcontracts.DealRoomParticipant
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		current, err := readParticipant(ctx, tx, roomID, participantID)
		if err != nil {
			return err
		}
		// Already revoked: report the row rather than refusing. Revocation is
		// the one act a steward may reach for in a hurry, and a second attempt
		// meaning "make sure they are out" deserves confirmation, not a 409.
		if current.RevokedAt != nil {
			out = current
			return nil
		}
		if err := revokeTx(ctx, tx, room, participantID); err != nil {
			return err
		}
		out, err = readParticipant(ctx, tx, roomID, participantID)
		return err
	})
	return out, err
}

// revokeTx ends access three ways at once, because any one of them left standing
// would keep the person in: the participant row stops being live, their sessions
// stop answering, and any credential still in a mailbox stops being exchangeable.
func revokeTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, participantID ids.DealRoomParticipantID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_participant SET revoked_at = now() WHERE id = $1`,
		participantID); err != nil {
		return fmt.Errorf("revoke deal room participant: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_session SET revoked_at = now()
		  WHERE participant_id = $1 AND revoked_at IS NULL`,
		participantID); err != nil {
		return fmt.Errorf("revoke deal room sessions: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_invitation SET superseded_at = now()
		  WHERE participant_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL`,
		participantID); err != nil {
		return fmt.Errorf("retire deal room invitation: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "revoke", participantObject, participantID.UUID,
		map[string]any{"revoked_at": nil}, map[string]any{fieldRoomID: ids.UUID(room.Id)})
	if err != nil {
		return fmt.Errorf("audit deal room revoke: %w", err)
	}
	revoked := crmcontracts.PublicEventDealRoomParticipantRevoked{
		DealId:        room.DealId,
		ParticipantId: openapi_types.UUID(participantID.UUID),
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(room.Id), revoked); err != nil {
		return fmt.Errorf("emit deal_room.participant_revoked: %w", err)
	}
	return nil
}

// UpdateParticipantInput corrects a participant. Every field is optional.
type UpdateParticipantInput struct {
	FullName   *string
	Email      *string
	Capability *string
}

// UpdateParticipant corrects a participant's details.
//
// Correcting the ADDRESS is only possible while their credential is unconsumed:
// once somebody has signed in, changing where their link points would hand their
// access to a different person. It also invalidates the credential already sent,
// because that link is in the OLD mailbox — leaving it live would mean the typo'd
// address kept working.
func (s *Store) UpdateParticipant(ctx context.Context, roomID ids.DealRoomID, participantID ids.DealRoomParticipantID, in UpdateParticipantInput) (crmcontracts.DealRoomParticipant, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomParticipant{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomParticipant{}, err
	}
	var out crmcontracts.DealRoomParticipant
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		current, err := readParticipant(ctx, tx, roomID, participantID)
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return errRevokedNoEdit
		}
		if err := applyParticipantPatch(ctx, tx, room, current, participantID, in); err != nil {
			return err
		}
		out, err = readParticipant(ctx, tx, roomID, participantID)
		return err
	})
	return out, err
}

func applyParticipantPatch(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, current crmcontracts.DealRoomParticipant, id ids.DealRoomParticipantID, in UpdateParticipantInput) error {
	p := storekit.NewPatch()
	if in.FullName != nil {
		p.Set("full_name", current.FullName, *in.FullName)
	}
	if in.Capability != nil {
		if err := refuseUnknownCapability(*in.Capability); err != nil {
			return err
		}
		p.Set("capability", current.Capability, *in.Capability)
	}
	if in.Email != nil {
		email := strings.ToLower(strings.TrimSpace(*in.Email))
		if email != string(current.Email) {
			if current.DeliveryState == crmcontracts.DealRoomDeliveryStateConsumed {
				return errAddressSettled
			}
			p.Set("email", current.Email, email)
		}
	}
	if p.Empty() {
		return nil
	}
	// No If-Match: a participant carries no version column, and the corrections
	// this accepts are last-writer-wins by nature — two stewards fixing the same
	// typo do not need to be told they collided.
	if err := p.ApplyGuardedIn(ctx, tx, participantObject, id.UUID, nil, storekit.NoArchiveColumn); err != nil {
		if storekit.IsUniqueViolation(err) {
			return errAlreadyInvited
		}
		return fmt.Errorf("apply deal room participant patch: %w", err)
	}
	// Correcting the address retires the link already sent, which is pointing at
	// the wrong mailbox. Nothing is minted here — a resend does that, so the act
	// of issuing a credential stays one code path.
	if in.Email != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE deal_room_invitation SET superseded_at = now()
			  WHERE participant_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL`,
			id); err != nil {
			return fmt.Errorf("retire deal room invitation after address change: %w", err)
		}
	}
	auditID, err := storekit.Audit(ctx, tx, "update", participantObject, id.UUID, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit deal room participant update: %w", err)
	}
	// Only an address change is announced, and only because it RETIRED a live
	// credential — the fact a subscriber can act on. Renaming somebody or
	// widening their capability changes nothing another system holds, and the
	// audit row already answers who changed what.
	if in.Email != nil {
		return emitCredentialReissued(ctx, tx, auditID, room, id, reasonAddressCorrected)
	}
	return nil
}

// Why a participant's standing credential stopped working.
const (
	reasonResent           = "resent"
	reasonAddressCorrected = "address_corrected"
)

// emitCredentialReissued announces that whatever a participant was holding no
// longer admits them.
func emitCredentialReissued(ctx context.Context, tx pgx.Tx, auditID ids.UUID, room crmcontracts.DealRoom, participantID ids.DealRoomParticipantID, reason string) error {
	reissued := crmcontracts.PublicEventDealRoomParticipantCredentialReissued{
		DealId:        room.DealId,
		ParticipantId: openapi_types.UUID(participantID.UUID),
		Reason:        reason,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(room.Id), reissued); err != nil {
		return fmt.Errorf("emit deal_room.participant_credential_reissued: %w", err)
	}
	return nil
}
