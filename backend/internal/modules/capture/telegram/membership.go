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
// (1:1 bot) chat can actually report — ParseMembership refuses every other
// chat kind, so a group's own statuses (restricted, administrator, creator)
// never reach a caller of this package and are deliberately not named here.
// StatusKicked and StatusMember are design §4.2/D9's two reachability edges.
const (
	StatusKicked = "kicked" // the user blocked the bot
	StatusMember = "member" // the user started or unblocked the bot

	// StatusLeft covers a user who has stopped the bot without blocking it
	// (or never started it) — Telegram sends it as the initial standing on
	// first contact too. Neither edge changes reachability: nothing this
	// system tracks depended on "left" ever being true, and there is no
	// blocked_at to clear or set on its account.
	StatusLeft = "left"
)

// Membership is one my_chat_member update, pure-parsed: the identity it
// names and the status Telegram reported for it.
type Membership struct {
	Identity connector.ChannelIdentity
	Status   string
}

// chatMember is the `new_chat_member` object, narrowed to the one field that
// says anything about the customer: the status. Its `user` is deliberately not
// modelled — see chatMemberUpdated for why that user is the wrong identity.
type chatMember struct {
	Status string `json:"status"`
}

// chatMemberUpdated is the `my_chat_member` field's own payload shape, and the
// customer's identity comes from Chat.
//
// `my_chat_member` reports a change to THE BOT's own membership, so
// new_chat_member describes the bot's standing and new_chat_member.user IS the
// bot: keying reachability on it would write against the bot's numeric id, an
// id no Person ever carries, so the update would report success having changed
// nothing. A private chat's id, by contrast, IS the counterpart user's id —
// exactly the id person_channel_identity is keyed on. `from` names whoever
// PERFORMED the change, which coincides with the subject only because a private
// chat holds nobody else; the chat is the subject definitionally, so that is
// what this reads.
type chatMemberUpdated struct {
	Chat          telegramChat `json:"chat"`
	NewChatMember chatMember   `json:"new_chat_member"`
}

// membershipEnvelope reads only the one field ParseMembership needs out of
// Telegram's update JSON. A pointer field (not a value) is how "this update
// carries no my_chat_member at all" is told apart from "it carries an empty
// one" — the same reason telegramUpdate.Message is a pointer in normalize.go.
type membershipEnvelope struct {
	MyChatMember *chatMemberUpdated `json:"my_chat_member"`
}

// ParseMembership reports whether raw (the same BuildRawEnvelope output
// Normalize consumes) carries a PRIVATE-chat my_chat_member update. ok is
// false for every other update kind — a message, an edited_message, anything
// this package does not classify as membership — which tells the caller to
// fall through to the message path instead.
//
// ok is false for a my_chat_member in a group too, and that is the same
// answer rather than a special one: a group's my_chat_member reports the BOT
// being added or removed, so no customer's reachability changed, and group
// chats are out of scope besides (normalize.go's chatTypePrivate). Falling
// through lands it on Normalize, which carries no message and so counts it as
// the deliberate skip it is.
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
	upd := mem.MyChatMember
	if !upd.Chat.isPrivate() {
		return Membership{}, false, nil
	}
	return Membership{
		Identity: connector.ChannelIdentity{
			Provider:      Provider,
			ChannelUserID: fmt.Sprintf("%d", upd.Chat.ID),
			Username:      upd.Chat.Username,
		},
		Status: upd.NewChatMember.Status,
	}, true, nil
}
