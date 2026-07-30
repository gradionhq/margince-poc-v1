// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// Whose account an update is about, decided BEFORE anything is persisted
// (design §10). The ingress webhook writes the verbatim update — numeric
// sender id, handle, names, message text — as the only copy of the message,
// and it does so before any domain code has classified the update at all. So
// the one question that has to be answerable at that moment, without a
// database read of the payload and without decoding it twice downstream, is
// which channel account the update belongs to: an installation that has
// erased a human may not go on storing their words because the refusal
// happens to sit further down the pipeline.
//
// This is the same identity Normalize and ParseMembership resolve, read from
// the same two places, and it is pure for the same reason they are.

import (
	"encoding/json"
	"fmt"
)

// subjectEnvelope decodes only the two update kinds this connector subscribes
// to (channelAllowedUpdates), each narrowed to the object the account id is
// read from.
type subjectEnvelope struct {
	Message      *telegramMessage   `json:"message"`
	MyChatMember *chatMemberUpdated `json:"my_chat_member"`
}

// SubjectAccountIDs returns the channel_user_id of every account one verbatim
// Telegram update is about — the value person_channel_identity is keyed on and
// the erasure suppression list hashes, so a caller can ask whether this
// installation may still hold data about the human behind it.
//
// The two reads are deliberately the ones the domain already makes, not a
// widened sweep of every user object the payload happens to contain: a
// message's subject is its sender (`message.from`), and a my_chat_member
// update's subject is the private chat, whose id IS the customer's own —
// `new_chat_member.user` there is the BOT (membership.go), so reading it would
// return an account no Person ever carries.
//
// An id of 0 is omitted rather than returned as "0": Telegram sends no `from`
// for an anonymous sender, and 0 is not an account any human owns (normalize.go
// refuses it for the same reason). Returning it would ask the suppression list
// about a key that cannot belong to anyone.
//
// An update naming no account at all returns empty, and that is not an error:
// an update kind neither function classifies is skipped further down the
// pipeline, and it has no subject to protect here.
func SubjectAccountIDs(update []byte) ([]string, error) {
	var env subjectEnvelope
	if err := json.Unmarshal(update, &env); err != nil {
		return nil, fmt.Errorf("telegram: decoding the update to read its subject account: %w", err)
	}
	var accounts []string
	if env.Message != nil && env.Message.From.ID != 0 {
		accounts = append(accounts, fmt.Sprintf("%d", env.Message.From.ID))
	}
	if env.MyChatMember != nil && env.MyChatMember.Chat.ID != 0 {
		accounts = append(accounts, fmt.Sprintf("%d", env.MyChatMember.Chat.ID))
	}
	return accounts, nil
}
