// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The outbound seam's two obligations, against the local stand-in in
// api_test.go — never the real host.
//
// One is the MAPPING: a channel message has to arrive at the Bot API as the chat,
// the text and the anchor it named, because a reply that loses its anchor reads
// to the customer as a message out of nowhere. The other is the CLASSIFICATION,
// which is a safety property rather than a nicety: the dispatcher decides whether
// to try again purely from the class this file produces, and one mis-mapped
// sentinel is either a message that never goes or one the customer receives
// twice.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// reply is the ordinary channel message every case here sends: a reply to a chat
// the customer opened, anchored on the message it answers.
func reply() connector.ChannelMessage {
	return connector.ChannelMessage{
		Recipient: connector.ChannelIdentity{
			Provider: ProviderName, ChannelUserID: "778899", Username: "buyer",
		},
		Body:           "On its way today.",
		ReplyTo:        "4231",
		IdempotencyKey: "01920000-0000-7000-8000-000000000001",
	}
}

func TestSendMessageMapsTheChannelMessageOntoTheBotAPIRequest(t *testing.T) {
	api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)
	c := New(api)

	receipt, err := c.SendMessage(context.Background(), connector.Auth("1:secret"), reply())
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if receipt.ProviderMessageID != "9911" {
		t.Errorf("provider message id = %q, want \"9911\" — a later reply threads under it", receipt.ProviderMessageID)
	}
	// A channel has no mail identity, so there is no re-key owed and the
	// receipt's RFC822 field must stay empty rather than carry a stand-in.
	if receipt.RFC822MessageID != "" {
		t.Errorf("RFC822 identity = %q on a channel receipt, want empty", receipt.RFC822MessageID)
	}

	body := rec.lastBody(t)
	for _, want := range []string{`"chat_id":778899`, `"text":"On its way today."`, `"message_id":4231`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body %s does not carry %s", body, want)
		}
	}
	if !strings.Contains(rec.lastPath(t), "/sendMessage") {
		t.Errorf("request went to %q, want the sendMessage method", rec.lastPath(t))
	}
}

// An unanchored message is the legitimate case, and it must omit the anchor
// rather than send a zero one: Telegram treats reply_parameters as a real
// reference and refuses a message id of 0.
func TestSendMessageOmitsTheAnchorWhenThereIsNoneToCarry(t *testing.T) {
	api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)
	msg := reply()
	msg.ReplyTo = ""

	if _, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if strings.Contains(rec.lastBody(t), "reply_parameters") {
		t.Errorf("request body %s carries an anchor for a message that named none", rec.lastBody(t))
	}
}

// The 429 branch, which is the whole reason a throttle is retryable at all: it is
// a definite answer, and Telegram states when to come back. Reading the interval
// from the provider rather than backing off on a schedule of our own is what
// keeps a rate limit from escalating — the bot is shared by the whole workspace.
func TestSendMessageHonoursTheStatedRetryAfterOn429(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want time.Duration
	}{
		{
			"the interval Telegram states in the envelope",
			`{"ok":false,"description":"Too Many Requests: retry later","parameters":{"retry_after":30}}`,
			30 * time.Second,
		},
		{
			// No interval to honour: the caller falls back to its own backoff,
			// which a zero is how this seam says so.
			"a throttle that names no interval",
			`{"ok":false,"description":"Too Many Requests: retry later"}`,
			0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := serve(t, 429, tc.body)

			_, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), reply())
			limited, throttled := errors.AsType[*connector.RateLimitedError](err)
			if !throttled {
				t.Fatalf("SendMessage on a 429 = %v; the shared rate-limit class is how the interval reaches the ladder", err)
			}
			if limited.RetryAfter != tc.want {
				t.Errorf("retry after = %v, want %v", limited.RetryAfter, tc.want)
			}
			// A throttle transmitted NOTHING, so it must not read as an outcome
			// the caller can never learn — that class is never retried, and a
			// throttled reply would die permanently.
			if errors.Is(err, connector.ErrSendOutcomeUnknown) {
				t.Errorf("a 429 also reads as an unknown outcome; a rate-limited message would never be retried")
			}
		})
	}
}

// The classification table, read as the dispatcher reads it. Each row is a
// different decision about a real customer's message, which is why they are
// pinned together rather than left to the one case that happened to be exercised.
func TestSendMessageClassifiesEveryFailureTheDispatcherActsOn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
		// notUnknown pins the rows that must stay RETRYABLE: an unknown outcome
		// is never tried again, so a definite refusal misread as one is a
		// message silently abandoned.
		notUnknown bool
	}{
		{
			// The bot token is refused. Parking and naming the credential is the
			// only useful answer; retrying cannot mint a new token.
			"a refused bot token is a credential fault",
			401, `{"ok":false,"description":"Unauthorized"}`,
			connector.ErrAuthRejected, true,
		},
		{
			// Telegram answered 5xx: it may have accepted the message before
			// failing, and nothing can ask it afterwards.
			"an upstream outage is an outcome we never learn",
			502, `{"ok":false,"description":"Bad Gateway"}`,
			connector.ErrSendOutcomeUnknown, false,
		},
		{
			// The customer blocked the bot. Definite, so nothing was transmitted —
			// but permanent, and the credential is fine. It takes its own class so
			// the delivery parks naming the block instead of telling an operator to
			// rotate a token that works.
			"a blocked bot is the recipient's doing, not the credential's",
			403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`,
			connector.ErrRecipientUnreachable, true,
		},
		{
			// Understood and refused on Telegram's own terms: nothing went, so
			// the ladder may try again.
			"a refusal on Telegram's own terms stays retryable",
			400, `{"ok":false,"description":"Bad Request: chat not found"}`,
			ErrRequestRejected, true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := serve(t, tc.status, tc.body)

			_, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), reply())
			if !errors.Is(err, tc.want) {
				t.Fatalf("SendMessage = %v, want %v", err, tc.want)
			}
			if tc.notUnknown && errors.Is(err, connector.ErrSendOutcomeUnknown) {
				t.Errorf("%v also reads as an unknown outcome; the delivery would be abandoned rather than retried", err)
			}
		})
	}
}

// The two permanent classes must stay distinguishable at this seam, because the
// dispatcher turns each into a different instruction and only one of them is ever
// right: reconnect the channel, or stop trying to reach this person here.
func TestABlockedRecipientIsNeverReportedAsACredentialFault(t *testing.T) {
	api, _ := serve(t, 403,
		`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)

	_, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), reply())
	if !errors.Is(err, connector.ErrRecipientUnreachable) {
		t.Fatalf("SendMessage on a blocked bot = %v, want connector.ErrRecipientUnreachable", err)
	}
	if errors.Is(err, connector.ErrAuthRejected) {
		t.Fatal("a blocked bot also reads as a rejected credential; the operator would be told to rotate a token that works")
	}
}

// A recipient or an anchor that cannot address a chat is refused BEFORE the
// network call. Routing to a guessed chat would deliver a customer's reply to
// whoever that id belongs to, and dropping a malformed anchor would detach the
// reply from the conversation it answers while still reporting success.
func TestSendMessageRefusesAnUnaddressableMessageWithoutCallingTheProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*connector.ChannelMessage)
	}{
		{"a non-numeric recipient", func(m *connector.ChannelMessage) { m.Recipient.ChannelUserID = "@buyer" }},
		{"a non-numeric reply anchor", func(m *connector.ChannelMessage) { m.ReplyTo = "root" }},
		{"no recipient at all", func(m *connector.ChannelMessage) { m.Recipient.ChannelUserID = "" }},
		{"no idempotency key", func(m *connector.ChannelMessage) { m.IdempotencyKey = "" }},
		// A negative chat id is Telegram's spelling of a supergroup or channel —
		// the non-private chat this connector never captures from and must never
		// answer into. Parsed as a chat, it would publish the rep's reply, and
		// the customer's words quoted in it, to a room whoever owns that id
		// controls.
		{"a supergroup chat id", func(m *connector.ChannelMessage) { m.Recipient.ChannelUserID = "-1001234567890" }},
		// Chat 0 addresses no account: it is what a staged row carries when the
		// identity behind it was never resolved.
		{"a recipient chat id of zero", func(m *connector.ChannelMessage) { m.Recipient.ChannelUserID = "0" }},
		// "0" parses cleanly and means "no anchor" to the Bot API, so a reply
		// carrying it would be sent detached from the conversation it answers
		// while reporting success — the same silent loss a dropped anchor is.
		{"a reply anchor of zero", func(m *connector.ChannelMessage) { m.ReplyTo = "0" }},
		{"a negative reply anchor", func(m *connector.ChannelMessage) { m.ReplyTo = "-5" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)
			msg := reply()
			tc.mutate(&msg)

			if _, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), msg); err == nil {
				t.Fatal("the message was accepted; a message that cannot address a chat must be refused")
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.paths) != 0 {
				t.Fatalf("the provider was called %d time(s) for a message that could not be addressed", len(rec.paths))
			}
		})
	}
}

// Carriage is what the shared gate measures a staged message against and what
// the channel directory publishes to the composer, so a wrong number here is
// either a message refused for no reason or one that parks at transmission
// after the human was told it would go.
//
// Every value is measured against a live bot: the album is atomic on validation
// (so 10, not 1), the per-file cap is deliberately the INBOUND getFile cap
// rather than the higher send limit, and the caption bound is exact — 1024
// accepted, 1025 refused.
func TestTelegramDeclaresTheCarriageItWasMeasuredAt(t *testing.T) {
	want := connector.Carriage{
		Carries:          true,
		MaxBytesPerFile:  20 << 20,
		MaxFiles:         10,
		MaxBodyWithFiles: 1024,
	}
	if got := New(nil).Carriage(); got != want {
		t.Errorf("Carriage() = %+v, want %+v", got, want)
	}
}

// The message a rep sends with documents attached, end to end through the
// connector: the album reaches the provider whole and the receipt carries the id
// a reply threads under.
func TestSendMessageTransmitsTheFilesItWasStagedWith(t *testing.T) {
	api, rec := serve(t, 200, `{"ok":true,"result":[{"message_id":9911},{"message_id":9912}]}`)
	msg := reply()
	msg.Files = []connector.OutboundFile{
		{AttachmentID: "a-1", Filename: "quote.pdf", ContentType: "application/pdf", Body: []byte("quote bytes")},
		{AttachmentID: "a-2", Filename: "terms.pdf", ContentType: "application/pdf", Body: []byte("terms bytes")},
	}

	receipt, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), msg)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if receipt.ProviderMessageID != "9911" {
		t.Errorf("provider message id = %q, want the album anchor \"9911\"", receipt.ProviderMessageID)
	}
	if got := rec.lastPath(t); !strings.HasSuffix(got, "/"+methodSendMediaGroup) {
		t.Errorf("the message went to %q, want the %s method", got, methodSendMediaGroup)
	}
	_, files := formOf(t, rec)
	if len(files) != 2 {
		t.Fatalf("%d file parts reached the provider, want both", len(files))
	}
	for i, want := range []string{"quote bytes", "terms bytes"} {
		if files[i].body != want {
			t.Errorf("file part %d carries %q, want %q", i, files[i].body, want)
		}
	}
}

// The regression that matters now the outbound path branches: a message with
// nothing attached must still take the text method, not an empty album.
func TestSendMessageStillTakesTheTextMethodWithNoFiles(t *testing.T) {
	api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)

	if _, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), reply()); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := rec.lastPath(t); !strings.HasSuffix(got, "/sendMessage") {
		t.Errorf("a message with no files went to %q, want sendMessage", got)
	}
}

// A message this connector declared it cannot carry is refused, and nothing
// reaches the provider.
//
// The shared carriage gate stops this earlier by reading Carriage() — and this
// is the case that gate cannot cover, because it measures against what the
// connector CLAIMS. The connector is the last place that can refuse a claim its
// own send path cannot honour, which is why the bounds are checked here too
// rather than trusted from above.
//
// The class matters as much as the refusal: ErrFilesNotCarried is what the
// dispatcher parks on, and none of these refusals can come out differently on a
// retry — left retryable they would spend the whole ladder re-reading the files
// and then park under a reason naming no cause.
func TestSendMessageRefusesWhatItDeclaredItCannotCarry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(m *connector.ChannelMessage)
	}{
		{"more files than the album takes", func(m *connector.ChannelMessage) {
			for range New(nil).Carriage().MaxFiles + 1 {
				m.Files = append(m.Files, connector.OutboundFile{
					AttachmentID: "a-1", Filename: "quote.pdf", ContentType: "application/pdf", Body: []byte("bytes"),
				})
			}
		}},
		{"a body longer than a caption holds", func(m *connector.ChannelMessage) {
			m.Body = strings.Repeat("x", New(nil).Carriage().MaxBodyWithFiles+1)
			m.Files = []connector.OutboundFile{
				{AttachmentID: "a-1", Filename: "quote.pdf", ContentType: "application/pdf", Body: []byte("bytes")},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)
			msg := reply()
			tc.build(&msg)

			if _, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), msg); !errors.Is(err, connector.ErrFilesNotCarried) {
				t.Fatalf("SendMessage → %v, want ErrFilesNotCarried", err)
			}
			if rec.calls() != 0 {
				t.Errorf("the connector called the provider %d time(s) for a message it cannot carry whole", rec.calls())
			}
		})
	}
}

// The recipient and anchor guards run on the FILE path too. A second route
// through this seam that skipped them would send the rep's words — and the
// documents — to a guessed chat, or detached from the conversation they answer,
// for exactly the messages that carry the most.
func TestSendMessageWithFilesKeepsTheGuardsTheTextPathHas(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(m *connector.ChannelMessage)
	}{
		{"a chat id that is not a private chat", func(m *connector.ChannelMessage) {
			m.Recipient.ChannelUserID = "-1001234567890"
		}},
		{"an anchor that is not a provider message id", func(m *connector.ChannelMessage) {
			m.ReplyTo = "0"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, rec := serve(t, 200, `{"ok":true,"result":{"message_id":9911}}`)
			msg := reply()
			msg.Files = []connector.OutboundFile{
				{AttachmentID: "a-1", Filename: "quote.pdf", ContentType: "application/pdf", Body: []byte("bytes")},
			}
			tc.spoil(&msg)

			if _, err := New(api).SendMessage(context.Background(), connector.Auth("1:secret"), msg); !errors.Is(err, ErrRequestRejected) {
				t.Fatalf("SendMessage → %v, want ErrRequestRejected", err)
			}
			if rec.calls() != 0 {
				t.Errorf("the provider was called %d time(s) for a message that could not be addressed", rec.calls())
			}
		})
	}
}
