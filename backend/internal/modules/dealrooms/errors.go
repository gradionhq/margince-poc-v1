// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// stateError refuses a lifecycle move the room's current state does not admit,
// and names both halves: what the room is now, and what the operation needed.
// A bare "conflict" leaves the caller guessing which of the nine states they
// are in, which is exactly the thing they cannot see from the outside.
type stateError struct {
	code    string
	current string
	wanted  string
}

func (e *stateError) Error() string {
	return fmt.Sprintf("deal room is %s: %s", e.current, e.wanted)
}

// MessageFault maps this to a 409 carrying the code, so a client can branch on
// the reason rather than parsing prose.
func (e *stateError) MessageFault() (code, message string) {
	return e.code, e.Error()
}

func (e *stateError) Unwrap() error { return apperrors.ErrConflict }

// notPublishable refuses a publish from one of the three states where a buyer is
// no longer meant to receive anything new. All three are terminal — there is no
// un-close and no un-expire — so the message names opening a new room rather
// than implying a way back that does not exist.
func notPublishable(current string) error {
	return &stateError{
		code:    "deal_room_not_publishable",
		current: current,
		wanted:  "this room is finished and cannot publish again: open a new Deal Room on the deal to show the buyer anything further",
	}
}

func notPausable(current string) error {
	return &stateError{
		code:    "deal_room_not_pausable",
		current: current,
		wanted:  "only a live room can be paused",
	}
}

func notPaused(current string) error {
	return &stateError{
		code:    "deal_room_not_paused",
		current: current,
		wanted:  "only a paused room can be resumed",
	}
}

func notClosable(current string) error {
	return &stateError{
		code:    "deal_room_not_closable",
		current: current,
		wanted:  "only a live or paused room can be closed",
	}
}

// errRoomAlreadyOpen refuses a second room on a deal that still has an active
// one. Archiving the first frees the deal, and saying so is the whole point:
// the caller's next move is otherwise unguessable.
var errRoomAlreadyOpen = &messageError{
	code: "deal_room_already_open",
	msg:  "this deal already has an active Deal Room: archive it before opening another",
}

// errStewardUnknown refuses a steward nobody can be pointed at.
var errStewardUnknown = &fieldError{
	field: "steward_user_id",
	code:  "unknown_user",
	msg:   "no live user with that id: the steward is the person a buyer contacts for help",
}

type messageError struct {
	code string
	msg  string
}

func (e *messageError) Error() string { return e.msg }

func (e *messageError) MessageFault() (code, message string) { return e.code, e.msg }

func (e *messageError) Unwrap() error { return apperrors.ErrConflict }

type fieldError struct {
	field string
	code  string
	msg   string
}

func (e *fieldError) Error() string { return e.msg }

func (e *fieldError) FieldFault() (field, code, message string) {
	return e.field, e.code, e.msg
}
