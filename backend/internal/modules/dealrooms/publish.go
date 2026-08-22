// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PublishRoom freezes the room's working copy as the next release and puts it
// in front of the buyer.
//
// This is the act the whole module is arranged around. An agent may shape the
// draft; the publish is a human's, because it is the moment words become
// visible outside the company and somebody has to be nameable for them. The
// contract enforces that with x-agent-access: human-only, and RequireHuman
// holds the same line at the store so the rule survives a new caller.
func (s *Store) PublishRoom(ctx context.Context, id ids.DealRoomID, note *string) (crmcontracts.DealRoomRelease, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}

	var out crmcontracts.DealRoomRelease
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = publishRoomTx(ctx, tx, id, note, by)
		return err
	})
	return out, err
}

func publishRoomTx(ctx context.Context, tx pgx.Tx, id ids.DealRoomID, note *string, by string) (crmcontracts.DealRoomRelease, error) {
	room, err := readRoom(ctx, tx, id)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	if err := ensureDealWritable(ctx, tx, room); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	// Lock before numbering: two concurrent publishes would otherwise both read
	// the same highest release number and race for it. One would lose to the
	// unique index, but the loser's whole transaction is wasted work and the
	// error it surfaces says nothing useful about what happened.
	if _, err := storekit.LockRow(ctx, tx, roomObject, id.UUID, storekit.LiveOnly); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	room, err = readRoom(ctx, tx, id)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	if !publishable(string(room.State)) {
		return crmcontracts.DealRoomRelease{}, notPublishable(string(room.State))
	}

	next, err := nextReleaseNo(ctx, tx, id)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}

	releaseID := ids.New[ids.DealRoomReleaseKind]()
	publisher, err := publishingUser(ctx)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	docs, err := publishableDocumentRows(ctx, tx, id)
	if err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO deal_room_release (id, room_id, release_no, snapshot, release_note,
		                                published_by, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		releaseID, id, next, snapshotOf(room, docs), note, publisher, room.Source, by)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.DealRoomRelease{}, apperrors.ErrConflict
		}
		return crmcontracts.DealRoomRelease{}, fmt.Errorf("insert deal room release: %w", err)
	}

	if err := markRoomLive(ctx, tx, room); err != nil {
		return crmcontracts.DealRoomRelease{}, err
	}

	auditID, err := storekit.Audit(ctx, tx, "publish", roomObject, id.UUID, nil,
		map[string]any{"release_no": next, "release_id": releaseID.UUID})
	if err != nil {
		return crmcontracts.DealRoomRelease{}, fmt.Errorf("audit deal room publish: %w", err)
	}
	relUUID := openapi_types.UUID(releaseID.UUID)
	published := crmcontracts.PublicEventDealRoomPublished{
		DealId:    room.DealId,
		ReleaseNo: next,
		ReleaseId: &relUUID,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, published); err != nil {
		return crmcontracts.DealRoomRelease{}, fmt.Errorf("emit deal_room.published: %w", err)
	}
	return readRelease(ctx, tx, releaseID)
}

// publishable names the states a publish may leave from. The three it refuses
// are the ones where a buyer is no longer meant to receive anything new;
// publishing a paused room deliberately resumes it, because a seller who
// publishes plainly intends the buyer to see the result.
func publishable(state string) bool {
	switch state {
	case "closed", "expired", "archived":
		return false
	}
	return true
}

// nextReleaseNo is the room's highest release number plus one, counted under
// the lock the caller already holds.
func nextReleaseNo(ctx context.Context, tx pgx.Tx, id ids.DealRoomID) (int, error) {
	var highest *int
	if err := tx.QueryRow(ctx,
		`SELECT max(release_no) FROM deal_room_release WHERE room_id = $1`, id).Scan(&highest); err != nil {
		return 0, fmt.Errorf("read highest release number: %w", err)
	}
	if highest == nil {
		return 1, nil
	}
	return *highest + 1, nil
}

// publishingUser is the human the release is attributed to. A release with no
// named publisher is legitimate only when the actor is not a seat at all, which
// RequireHuman has already ruled out for this path.
func publishingUser(ctx context.Context) (*ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return nil, apperrors.ErrPermissionDenied
	}
	u := ids.From[ids.UserKind](p.UserID)
	return &u, nil
}

// markRoomLive moves the room to live and stamps published_at once.
//
// published_at answers "when did this room go live?", which a later release does
// not change — so the guard is that it is not already set, and nothing narrower.
// Keying it on "this is release 1" would say the same thing in the common case
// and then refuse to repair a live room that somehow reached release 2 without
// the stamp, leaving that question permanently unanswerable.
func markRoomLive(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom) error {
	p := storekit.NewPatch()
	if string(room.State) != stateLive {
		p.Set("state", string(room.State), stateLive)
	}
	if room.PublishedAt == nil {
		p.Set("published_at", nil, time.Now().UTC())
	}
	if p.Empty() {
		return nil
	}
	if err := p.ApplyGuarded(ctx, tx, roomObject, ids.UUID(room.Id), nil); err != nil {
		return fmt.Errorf("mark deal room live: %w", err)
	}
	return nil
}

// readRelease returns one release. It carries no scope clause of its own: the
// only callers reach it having already read the parent room through the
// deal-scoped path, so a release cannot be read past a room the caller cannot
// see.
func readRelease(ctx context.Context, tx pgx.Tx, id ids.DealRoomReleaseID) (crmcontracts.DealRoomRelease, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+releaseColumns+` FROM deal_room_release WHERE id = $1`, id)
	out, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoomRelease{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.DealRoomRelease{}, fmt.Errorf("read deal room release: %w", err)
	}
	return out, nil
}
