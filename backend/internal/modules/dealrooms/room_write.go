// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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

// CreateRoomInput opens a room on a deal.
type CreateRoomInput struct {
	DealID         ids.DealID
	Title          string
	WelcomeMessage *string
	StewardUserID  *ids.UserID
	ExpiresAt      *time.Time
	Source         string
}

// UpdateRoomInput edits a room's working copy. Every field is optional; a nil
// pointer leaves the column unchanged.
type UpdateRoomInput struct {
	Title          *string
	WelcomeMessage **string
	StewardUserID  **ids.UserID
	ExpiresAt      **time.Time
	IfVersion      *int64
}

// CreateRoom opens a Deal Room on a deal.
func (s *Store) CreateRoom(ctx context.Context, in CreateRoomInput) (crmcontracts.DealRoom, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionCreate); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}

	var out crmcontracts.DealRoom
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = createRoomTx(ctx, tx, in, by)
		return err
	})
	return out, err
}

func createRoomTx(ctx context.Context, tx pgx.Tx, in CreateRoomInput, by string) (crmcontracts.DealRoom, error) {
	// Opening a room on a deal is a write against that deal's buyer-facing
	// surface, so the caller needs write authority on the deal — seeing it is
	// not enough to start showing it to an outside party.
	if err := auth.EnsureWritableLive(ctx, tx, dealTable, in.DealID.UUID); err != nil {
		return crmcontracts.DealRoom{}, err
	}

	steward, hasSteward, err := resolveSteward(ctx, tx, in)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}
	var stewardArg *ids.UserID
	if hasSteward {
		stewardArg = &steward
	}

	id := ids.New[ids.DealRoomKind]()
	_, err = tx.Exec(ctx,
		`INSERT INTO deal_room (id, deal_id, title, welcome_message, steward_user_id,
		                        expires_at, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, in.DealID, in.Title, in.WelcomeMessage, stewardArg, in.ExpiresAt, in.Source, by)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.DealRoom{}, errRoomAlreadyOpen
		}
		if storekit.IsForeignKeyViolation(err) {
			return crmcontracts.DealRoom{}, apperrors.ErrNotFound
		}
		return crmcontracts.DealRoom{}, fmt.Errorf("insert deal room: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "create", roomObject, id.UUID, nil,
		map[string]any{"deal_id": in.DealID.UUID, columnTitle: in.Title, "state": stateDraft})
	if err != nil {
		return crmcontracts.DealRoom{}, fmt.Errorf("audit deal room create: %w", err)
	}
	opened := crmcontracts.PublicEventDealRoomOpened{
		DealId: openapi_types.UUID(in.DealID.UUID),
		Title:  in.Title,
	}
	if hasSteward {
		u := openapi_types.UUID(steward.UUID)
		opened.StewardUserId = &u
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, opened); err != nil {
		return crmcontracts.DealRoom{}, fmt.Errorf("emit deal_room.opened: %w", err)
	}
	return readRoom(ctx, tx, id)
}

// resolveSteward settles who a buyer is pointed at for help: the caller's
// choice when they made one, otherwise the deal's owner.
//
// An explicitly named steward is checked for existence rather than trusted —
// an unknown id would otherwise land as a foreign-key violation reported as
// "deal not found", which sends the caller looking in the wrong place.
//
// The second return says whether a steward was found at all, so an unowned deal
// reads as "no steward" rather than as a nil that a caller might take for a
// failure. The room is still created: a room without a steward is a real state
// the schema admits (the column is nullable and a deleted user nulls it), and
// it is repaired by transferring one rather than by refusing to open the room.
func resolveSteward(ctx context.Context, tx pgx.Tx, in CreateRoomInput) (ids.UserID, bool, error) {
	if in.StewardUserID != nil {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND archived_at IS NULL)`,
			in.StewardUserID).Scan(&exists); err != nil {
			return ids.UserID{}, false, fmt.Errorf("check steward: %w", err)
		}
		if !exists {
			return ids.UserID{}, false, errStewardUnknown
		}
		return *in.StewardUserID, true, nil
	}

	var owner *ids.UUID
	if err := tx.QueryRow(ctx, `SELECT owner_id FROM deal WHERE id = $1`, in.DealID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ids.UserID{}, false, apperrors.ErrNotFound
		}
		return ids.UserID{}, false, fmt.Errorf("read deal owner for steward: %w", err)
	}
	if owner == nil {
		return ids.UserID{}, false, nil
	}
	return ids.From[ids.UserKind](*owner), true, nil
}

// UpdateRoom edits a room's working copy. It never changes what a buyer is
// currently reading — that is the published release, which only publish moves.
func (s *Store) UpdateRoom(ctx context.Context, id ids.DealRoomID, in UpdateRoomInput) (crmcontracts.DealRoom, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	var out crmcontracts.DealRoom
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readRoom(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ensureDealWritable(ctx, tx, current); err != nil {
			return err
		}
		p := buildRoomPatch(current, in)
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, roomObject, id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply deal room patch: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "update", roomObject, id.UUID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit deal room update: %w", err)
		}
		updated := crmcontracts.PublicEventDealRoomUpdated{
			DealId:        current.DealId,
			ChangedFields: changedFields(p),
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, updated); err != nil {
			return fmt.Errorf("emit deal_room.updated: %w", err)
		}
		out, err = readRoom(ctx, tx, id)
		return err
	})
	return out, err
}

func buildRoomPatch(current crmcontracts.DealRoom, in UpdateRoomInput) *storekit.Patch {
	p := storekit.NewPatch()
	if in.Title != nil {
		p.Set(columnTitle, current.Title, *in.Title)
	}
	if in.WelcomeMessage != nil {
		p.Set("welcome_message", current.WelcomeMessage, *in.WelcomeMessage)
	}
	if in.StewardUserID != nil {
		p.Set("steward_user_id", current.StewardUserId, *in.StewardUserID)
	}
	if in.ExpiresAt != nil {
		p.Set("expires_at", current.ExpiresAt, *in.ExpiresAt)
	}
	return p
}

// changedFields names the editorial columns this patch moved, sorted so a
// subscriber comparing two events is not reading map iteration order.
//
// It publishes NAMES only. The welcome text a rep is drafting is unpublished
// editorial content — the whole reason a room has releases at all — and putting
// it on the bus would hand every subscriber the words before the buyer has
// them, past the human-publishes gate.
func changedFields(p *storekit.Patch) []string {
	out := slices.Collect(maps.Keys(p.After()))
	slices.Sort(out)
	return out
}

// ensureDealWritable holds the rule that authority over a room follows
// authority over its deal: reading the room proved the deal is visible, and a
// mutation additionally needs write authority on it.
func ensureDealWritable(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom) error {
	return auth.EnsureWritable(ctx, tx, dealTable, ids.UUID(room.DealId))
}
