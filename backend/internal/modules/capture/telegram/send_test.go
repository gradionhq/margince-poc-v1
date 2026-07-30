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
