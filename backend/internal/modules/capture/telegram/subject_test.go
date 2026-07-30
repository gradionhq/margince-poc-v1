// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// SubjectAccountIDs decides, before a single byte is persisted, whose account
// an update belongs to. Everything the ingress webhook does about an erased
// subject hangs off it, so what it reads (and refuses to read) is asserted here
// against the shapes Telegram actually posts.

import (
	"slices"
	"testing"
)

// A message's subject is its sender. Reading the CHAT instead would agree with
// this fixture — a private chat's id is the sender's own — and disagree with
// the identity Normalize mints, which is what the suppression list is keyed on.
func TestSubjectAccountIDsReadsAMessagesSender(t *testing.T) {
	got, err := SubjectAccountIDs([]byte(telegramUpdateFixture))
	if err != nil {
		t.Fatalf("SubjectAccountIDs: %v", err)
	}
	if !slices.Equal(got, []string{"555"}) {
		t.Errorf("accounts = %v, want [555] — the sender of the fixture message", got)
	}
}

// A my_chat_member update's subject is the private CHAT, whose id is the
// customer's own account. new_chat_member.user is the BOT: an extractor reading
// it would hand the suppression probe an id no Person carries, so an erased
// subject's block/unblock report would be persisted verbatim.
func TestSubjectAccountIDsReadsAMembershipUpdatesChatNotTheBot(t *testing.T) {
	got, err := SubjectAccountIDs([]byte(telegramBlockedFixture))
	if err != nil {
		t.Fatalf("SubjectAccountIDs: %v", err)
	}
	if !slices.Equal(got, []string{"556"}) {
		t.Errorf("accounts = %v, want [556] — the customer's chat, not bot 42", got)
	}
}

// A message with no `from` at all decodes to sender id 0, which is not an
// account: every anonymous sender would share it, so probing the suppression
// list with "0" would ask about a key no human can own — and, worse, one that
// an erasure could arm for all of them at once.
func TestSubjectAccountIDsOmitsAnAbsentSender(t *testing.T) {
	got, err := SubjectAccountIDs([]byte(`{
		"update_id": 900,
		"message": {"message_id": 1, "chat": {"id": -100123, "type": "supergroup"}, "text": "anonymous"}
	}`))
	if err != nil {
		t.Fatalf("SubjectAccountIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none — sender id 0 is not an account", got)
	}
}

// An update kind this connector does not subscribe to names no subject, and
// that is not a fault: it is skipped further down the pipeline and there is
// nobody to protect here.
func TestSubjectAccountIDsReportsNoSubjectForAnUnrelatedUpdate(t *testing.T) {
	got, err := SubjectAccountIDs([]byte(`{"update_id": 901, "poll": {"id": "7"}}`))
	if err != nil {
		t.Fatalf("SubjectAccountIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("accounts = %v, want none", got)
	}
}

// Undecodable bytes are an error, never an empty answer: an empty answer reads
// as "nobody to protect" and would wave the payload straight through to the
// only-copy store.
func TestSubjectAccountIDsRefusesUndecodableBytes(t *testing.T) {
	if _, err := SubjectAccountIDs([]byte(`{"update_id":`)); err == nil {
		t.Error("SubjectAccountIDs accepted truncated JSON — a decode fault must not read as 'no subject'")
	}
}
