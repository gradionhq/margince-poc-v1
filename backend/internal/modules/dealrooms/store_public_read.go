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
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

// liveCompletion is the part of a task that is NOT in the release: whether it
// is done, when, and by which side.
type liveCompletion struct {
	doneAt *time.Time
	doneBy *string
}

// readCompletions returns the live completion state of a room's tasks, keyed by
// id. Only rows of THIS room are read, so a snapshot id that somehow named a
// foreign task would come back absent rather than resolved.
func readCompletions(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) (map[openapi_types.UUID]liveCompletion, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, done_at,
		        CASE WHEN done_by_participant_id IS NOT NULL THEN 'buyer'
		             WHEN done_by_user_id IS NOT NULL THEN 'seller' END
		   FROM deal_room_task WHERE room_id = $1 AND archived_at IS NULL`, roomID)
	if err != nil {
		return nil, fmt.Errorf("read deal room task completions: %w", err)
	}
	defer rows.Close()
	out := map[openapi_types.UUID]liveCompletion{}
	for rows.Next() {
		var id openapi_types.UUID
		var c liveCompletion
		if err := rows.Scan(&id, &c.doneAt, &c.doneBy); err != nil {
			return nil, fmt.Errorf("scan deal room task completion: %w", err)
		}
		out[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room task completions: %w", err)
	}
	return out, nil
}

// buyerTasks joins the published definitions to their live completion. A task
// archived since the release is dropped: its definition was published, but the
// row it would be ticked on is gone.
func buyerTasks(snap releaseSnapshot, live map[openapi_types.UUID]liveCompletion) []crmcontracts.BuyerRoomTask {
	out := make([]crmcontracts.BuyerRoomTask, 0, len(snap.Tasks))
	for _, t := range snap.Tasks {
		c, ok := live[t.ID]
		if !ok {
			continue
		}
		out = append(out, crmcontracts.BuyerRoomTask{
			Id:       t.ID,
			Side:     crmcontracts.DealRoomTaskSide(t.Side),
			Title:    t.Title,
			Position: t.Position,
			Done:     c.doneAt != nil,
			DoneAt:   c.doneAt,
			DoneBy:   c.doneBy,
		})
	}
	return out
}

// publishedTasks reads the room's standing and returns the list a buyer may see
// and act on. Empty — not refused — while the room serves no content.
func publishedTasks(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, now time.Time) (roomStanding, []crmcontracts.BuyerRoomTask, error) {
	st, err := readStanding(ctx, tx, roomID)
	if err != nil {
		return roomStanding{}, nil, err
	}
	if !servesContent(st.access(now)) || st.snapshot == nil {
		return st, []crmcontracts.BuyerRoomTask{}, nil
	}
	snap, err := decodeSnapshot(st.snapshot)
	if err != nil {
		return roomStanding{}, nil, err
	}
	live, err := readCompletions(ctx, tx, roomID)
	if err != nil {
		return roomStanding{}, nil, err
	}
	return st, buyerTasks(snap, live), nil
}

// BuyerTasks lists the shared to-do list as the buyer sees it.
func (s *Store) BuyerTasks(ctx context.Context, sess Session) ([]crmcontracts.BuyerRoomTask, error) {
	if sess.ID == ids.Nil {
		return nil, apperrors.ErrPermissionDenied
	}
	var out []crmcontracts.BuyerRoomTask
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		_, out, err = publishedTasks(ctx, tx, sess.RoomID, time.Now())
		return err
	})
	return out, err
}

// CompleteBuyerTask ticks or un-ticks one published item as the buyer.
//
// The item must be in the latest release — a task the seller added and has not
// published is invisible to the buyer and stays so — and the room must be live:
// paused, closed and expired rooms refuse with the same typed conflict the
// seller's side gets, because a finished list is a record.
func (s *Store) CompleteBuyerTask(ctx context.Context, sess Session, taskID ids.DealRoomTaskID, done bool) (crmcontracts.BuyerRoomTask, error) {
	if sess.ID == ids.Nil {
		return crmcontracts.BuyerRoomTask{}, apperrors.ErrPermissionDenied
	}
	var out crmcontracts.BuyerRoomTask
	err := s.tx(ctx, func(tx pgx.Tx) error {
		st, tasks, err := publishedTasks(ctx, tx, sess.RoomID, time.Now())
		if err != nil {
			return err
		}
		if access := st.access(time.Now()); access != accessLive {
			return notTaskEditable(access)
		}
		current, ok := findBuyerTask(tasks, taskID)
		if !ok {
			return apperrors.ErrNotFound
		}
		if current.Done == done {
			out = current
			return nil
		}
		if err := writeBuyerCompletion(ctx, tx, sess, taskID, done); err != nil {
			return err
		}
		_, tasks, err = publishedTasks(ctx, tx, sess.RoomID, time.Now())
		if err != nil {
			return err
		}
		out, _ = findBuyerTask(tasks, taskID)
		return nil
	})
	return out, err
}

func findBuyerTask(tasks []crmcontracts.BuyerRoomTask, taskID ids.DealRoomTaskID) (crmcontracts.BuyerRoomTask, bool) {
	for _, t := range tasks {
		if ids.UUID(t.Id) == taskID.UUID {
			return t, true
		}
	}
	return crmcontracts.BuyerRoomTask{}, false
}

// writeBuyerCompletion moves the three completion columns as one, attributing
// a tick to the participant, and records it the way the seller's tick is
// recorded — same audit verb, same event — so a subscriber cannot tell the two
// apart except by the actor, which is the point.
func writeBuyerCompletion(ctx context.Context, tx pgx.Tx, sess Session, taskID ids.DealRoomTaskID, done bool) error {
	var tag pgconn.CommandTag
	var err error
	if done {
		tag, err = tx.Exec(ctx,
			`UPDATE deal_room_task SET done_at = now(), done_by_participant_id = $3, done_by_user_id = NULL
			  WHERE id = $1 AND room_id = $2 AND archived_at IS NULL AND done_at IS NULL`,
			taskID, sess.RoomID, sess.ParticipantID)
	} else {
		tag, err = tx.Exec(ctx,
			`UPDATE deal_room_task SET done_at = NULL, done_by_participant_id = NULL, done_by_user_id = NULL
			  WHERE id = $1 AND room_id = $2 AND archived_at IS NULL AND done_at IS NOT NULL`,
			taskID, sess.RoomID)
	}
	if err != nil {
		return fmt.Errorf("complete deal room task as buyer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The other side moved it between our read and our write. Reported as
		// a conflict so the client re-reads rather than retrying blindly.
		return apperrors.ErrConflict
	}
	auditID, err := storekit.Audit(ctx, tx, "update", taskObject, taskID.UUID,
		map[string]any{"done": !done}, map[string]any{"done": done, fieldRoomID: sess.RoomID.UUID})
	if err != nil {
		return fmt.Errorf("audit deal room task completion: %w", err)
	}
	var dealID openapi_types.UUID
	var side string
	if err := tx.QueryRow(ctx,
		`SELECT r.deal_id, t.side FROM deal_room_task t JOIN deal_room r ON r.id = t.room_id
		  WHERE t.id = $1 AND t.room_id = $2`, taskID, sess.RoomID).Scan(&dealID, &side); err != nil {
		return fmt.Errorf("read deal room task for its event: %w", err)
	}
	changed := crmcontracts.PublicEventDealRoomTaskCompletionChanged{
		DealId: dealID,
		TaskId: openapi_types.UUID(taskID.UUID),
		Side:   side,
		Done:   done,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, sess.RoomID.UUID, changed); err != nil {
		return fmt.Errorf("emit deal_room.task_completion_changed: %w", err)
	}
	return nil
}
