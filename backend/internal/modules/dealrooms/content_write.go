// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What every seller-side content write shares: the sides a comment is spoken
// from, the length a title may have, and the locked room read that settles
// whether the room can still take content at all.

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The sides of the room, spelled here because the contract carries them as a
// plain string — an inline enum would generate package-scope Go constants named
// Seller and Buyer in the shared contracts package and silently rename any
// other schema declaring the same values.
const (
	sideSeller = "seller"
	sideBuyer  = "buyer"
)

// titleLimit bounds a document's wording. Unbounded, it is a line the other
// side has to read in a list they did not write.
const titleLimit = 255

// cleanTitle trims the wording and refuses what cannot stand as a title.
func cleanTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", &fieldError{field: columnTitle, code: codeRequired, msg: "title is required"}
	}
	if len([]rune(trimmed)) > titleLimit {
		return "", &fieldError{
			field: columnTitle,
			code:  codeTooLong,
			msg:   "title is longer than 255 characters",
		}
	}
	return trimmed, nil
}

// openRoomForContent returns the room a content write is about to land in,
// with its state settled under a lock.
//
// The lock is what makes the state answer trustworthy. Without it a close or an
// archive committing after the unlocked read leaves this write landing on a
// terminal room — a document added to, or a thread opened in, a room that is
// supposed to have become a record. This is the same lock-then-re-read the
// lifecycle moves take, and for the same reason: a decision read outside a
// lock is a guess.
func openRoomForContent(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) (crmcontracts.DealRoom, error) {
	room, err := readRoom(ctx, tx, roomID)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}
	if err := ensureDealWritable(ctx, tx, room); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	if _, err := storekit.LockRow(ctx, tx, roomObject, ids.UUID(room.Id), storekit.LiveOnly); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	room, err = readRoom(ctx, tx, roomID)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}
	if !publishable(string(room.State)) {
		return crmcontracts.DealRoom{}, notContentEditable(string(room.State))
	}
	return room, nil
}
