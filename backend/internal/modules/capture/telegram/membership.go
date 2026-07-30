// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The my_chat_member update (design §4.2 D9): Telegram's report that one
// user's standing toward the bot changed — blocked it, unblocked it, or
// something the two states below don't cover. It is not a message and never
// becomes one; ParseMembership is the pure classification the ingest worker
// runs BEFORE Normalize, so this update kind never takes the message path
// and never mints an activity.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Telegram's chat_member.status vocabulary, restricted to what a PRIVATE
// (1:1 bot) chat can actually report. StatusKicked and StatusMember are
// design §4.2/D9's two reachability edges. The rest are named so a status
// this package receives is always matched deliberately, never fallen
// through silently — see Membership.Handled.
const (
	StatusKicked = "kicked" // the user blocked the bot
	StatusMember = "member" // the user started or unblocked the bot

	// StatusLeft covers a user who has stopped the bot without blocking it
	// (or never started it) — Telegram sends it as the initial standing on
	// first contact too. Neither edge changes reachability: nothing this
	// system tracks depended on "left" ever being true, and there is no
	// blocked_at to clear or set on its account.
	StatusLeft = "left"

	// StatusRestricted, StatusAdministrator and StatusCreator are real
	// values of the SAME field in a GROUP chat's my_chat_member update. A
	// private bot chat never sends them, but the field is one shared enum
	// across both chat kinds, so they are named — and logged as unhandled by
	// the worker — rather than silently absorbed into "no-op" alongside
	// StatusLeft.
	StatusRestricted    = "restricted"
	StatusAdministrator = "administrator"
	StatusCreator       = "creator"
)

// Membership is one my_chat_member update, pure-parsed: the identity it
// names and the status Telegram reported for it.
type Membership struct {
	Identity connector.ChannelIdentity
	Status   string
}

// chatMember is the `new_chat_member` object: the user whose standing this
// update reports, and what it now is.
type chatMember struct {
	User   telegramUser `json:"user"`
	Status string       `json:"status"`
}

// chatMemberUpdated is the `my_chat_member` field's own payload shape.
type chatMemberUpdated struct {
	NewChatMember chatMember `json:"new_chat_member"`
}

// membershipEnvelope reads only the one field ParseMembership needs out of
// Telegram's update JSON. A pointer field (not a value) is how "this update
// carries no my_chat_member at all" is told apart from "it carries an empty
// one" — the same reason telegramUpdate.Message is a pointer in normalize.go.
type membershipEnvelope struct {
	MyChatMember *chatMemberUpdated `json:"my_chat_member"`
}

// ParseMembership reports whether raw (the same BuildRawEnvelope output
// Normalize consumes) carries a my_chat_member update. ok is false for
// every other update kind — a message, an edited_message, anything this
// package does not classify as membership — which tells the caller to fall
// through to the message path instead.
func ParseMembership(raw connector.RawRecord) (Membership, bool, error) {
	var env ingestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Membership{}, false, fmt.Errorf("telegram: decoding the ingest envelope: %w", err)
	}
	var mem membershipEnvelope
	if err := json.Unmarshal(env.Update, &mem); err != nil {
		return Membership{}, false, fmt.Errorf("telegram: decoding the update: %w", err)
	}
	if mem.MyChatMember == nil {
		return Membership{}, false, nil
	}
	who := mem.MyChatMember.NewChatMember
	return Membership{
		Identity: connector.ChannelIdentity{
			Provider:      Provider,
			ChannelUserID: fmt.Sprintf("%d", who.User.ID),
			Username:      who.User.Username,
		},
		Status: who.Status,
	}, true, nil
}
