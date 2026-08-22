// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What a buyer reads, and the one thing they write: a tick on the shared list.
// Every query here names the session's room in its WHERE clause; nothing reads
// the deal, and the editorial content comes from the latest release only.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The four access states the buyer edge reports. Distinct from the room's
// nine lifecycle states on purpose: a buyer is told what they can do, not
// where the seller's workflow stands.
const (
	accessLive    = "live"
	accessClosed  = "closed"
	accessPaused  = "paused"
	accessExpired = "expired"
)

// roomStanding is the one room read the buyer edge performs: state and expiry
// for access, the steward's name for the contact line, and the latest release
// for everything else.
type roomStanding struct {
	state       string
	expiresAt   *time.Time
	closedAt    *time.Time
	stewardName *string
	releaseNo   *int
	snapshot    []byte
}

func readStanding(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) (roomStanding, error) {
	var st roomStanding
	err := tx.QueryRow(ctx,
		`SELECT r.state, r.expires_at, r.closed_at, u.display_name, rel.release_no, rel.snapshot
		   FROM deal_room r
		   LEFT JOIN app_user u ON u.id = r.steward_user_id
		   LEFT JOIN LATERAL (SELECT release_no, snapshot FROM deal_room_release
		                       WHERE room_id = r.id ORDER BY release_no DESC LIMIT 1) rel ON true
		  WHERE r.id = $1 AND r.archived_at IS NULL`,
		roomID).Scan(&st.state, &st.expiresAt, &st.closedAt, &st.stewardName, &st.releaseNo, &st.snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return roomStanding{}, apperrors.ErrNotFound
	}
	if err != nil {
		return roomStanding{}, fmt.Errorf("read deal room standing: %w", err)
	}
	return st, nil
}

// access maps the room's lifecycle onto what the buyer may do. Expiry is
// decided HERE, on every read, rather than by a sweep that flips the state
// later: a room whose expires_at has passed stops serving the moment it passes.
func (st roomStanding) access(now time.Time) string {
	switch st.state {
	case statePaused:
		return accessPaused
	case stateClosed:
		return accessClosed
	case "expired", stateArchived:
		return accessExpired
	}
	if st.expiresAt != nil && !st.expiresAt.After(now) {
		return accessExpired
	}
	return accessLive
}

// servesContent says whether the release is shown at all in this access state.
func servesContent(access string) bool {
	return access == accessLive || access == accessClosed
}

// BuyerView assembles the room bootstrap for one session.
func (s *Store) BuyerView(ctx context.Context, sess Session) (crmcontracts.BuyerRoomView, error) {
	if sess.ID == ids.Nil {
		return crmcontracts.BuyerRoomView{}, apperrors.ErrPermissionDenied
	}
	var out crmcontracts.BuyerRoomView
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out.Participant, err = readBuyerParticipant(ctx, tx, sess)
		if err != nil {
			return err
		}
		st, err := readStanding(ctx, tx, sess.RoomID)
		if err != nil {
			return err
		}
		out.Access = crmcontracts.BuyerRoomAccess(st.access(time.Now()))
		out.StewardName = st.stewardName
		if sess.Preview {
			preview := true
			out.Preview = &preview
		}
		if !servesContent(string(out.Access)) || st.snapshot == nil {
			return nil
		}
		snap, err := decodeSnapshot(st.snapshot)
		if err != nil {
			return err
		}
		out.Room = &crmcontracts.BuyerRoomContent{
			Title:          snap.Title,
			WelcomeMessage: snap.WelcomeMessage,
			ReleaseNo:      *st.releaseNo,
			ReleasedAt:     snap.ReleasedAt,
			StewardName:    st.stewardName,
			ClosedAt:       st.closedAt,
		}
		return nil
	})
	return out, err
}

// readBuyerParticipant returns the caller's own row — and only theirs. The
// predicate is the session's (participant, room) pair, so even a session whose
// room column was somehow wrong could not read another room's person.
func readBuyerParticipant(ctx context.Context, tx pgx.Tx, sess Session) (crmcontracts.BuyerRoomParticipant, error) {
	var (
		out   crmcontracts.BuyerRoomParticipant
		email string
	)
	err := tx.QueryRow(ctx,
		`SELECT id, full_name, email, capability FROM deal_room_participant
		  WHERE id = $1 AND room_id = $2 AND revoked_at IS NULL`,
		sess.ParticipantID, sess.RoomID).Scan(&out.Id, &out.FullName, &email, &out.Capability)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.BuyerRoomParticipant{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.BuyerRoomParticipant{}, fmt.Errorf("read deal room buyer: %w", err)
	}
	out.Email = openapi_types.Email(email)
	return out, nil
}
