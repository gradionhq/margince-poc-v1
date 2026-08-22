// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The credential's own lifecycle: minting one onto a participant, retiring
// whatever stood before it, and writing down what the mail relay did with it.
//
// Kept apart from participant CRUD because the two are governed differently. A
// participant's name and capability are ordinary editable fields; a credential
// is a live secret whose ONE invariant — at most one works at a time — is held
// by an index and a supersede that must run in that order. Reading those three
// statements together, without the roster management around them, is the point.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// issueCredential retires whatever credential stands for this participant and
// records the new one as the next attempt.
//
// Superseding first is what makes a resend REPLACE rather than fail: the index
// uq_deal_room_invitation_live is what holds "at most one live credential", and
// without the supersede this INSERT would simply collide with it.
func issueCredential(ctx context.Context, tx pgx.Tx, participantID ids.DealRoomParticipantID, digest []byte, by string) (time.Time, error) {
	return issueCredentialFor(ctx, tx, participantID, digest, by, invitationTTL)
}

// issueCredentialFor is issueCredential with the credential's lifetime chosen
// by the caller: a buyer's invitation lives a week, a seller's preview minutes.
func issueCredentialFor(ctx context.Context, tx pgx.Tx, participantID ids.DealRoomParticipantID, digest []byte, by string, ttl time.Duration) (time.Time, error) {
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_invitation SET superseded_at = now()
		  WHERE participant_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL`,
		participantID); err != nil {
		return time.Time{}, fmt.Errorf("supersede deal room invitation: %w", err)
	}

	expiresAt := time.Now().UTC().Add(ttl)
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
		if storekit.IsUniqueViolation(err) {
			// Another resend for this participant committed between our
			// supersede and our insert. Reported as a conflict rather than
			// letting the bare violation surface as a 500, which would invite
			// the retry that mints a third credential.
			return time.Time{}, errResendInFlight
		}
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

// lockParticipant serializes the three mutations that decide from a
// participant's current state — resend, revoke and correct.
//
// It is taken BEFORE the decision read, which is the whole point. Without it a
// resend and a revoke can interleave at READ COMMITTED so that the resend reads
// "not revoked", blocks on the revoke's row locks, and then inserts a live
// credential AFTER revocation committed — leaving a revoked participant holding
// a working link, and a deal_room.participant_revoked event that already told
// every subscriber their credential was retired.
func lockParticipant(ctx context.Context, tx pgx.Tx, participantID ids.DealRoomParticipantID) error {
	_, err := storekit.LockRow(ctx, tx, participantObject, participantID.UUID, storekit.NoArchiveColumn)
	return err
}

// RecordInvitationSend stamps the outcome of handing an invitation to the relay.
//
// It writes only the standing credential's row, and only the two columns that
// say what the relay did — so a resend that has already superseded this
// invitation is untouched, and a late-arriving outcome cannot resurrect a
// retired link's delivery state.
//
// Deliberately NOT audited or evented: nothing about the record changed, and
// nobody gained or lost access. This is the mail server's answer written down.
func (s *Store) RecordInvitationSend(ctx context.Context, participantID ids.DealRoomParticipantID, sendErr error) error {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var reason *string
		column := "sent_at"
		if sendErr != nil {
			column = "failed_at"
			// The relay's own words, bounded: a reason is for a seller to read,
			// not a place to accumulate a stack trace.
			text := sendErr.Error()
			if len(text) > failureReasonLimit {
				text = text[:failureReasonLimit]
			}
			reason = &text
		}
		// The column is a compile-time literal chosen by the branch above, never
		// a value off a request.
		if _, err := tx.Exec(ctx,
			`UPDATE deal_room_invitation SET `+column+` = now(), failure_reason = $2
			  WHERE participant_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL`,
			participantID, reason); err != nil {
			return fmt.Errorf("record deal room invitation delivery: %w", err)
		}
		return nil
	})
}

// failureReasonLimit bounds what a relay's refusal may write into the row.
const failureReasonLimit = 500
