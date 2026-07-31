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
// sender 555 — the exact numbers the natural-key test asserts on. The chat
// carries its type because a real update does and because nothing outside a
// private chat is captured at all.
const telegramUpdateFixture = `{
	"update_id": 100,
	"message": {
		"message_id": 7,
		"chat": {"id": 1001, "type": "private", "username": "annlee"},
		"from": {"id": 555, "username": "annlee", "first_name": "Ann", "last_name": "Lee"},
		"date": 1690000000,
		"text": "hello"
	}
}`

// normalizeUpdate runs Normalize over one verbatim update as bot 42, handing
// back both results — the shared arrange step for a case whose claim is about
// the error as much as about the record.
func normalizeUpdate(t *testing.T, update string) ([]connector.NormalizedRecord, error) {
	t.Helper()
	raw, err := BuildRawEnvelope("42", []byte(update))
	if err != nil {
		t.Fatalf("BuildRawEnvelope: %v", err)
	}
	return Normalize(context.Background(), raw)
}

// normalizeOne runs one update that must produce exactly one record, failing
// the test on any error — the setup every assertion-focused case below starts
// from.
func normalizeOne(t *testing.T, update string) connector.NormalizedRecord {
	t.Helper()
	records, err := normalizeUpdate(t, update)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Normalize returned %d records, want 1", len(records))
	}
	return records[0]
}

// normalizeFixture is normalizeOne over the canonical private-chat message.
func normalizeFixture(t *testing.T) connector.NormalizedRecord {
	t.Helper()
	return normalizeOne(t, telegramUpdateFixture)
}

// bodyOf reads the activity body off a record, naming the Fields type when the
// assertion cannot even be attempted — a nil-body assertion would otherwise
// read as a passing test.
func bodyOf(t *testing.T, rec connector.NormalizedRecord) string {
	t.Helper()
	fields, ok := rec.Fields.(ActivityFields)
	if !ok {
		t.Fatalf("record carries %T, want telegram.ActivityFields", rec.Fields)
	}
	return fields.Body
}

// assertSkipped insists Normalize declined an update deliberately: no record,
// and an ErrSkip the worker counts as an exclusion rather than a fault.
func assertSkipped(t *testing.T, update string) {
	t.Helper()
	records, err := normalizeUpdate(t, update)
	if records != nil {
		t.Errorf("records = %v, want nil", records)
	}
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("got %v, want an ErrSkip-wrapped error", err)
	}
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
	assertSkipped(t, `{"update_id":101,"my_chat_member":{}}`)
}

// Group chats are out of scope (design §1) and `allowed_updates` cannot say so:
// Telegram delivers a group message under the same bare `message` update a
// private one arrives on. Capturing one would mint a Person per member the
// bot's privacy mode happens to show, file the activity under the group's
// thread, and then route the rep's reply to the sender's PRIVATE chat — a
// message answered somewhere other than where it was read.
func TestNormalizeSkipsAGroupChatMessage(t *testing.T) {
	assertSkipped(t, `{
		"update_id": 102,
		"message": {
			"message_id": 8,
			"chat": {"id": -1001234567890, "type": "supergroup", "title": "Acme Support"},
			"from": {"id": 555, "username": "annlee", "first_name": "Ann"},
			"date": 1690000100,
			"text": "/help please"
		}
	}`)
}

// The scope exclusion fails CLOSED: an update whose chat states no type is not
// evidence of a private conversation, and capturing it on the strength of a
// missing field would file a message nobody can be shown to be able to reply to.
func TestNormalizeSkipsAChatOfUnstatedType(t *testing.T) {
	assertSkipped(t, `{
		"update_id": 103,
		"message": {"message_id": 9, "chat": {"id": 1001}, "from": {"id": 555}, "date": 1690000100, "text": "hello"}
	}`)
}

// Telegram gives a supergroup or channel a NEGATIVE id, and the type field is
// the payload's own claim about itself. A chat calling itself private while
// carrying a group's id is therefore not a private chat, and admitting it on
// the strength of the label would file a group conversation as a customer's
// and then answer it in that group — publishing the rep's reply to whoever
// owns the id.
func TestNormalizeSkipsAChatWhoseIDIsNotAPrivateOne(t *testing.T) {
	assertSkipped(t, `{
		"update_id": 106,
		"message": {
			"message_id": 12,
			"chat": {"id": -1001234567890, "type": "private"},
			"from": {"id": 555, "username": "annlee"},
			"date": 1690000100,
			"text": "mislabelled"
		}
	}`)
}

// The same rule for the sender: an account id is positive, so a negative one
// names no Telegram account. Minted as an identity it would bind a Person to a
// channel_user_id no human owns and no reply can reach.
func TestNormalizeSkipsAMessageFromANonAccountSender(t *testing.T) {
	assertSkipped(t, `{
		"update_id": 107,
		"message": {
			"message_id": 13,
			"chat": {"id": 1001, "type": "private", "username": "annlee"},
			"from": {"id": -555},
			"date": 1690000100,
			"text": "who owns this id?"
		}
	}`)
}

// A sender id of 0 — Telegram's rendering of a message with no `from` — is a
// valid, non-empty key that EVERY anonymous sender shares. Captured, it merges
// distinct humans onto one Person and one identity row, which then reads as
// reachable at chat id 0. The private-chat gate excludes the shape this
// arrives in today, so the refusal is stated here, where the identity is
// minted, rather than left resting on a gate that exists for another reason.
func TestNormalizeSkipsAMessageWithNoSender(t *testing.T) {
	assertSkipped(t, `{
		"update_id": 104,
		"message": {
			"message_id": 10,
			"chat": {"id": 1001, "type": "private", "username": "annlee"},
			"date": 1690000100,
			"text": "who sent this?"
		}
	}`)
}

// Raw must stay EMPTY for Telegram, and this is the assertion that keeps it
// that way. The ingress webhook already stored this exact update as the
// only-copy evidence row before answering 200; a record carrying Raw makes the
// Sink store the same bytes a second time under a different key with the
// opposite conflict rule — every inbound message duplicated in the largest
// column, and handed to the subject twice in their Art. 15 export.
func TestNormalizeCarriesNoRawBecauseTheWebhookOwnsTheEvidenceCopy(t *testing.T) {
	if raw := normalizeFixture(t).Raw; len(raw) != 0 {
		t.Errorf("Raw = %s, want empty — the webhook's raw_capture row is the single evidence copy", raw)
	}
}

// A text message's body is its text — the baseline the media cases below vary.
func TestNormalizeCapturesTheMessageTextAsTheBody(t *testing.T) {
	if body := bodyOf(t, normalizeFixture(t)); body != "hello" {
		t.Errorf("Body = %q, want %q", body, "hello")
	}
}

// Telegram puts the words of a media message in `caption`, never in `text`, so
// a connector reading only `text` files a photo-with-a-caption as an activity
// with an empty body: the customer's sentence is on the wire and nowhere on the
// timeline.
func TestNormalizeKeepsAMediaCaptionAsTheBody(t *testing.T) {
	rec := normalizeOne(t, `{
		"update_id": 104,
		"message": {
			"message_id": 10,
			"chat": {"id": 1001, "type": "private", "username": "annlee"},
			"from": {"id": 555, "username": "annlee", "first_name": "Ann"},
			"date": 1690000100,
			"photo": [{"file_id": "AgACAgQ", "width": 90, "height": 90}],
			"caption": "here is the damaged part"
		}
	}`)
	if body := bodyOf(t, rec); body != "here is the damaged part" {
		t.Errorf("Body = %q, want the caption", body)
	}
}

// A wordless media message is captured with a placeholder naming what arrived,
// not skipped and not left blank: the customer did reach out, and both of the
// alternatives leave the rep a timeline that says otherwise while the reply box
// offers to answer it.
func TestNormalizeNamesTheMediaKindWhenAMessageHasNoWords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"photo", `"photo": [{"file_id": "AgACAgQ"}]`, "[photo]"},
		{"sticker", `"sticker": {"file_id": "CAACAgIA", "emoji": "👍"}`, "[sticker]"},
		{"voice", `"voice": {"file_id": "AwACAgQ", "duration": 3}`, "[voice message]"},
		// Telegram sends a GIF as an animation AND a document; the animation is
		// the truer of the two descriptions.
		{"animation", `"animation": {"file_id": "CgACAgQ"}, "document": {"file_id": "BQACAgQ"}`, "[animation]"},
		// A kind this package does not model (a poll here) still says something
		// arrived rather than vanishing.
		{"unmodelled kind", `"poll": {"question": "which day suits?"}`, "[attachment]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := normalizeOne(t, `{
				"update_id": 105,
				"message": {
					"message_id": 11,
					"chat": {"id": 1001, "type": "private", "username": "annlee"},
					"from": {"id": 555, "username": "annlee"},
					"date": 1690000100,
					`+tc.payload+`
				}
			}`)
			if body := bodyOf(t, rec); body != tc.want {
				t.Errorf("Body = %q, want %q", body, tc.want)
			}
		})
	}
}
