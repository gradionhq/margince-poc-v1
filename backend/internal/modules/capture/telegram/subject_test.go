// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// InScopeSubjects decides, before a single byte is persisted, whose account an
// update belongs to and whether the update is one this connector captures at
// all. The ingress webhook stores nothing when it answers empty, so every shape
// Telegram actually posts is asserted here — an over-generous answer puts a
// verbatim payload in the only-copy store with no subject any erasure could
// reach it by.

import (
	"slices"
	"testing"
)

// A message's subject is its sender. Reading the CHAT instead would agree with
// this fixture — a private chat's id is the sender's own — and disagree with
// the identity Normalize mints, which is what the suppression list is keyed on.
func TestInScopeSubjectsReadsAMessagesSender(t *testing.T) {
	got, err := InScopeSubjects([]byte(telegramUpdateFixture))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if !slices.Equal(got, []string{"555"}) {
		t.Errorf("accounts = %v, want [555] — the sender of the fixture message", got)
	}
}

// A my_chat_member update's subject is the private CHAT, whose id is the
// customer's own account. new_chat_member.user is the BOT: an extractor reading
// it would hand the suppression probe an id no Person carries, so an erased
// subject's block/unblock report would be persisted verbatim.
func TestInScopeSubjectsReadsAMembershipUpdatesChatNotTheBot(t *testing.T) {
	got, err := InScopeSubjects([]byte(telegramBlockedFixture))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if !slices.Equal(got, []string{"556"}) {
		t.Errorf("accounts = %v, want [556] — the customer's chat, not bot 42", got)
	}
}

// A group message names no subject this installation may keep. Design §1 puts
// group chats out of scope, so no record is ever made of one — which means no
// person_channel_identity, which means neither the erasure raw purge nor the
// subject-access raw section can ever reach the stored payload again. Answering
// with the sender's account here would let the webhook store their id, handle,
// names and every word they wrote, permanently and unerasably.
func TestInScopeSubjectsRefusesAGroupChat(t *testing.T) {
	got, err := InScopeSubjects([]byte(`{
		"update_id": 902,
		"message": {
			"message_id": 3,
			"chat": {"id": -100123, "type": "supergroup", "title": "Acme staff"},
			"from": {"id": 557, "username": "grouptalker", "first_name": "Gina"},
			"date": 1690000300,
			"text": "/help with the invoice"
		}
	}`))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none — a group message must never be persisted", got)
	}
}

// The same rule for the other update kind: a my_chat_member in a group reports
// the BOT being added or removed, so no customer's reachability changed and
// there is nobody the payload could later be erased by.
func TestInScopeSubjectsRefusesAGroupMembershipUpdate(t *testing.T) {
	got, err := InScopeSubjects([]byte(`{
		"update_id": 903,
		"my_chat_member": {
			"chat": {"id": -100124, "type": "group", "title": "Acme staff"},
			"new_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "member"}
		}
	}`))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none — the bot joining a group is not a customer", got)
	}
}

// A message with no `from` at all decodes to sender id 0, which is not an
// account: every anonymous sender would share it, so probing the suppression
// list with "0" would ask about a key no human can own — and, worse, one that
// an erasure could arm for all of them at once.
func TestInScopeSubjectsOmitsAnAbsentSender(t *testing.T) {
	got, err := InScopeSubjects([]byte(`{
		"update_id": 900,
		"message": {"message_id": 1, "chat": {"id": 1001, "type": "private"}, "text": "anonymous"}
	}`))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none — sender id 0 is not an account", got)
	}
}

// An update kind this connector does not subscribe to names no subject, and
// that is not a fault: nothing downstream would capture it, so persisting it
// would leave the same unreachable payload a group message would.
func TestInScopeSubjectsReportsNoSubjectForAnUnrelatedUpdate(t *testing.T) {
	got, err := InScopeSubjects([]byte(`{"update_id": 901, "poll": {"id": "7"}}`))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none", got)
	}
}

// The Bot API posts exactly one kind per update. A payload carrying two is a
// shape nothing here classifies, so it fails closed rather than admitting one
// half of itself on the strength of the other.
func TestInScopeSubjectsRefusesAnUpdateCarryingBothKinds(t *testing.T) {
	got, err := InScopeSubjects([]byte(`{
		"update_id": 904,
		"message": {
			"message_id": 4,
			"chat": {"id": -100125, "type": "supergroup"},
			"from": {"id": 558},
			"text": "smuggled"
		},
		"my_chat_member": {
			"chat": {"id": 559, "type": "private"},
			"new_chat_member": {"user": {"id": 42, "is_bot": true}, "status": "member"}
		}
	}`))
	if err != nil {
		t.Fatalf("InScopeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none — a private membership report must not admit a group message riding with it", got)
	}
}

// Undecodable bytes are an error, never an empty answer: an empty answer reads
// as "nothing to capture" and would have the webhook answer 200 to a delivery
// it never understood, which Telegram will then never resend.
func TestInScopeSubjectsRefusesUndecodableBytes(t *testing.T) {
	if _, err := InScopeSubjects([]byte(`{"update_id":`)); err == nil {
		t.Error("InScopeSubjects accepted truncated JSON — a decode fault must not read as 'nothing to capture'")
	}
}
