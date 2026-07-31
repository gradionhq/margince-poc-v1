// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// Whose account an update is about, decided BEFORE anything is persisted
// (design §10). The poller writes the verbatim update — numeric sender id,
// handle, names, message text — as the only copy of the message, and it does so
// before any domain code has classified the update at all. So the one question
// that has to be answerable at that moment, without a database read of the
// payload and without decoding it twice downstream, is which channel account the
// update belongs to: an installation that has erased a human may not go on
// storing their words because the refusal happens to sit further down the
// pipeline.
//
// This is the same identity Normalize and ParseMembership resolve, read from
// the same two places, and it is pure for the same reason they are.

import (
	"encoding/json"
	"fmt"
)

// AllowedUpdates narrows what the poller asks Telegram for: the messages a
// person writes, and the membership changes that tell us a person blocked or
// unblocked the bot. Anything else (polls, inline queries, edited channel
// posts) is bandwidth this system has no reader for, and asking for it would
// mean fetching updates nobody consumes.
//
// It lives beside subjectEnvelope because the two must name the SAME set. A
// kind subscribed here with no arm below falls to InScopeSubjects' `default:
// return nil, nil` and is dropped silently at ingress — which reads exactly
// like a bot nobody is messaging.
func AllowedUpdates() []string { return []string{"message", "my_chat_member"} }

// subjectEnvelope decodes only the update kinds this connector subscribes to
// (AllowedUpdates), each narrowed to the object the account id is read from.
type subjectEnvelope struct {
	Message      *telegramMessage   `json:"message"`
	MyChatMember *chatMemberUpdated `json:"my_chat_member"`
}

// InScopeSubjects returns the channel_user_id of every account one verbatim
// Telegram update is about — the value person_channel_identity is keyed on and
// the erasure suppression list hashes — and returns NONE for an update this
// connector does not capture.
//
// An empty result is therefore the poller's whole refusal test, and the reason
// this function answers scope and subject together rather than leaving the scope
// decision to the worker that normalizes the payload later.
// A record this connector captures always names a human the erasure and SAR
// lanes can reach it by: they drive off person_channel_identity, which only a
// captured record ever creates. An update outside that scope names nobody
// those lanes can reach, so a verbatim copy of it would sit in raw_capture —
// sender id, handle, first and last name, full message text — beyond the reach
// of any later Art. 17 request, with no retention sweep to age it out.
// Refusing to persist is the only point at which that data can be kept out.
//
// Out of scope, and each for a reason the domain already states:
//   - a non-private chat, because design §1 puts group chats out of scope and
//     the connector could neither read nor answer one (normalize.go's
//     chatTypePrivate);
//   - an update Telegram names no account for — an anonymous group admin posts
//     under sender_chat, leaving `from` absent and the id 0, which is not an
//     account any human owns, and neither is a negative id, which is how
//     Telegram numbers chats rather than users;
//   - an update kind outside the two this connector subscribes to, or one
//     carrying both of them, which is not a shape the Bot API posts.
//
// The two reads are deliberately the ones the domain already makes, not a
// widened sweep of every user object the payload happens to contain: a
// message's subject is its sender (`message.from`), and a my_chat_member
// update's subject is the private chat, whose id IS the customer's own —
// `new_chat_member.user` there is the BOT (membership.go), so reading it would
// return an account no Person ever carries.
func InScopeSubjects(update []byte) ([]string, error) {
	var env subjectEnvelope
	if err := json.Unmarshal(update, &env); err != nil {
		return nil, fmt.Errorf("telegram: decoding the update to read its subject account: %w", err)
	}
	switch {
	case env.Message != nil && env.MyChatMember == nil:
		return subjectOf(env.Message.Chat, env.Message.From.ID), nil
	case env.MyChatMember != nil && env.Message == nil:
		// The chat IS the subject of a membership report, so its id is read
		// twice here: once as the scope gate, once as the account.
		return subjectOf(env.MyChatMember.Chat, env.MyChatMember.Chat.ID), nil
	default:
		return nil, nil
	}
}

// subjectOf answers for one update kind: the account, when the chat is one this
// connector captures and Telegram named an account at all; nothing otherwise.
//
// The account test is the sign test Normalize's identity mint applies, and the
// two must stay identical: this function decides what the POLLER persists and
// Normalize decides what is captured, so an id admitted here and refused there
// is a verbatim payload in the only-copy store with no person_channel_identity
// the erasure or SAR lanes could ever reach it by.
func subjectOf(chat telegramChat, account int64) []string {
	if !chat.isPrivate() || account <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d", account)}
}
