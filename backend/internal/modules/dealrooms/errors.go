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

// notEditable refuses an edit to a room that can no longer publish. The draft
// would be unreachable rather than merely unpublished, which a caller cannot see
// from the outside.
func notEditable(current string) error {
	return &stateError{
		code:    "deal_room_not_editable",
		current: current,
		wanted:  "this room is finished and its text can no longer reach a buyer: open a new Deal Room on the deal",
	}
}

// notAdmitting refuses an invitation into a room that can no longer publish.
// The link would open a room that will never tell the recipient anything
// further, which is worse than being told no.
func notAdmitting(current string) error {
	return &stateError{
		code:    "deal_room_not_admitting",
		current: current,
		wanted:  "this room is finished and admits nobody new: open a new Deal Room on the deal",
	}
}

// notTaskEditable refuses every change to the shared to-do list in a room that
// can no longer reach a buyer — completing an item as much as rewording one. A
// finished room's list is the record of what the two sides owed each other, and
// ticking an item off months later would rewrite that record rather than reflect
// work anybody is still doing.
func notTaskEditable(current string) error {
	return &stateError{
		code:    "deal_room_task_not_editable",
		current: current,
		wanted:  "this room is finished and its to-do list is now a record: open a new Deal Room on the deal to track anything further",
	}
}

// codeRequired is the fault code every "you left this out" refusal in this
// module publishes, named once so the three that raise it cannot drift into
// three spellings a client would have to special-case.
const codeRequired = "required"

// errCompletionNeedsActor refuses a completion nobody can be attributed to. The
// schema requires a done item to name who did it, and a to-do list that says
// work was finished without saying by whom answers neither side's question.
var errCompletionNeedsActor = &fieldError{
	field: "done",
	code:  "actor_required",
	msg:   "completing an item records who did it, and this request carries no user to record",
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

// errAlreadyInvited refuses a second live seat for one address. It names
// revoking as the way out, because the caller's alternative — inviting the same
// person twice — is exactly what the index prevents.
var errAlreadyInvited = &messageError{
	code: "deal_room_participant_already_invited",
	msg:  "that address already has access to this room: revoke it first, or resend their invitation",
}

// errResendInFlight refuses a resend that raced another one. Both cannot stand:
// the index permits one live credential, and telling the caller to re-read is
// better than a 500 that invites a retry minting yet another.
var errResendInFlight = &messageError{
	code: "deal_room_resend_in_flight",
	msg:  "another invitation for this person was issued a moment ago: re-read the participant before resending",
}

// errRevokedNoResend refuses a resend to somebody whose access was taken away.
// Silently re-admitting them would turn a resend into an un-revoke, which is a
// different decision and belongs to a fresh invitation.
var errRevokedNoResend = &messageError{
	code: "deal_room_participant_revoked",
	msg:  "this person's access was revoked: invite the address again to admit them",
}

// errRevokedNoEdit refuses corrections to a revoked participant. Their row is
// kept to attribute what they already wrote, not to go on being managed.
var errRevokedNoEdit = &messageError{
	code: "deal_room_participant_revoked",
	msg:  "this person's access was revoked: their record is kept for attribution and is no longer editable",
}

// errAddressSettled refuses moving an address after its credential was used.
// Redirecting a link somebody has already signed in with would hand their
// standing access to a different person.
var errAddressSettled = &messageError{
	code: "deal_room_address_settled",
	msg:  "this person has already signed in, so their address is fixed: revoke them and invite the correct address",
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
