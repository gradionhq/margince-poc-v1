// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// ParseMembership's own proof (design §4.2 D9): pure classification, no
// database — every case here carries a real my_chat_member shape, because the
// whole risk this file guards is a fixture that agrees with the parser instead
// of with Telegram.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// telegramBlockedFixture is what Telegram actually posts when customer 556
// blocks bot 42 in their private chat: the chat is the customer's own (a
// private chat's id IS the user's id), `from` is the customer who performed
// the block, and BOTH chat members are the BOT — my_chat_member reports a
// change to the bot's own standing, nothing else.
const telegramBlockedFixture = `{
	"update_id": 200,
	"my_chat_member": {
		"chat": {"id": 556, "type": "private", "username": "blockeduser", "first_name": "Blocks"},
		"from": {"id": 556, "username": "blockeduser", "first_name": "Blocks"},
		"date": 1690000200,
		"old_chat_member": {"user": {"id": 42, "is_bot": true, "username": "acme_bot"}, "status": "member"},
		"new_chat_member": {"user": {"id": 42, "is_bot": true, "username": "acme_bot"}, "status": "kicked"}
	}
}`

// parseMembershipUpdate runs ParseMembership over one verbatim update as bot 42.
func parseMembershipUpdate(t *testing.T, update string) (Membership, bool) {
	t.Helper()
	raw, err := BuildRawEnvelope("42", []byte(update))
	if err != nil {
		t.Fatalf("BuildRawEnvelope: %v", err)
	}
	mem, ok, err := ParseMembership(raw)
	if err != nil {
		t.Fatalf("ParseMembership: %v", err)
	}
	return mem, ok
}

// The identity a my_chat_member names is the CHAT's, never
// new_chat_member.user's: that user is the bot. Reading it there keys the
// reachability write on the bot's id, which no Person carries, so
// SetChannelIdentityBlocked updates zero rows and reports success — the
// customer keeps rendering as reachable and every reply is accepted only to
// fail at Telegram.
func TestParseMembershipReadsTheCustomerFromTheChatNotTheBot(t *testing.T) {
	mem, ok := parseMembershipUpdate(t, telegramBlockedFixture)
	if !ok {
		t.Fatal("ParseMembership declined a private-chat my_chat_member update")
	}
	want := connector.ChannelIdentity{Provider: Provider, ChannelUserID: "556", Username: "blockeduser"}
	if mem.Identity != want {
		t.Errorf("Identity = %+v, want %+v — 42 is the bot's id, not the customer's", mem.Identity, want)
	}
	if mem.Status != StatusKicked {
		t.Errorf("Status = %q, want %q", mem.Status, StatusKicked)
	}
	// update_id sits beside my_chat_member, not inside it, and it is the only
	// thing that orders this transition against the one that may be racing it
	// downstream — dropped here, the reachability write has nothing to order by.
	if mem.UpdateID != 200 {
		t.Errorf("UpdateID = %d, want 200 — it is read from the update, not the membership payload", mem.UpdateID)
	}
}

// An unblock is the same update with the bot's status back to member — the
// edge that CLEARS blocked_at, and it has to read the same identity as the
// block did or the two never cancel out.
func TestParseMembershipReadsAnUnblockOfTheSameIdentity(t *testing.T) {
	mem, ok := parseMembershipUpdate(t, `{
		"update_id": 201,
		"my_chat_member": {
			"chat": {"id": 556, "type": "private", "username": "blockeduser"},
			"from": {"id": 556, "username": "blockeduser"},
			"date": 1690000300,
			"old_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "kicked"},
			"new_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "member"}
		}
	}`)
	if !ok {
		t.Fatal("ParseMembership declined a private-chat my_chat_member update")
	}
	if mem.Identity.ChannelUserID != "556" || mem.Status != StatusMember {
		t.Errorf("got %+v with status %q, want customer 556 and status %q", mem.Identity, mem.Status, StatusMember)
	}
}

// A group's my_chat_member reports the BOT being promoted, added or removed —
// no customer's reachability changed, and group chats are out of scope anyway
// (normalize.go's chatTypePrivate). Classifying it as membership would write a
// reachability state against the group's own negative id.
func TestParseMembershipDeclinesAGroupChatUpdate(t *testing.T) {
	if _, ok := parseMembershipUpdate(t, `{
		"update_id": 202,
		"my_chat_member": {
			"chat": {"id": -1001234567890, "type": "supergroup", "title": "Acme Support"},
			"from": {"id": 555, "username": "annlee"},
			"date": 1690000400,
			"old_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "member"},
			"new_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "administrator"}
		}
	}`); ok {
		t.Error("ParseMembership claimed a supergroup update as a customer reachability change")
	}
}

// A message update is not a membership update: ok=false is what sends it down
// the Normalize path instead, so a true here would swallow every message.
func TestParseMembershipDeclinesAMessageUpdate(t *testing.T) {
	if _, ok := parseMembershipUpdate(t, telegramUpdateFixture); ok {
		t.Error("ParseMembership claimed a message update as a membership change")
	}
}

// Malformed provider JSON is an error, never a silent "not a membership
// update" — a decode fault that read as ok=false would send the payload to
// Normalize, which would fail on the same bytes and report the wrong reason.
func TestParseMembershipFailsOnAnUndecodableUpdate(t *testing.T) {
	if _, _, err := ParseMembership(connector.RawRecord(`{"bot_id":"42","update":"not-an-object"}`)); err == nil {
		t.Error("ParseMembership accepted an update that is not a JSON object")
	}
}

// A my_chat_member update whose chat id is 0 names no addressable customer:
// this function reads the account OUT of the chat, so 0 would write a
// reachability state against "telegram:0" — an account every such update
// shares. Declining is the same answer a group update gets, which sends it to
// Normalize and the deliberate-skip path.
func TestParseMembershipDeclinesAnUpdateWithNoChatID(t *testing.T) {
	if _, ok := parseMembershipUpdate(t, `{
		"update_id": 203,
		"my_chat_member": {
			"chat": {"type": "private"},
			"new_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "kicked"}
		}
	}`); ok {
		t.Error("ParseMembership claimed a chat-less update as a reachability change for account 0")
	}
}
