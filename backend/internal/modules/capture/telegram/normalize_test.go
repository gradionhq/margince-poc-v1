// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// Normalize's own table-driven proof (design §6.3): pure, no provider
// handle, no database — every case here builds the raw envelope
// BuildRawEnvelope produces for the worker and asserts on the literal
// strings the brief specifies.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// telegramUpdateFixture is one text message from chat 1001, message id 7,
// sender 555 — the exact numbers the natural-key test asserts on.
const telegramUpdateFixture = `{
	"update_id": 100,
	"message": {
		"message_id": 7,
		"chat": {"id": 1001},
		"from": {"id": 555, "username": "annlee", "first_name": "Ann", "last_name": "Lee"},
		"date": 1690000000,
		"text": "hello"
	}
}`

// normalizeFixture builds the envelope BuildRawEnvelope produces for bot 42
// and runs Normalize over it, failing the test on any error — the shared
// setup every assertion-focused case below starts from.
func normalizeFixture(t *testing.T) connector.NormalizedRecord {
	t.Helper()
	raw, err := BuildRawEnvelope("42", []byte(telegramUpdateFixture))
	if err != nil {
		t.Fatalf("BuildRawEnvelope: %v", err)
	}
	records, err := Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Normalize returned %d records, want 1", len(records))
	}
	return records[0]
}

// message_id is unique only WITHIN a chat, so the natural key must be
// chat-scoped — a key omitting the chat would collide two different
// customers' (or two different bots') conversations into one activity.
func TestNormalizeBuildsTheChatScopedNaturalKey(t *testing.T) {
	rec := normalizeFixture(t)
	if rec.NaturalKey.SourceID != "42:1001:7" {
		t.Errorf("NaturalKey.SourceID = %q, want %q", rec.NaturalKey.SourceID, "42:1001:7")
	}
	if rec.NaturalKey.SourceSystem != Provider {
		t.Errorf("NaturalKey.SourceSystem = %q, want %q", rec.NaturalKey.SourceSystem, Provider)
	}
}

// The conversation IS the chat for a channel (connector.go's amended
// ThreadKey comment) — CAP-FORMULA-1 joins on this the same way it joins a
// mail thread on a Message-ID root.
func TestNormalizeSetsThreadKeyToTheChat(t *testing.T) {
	rec := normalizeFixture(t)
	if rec.ThreadKey != "telegram:42:1001" {
		t.Errorf("ThreadKey = %q, want %q", rec.ThreadKey, "telegram:42:1001")
	}
}

// A Telegram counterparty has no address at all — it is identified by
// ChannelIdentity instead of Email, the two being mutually exclusive on
// Counterparty (connector.go).
func TestNormalizeCarriesNoEmailButAChannelIdentity(t *testing.T) {
	rec := normalizeFixture(t)
	if rec.Counterparty.Email != "" {
		t.Errorf("Counterparty.Email = %q, want empty", rec.Counterparty.Email)
	}
	want := connector.ChannelIdentity{Provider: Provider, ChannelUserID: "555", Username: "annlee"}
	if rec.Counterparty.ChannelIdentity != want {
		t.Errorf("Counterparty.ChannelIdentity = %+v, want %+v", rec.Counterparty.ChannelIdentity, want)
	}
}

// A my_chat_member update (the block/unblock signal Task 11 owns) carries no
// message at all; Normalize must skip it rather than error, exactly like a
// mail connector's own deliberate exclusions.
func TestNormalizeSkipsAnUpdateWithNoMessage(t *testing.T) {
	raw, err := BuildRawEnvelope("42", []byte(`{"update_id":101,"my_chat_member":{}}`))
	if err != nil {
		t.Fatalf("BuildRawEnvelope: %v", err)
	}
	records, err := Normalize(context.Background(), raw)
	if records != nil {
		t.Errorf("records = %v, want nil", records)
	}
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("got %v, want an ErrSkip-wrapped error", err)
	}
}
