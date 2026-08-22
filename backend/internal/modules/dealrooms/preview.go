// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// "View as buyer". A seller sees the room through the real public edge, as a
// PREVIEW participant: their own seat, read-only, invisible to buyers. A mock
// would drift from the release, paused and expired rules the moment one of
// them changed; a real session cannot.
//
// What keeps the preview harmless: the row is `preview` and `view` by CHECK,
// so no participant write can make it a reviewer; it is outside the
// per-address uniqueness and excluded from every roster, count and the public
// link request, so a buyer never learns of it and the rep's own address never
// receives a mailed link; every public write refuses a preview session; and
// pausing or closing the room ends preview sessions while a real buyer's
// survive, since the rep has no reason to keep reading a room they froze.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// previewCredentialTTL is how long the rep has to open the tab. Minutes, not
// the week a buyer gets: the credential is handed to a browser in the same
// click, and anything longer is a link lying around.
const previewCredentialTTL = 10 * time.Minute

// previewSessionTTL bounds a preview tab left open.
const previewSessionTTL = time.Hour

// IssuedPreview is the one-time credential, returned once.
type IssuedPreview struct {
	Credential string
	ExpiresAt  time.Time
}

// errNoPreviewInDraft refuses a preview of a room that has never been
// published: the buyer would see nothing, and a blank page teaches a rep that
// the feature is broken rather than that the room is a draft.
var errNoPreviewInDraft = &stateError{
	code:    "deal_room_not_previewable",
	current: stateDraft,
	wanted:  "publish first; a buyer only ever sees a published release",
}

// PreviewRoom mints a credential for the caller's preview seat in the room,
// creating the seat on first use and ending the caller's earlier preview
// sessions.
func (s *Store) PreviewRoom(ctx context.Context, roomID ids.DealRoomID) (IssuedPreview, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return IssuedPreview{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return IssuedPreview{}, err
	}
	// RequireHuman admits the system principal; a preview is a person's act
	// on their own seat and a system caller has no seat to preview from.
	if actor, ok := principal.Actor(ctx); !ok || actor.Type != principal.PrincipalHuman {
		return IssuedPreview{}, apperrors.ErrPermissionDenied
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return IssuedPreview{}, err
	}
	raw, digest, err := mintCredential()
	if err != nil {
		return IssuedPreview{}, err
	}
	var out IssuedPreview
	err = s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, room); err != nil {
			return err
		}
		switch room.State {
		case stateDraft:
			return errNoPreviewInDraft
		case stateArchived:
			return notAdmitting(stateArchived)
		}
		seat, err := previewSeat(ctx, tx, roomID, by)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE deal_room_session SET revoked_at = now()
			  WHERE participant_id = $1 AND revoked_at IS NULL`, seat); err != nil {
			return fmt.Errorf("end earlier preview sessions: %w", err)
		}
		expiresAt, err := issueCredentialFor(ctx, tx, seat, digest, by, previewCredentialTTL)
		if err != nil {
			return err
		}
		// Audit-only: a preview is the seller looking at their own room, and
		// the catalog's invitation events are about buyers being admitted.
		if _, err := storekit.Audit(ctx, tx, "invite", participantObject, seat.UUID, nil,
			map[string]any{fieldRoomID: roomID.UUID, "preview": true}); err != nil {
			return fmt.Errorf("audit deal room preview: %w", err)
		}
		out = IssuedPreview{Credential: raw, ExpiresAt: expiresAt}
		return nil
	})
	return out, err
}

// previewSeat finds the caller's preview participant in the room, or creates
// it: the rep's own name and address, so a buyer-side audit row that names
// the participant names the rep.
func previewSeat(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, by string) (ids.DealRoomParticipantID, error) {
	user := invitingUser(ctx)
	if user == nil {
		return ids.DealRoomParticipantID{}, apperrors.ErrPermissionDenied
	}
	var seat ids.DealRoomParticipantID
	err := tx.QueryRow(ctx,
		`SELECT id FROM deal_room_participant
		  WHERE room_id = $1 AND invited_by = $2 AND preview AND revoked_at IS NULL`,
		roomID, *user).Scan(&seat)
	if err == nil {
		return seat, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.DealRoomParticipantID{}, fmt.Errorf("find deal room preview seat: %w", err)
	}
	var name, email string
	if err := tx.QueryRow(ctx,
		`SELECT display_name, lower(email) FROM app_user WHERE id = $1 AND archived_at IS NULL`,
		*user).Scan(&name, &email); err != nil {
		return ids.DealRoomParticipantID{}, fmt.Errorf("read the previewing user: %w", err)
	}
	seat = ids.New[ids.DealRoomParticipantKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_participant
		     (id, room_id, full_name, email, capability, invited_by, preview, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, true, $7, $8)`,
		seat, roomID, name, email, capabilityView, *user, sourceCredential, by); err != nil {
		return ids.DealRoomParticipantID{}, fmt.Errorf("insert deal room preview seat: %w", err)
	}
	return seat, nil
}

// endPreviewSessions is what pausing and closing do to preview sessions: the
// rep froze the room, and a preview tab still reading it would only tell
// them what they already know. A real buyer's session is untouched.
func endPreviewSessions(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_session s SET revoked_at = now()
		   FROM deal_room_participant p
		  WHERE p.id = s.participant_id AND p.room_id = $1 AND p.preview AND s.revoked_at IS NULL`,
		roomID); err != nil {
		return fmt.Errorf("end deal room preview sessions: %w", err)
	}
	// And the credential not yet opened: a preview minted a minute before the
	// pause must not become a session a minute after it.
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_invitation i SET superseded_at = now()
		   FROM deal_room_participant p
		  WHERE p.id = i.participant_id AND p.room_id = $1 AND p.preview
		    AND i.consumed_at IS NULL AND i.superseded_at IS NULL`,
		roomID); err != nil {
		return fmt.Errorf("retire deal room preview credentials: %w", err)
	}
	return nil
}

// errPreviewSession refuses a write from a preview session. Read everything,
// change nothing: a rep's question to their own room would otherwise be
// recorded as the buyer's.
var errPreviewSession = &fieldError{
	field: fieldCapability,
	code:  "preview_session",
	msg:   "a preview cannot write; sign in as a buyer to comment or decide",
}
