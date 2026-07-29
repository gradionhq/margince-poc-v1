// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func authFixture(t *testing.T, granted ...string) connector.Auth {
	t.Helper()
	raw, err := json.Marshal(authState{
		RefreshToken: "rt", Owner: "rep@acme.test",
		Scopes: []string{"read"}, Granted: granted,
	})
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	return connector.Auth(raw)
}

// sendCapture serves messages.send and records the raw MIME it received. The
// read-back that follows every send is answered with those same bytes, which is
// what a provider that honoured the client's identity returns.
func sendCapture(t *testing.T, raw *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			echo := map[string]any{"raw": *raw, "labelIds": []string{"SENT"}}
			if err := json.NewEncoder(w).Encode(echo); err != nil {
				t.Errorf("write read-back response: %v", err)
			}
			return
		}
		var body struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode send body: %v", err)
			return
		}
		*raw = body.Raw
		if _, err := w.Write([]byte(`{"id":"gmsg1","threadId":"gthread1"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
}

func decodeMIME(t *testing.T, raw string) string {
	t.Helper()
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("raw is not base64url: %v", err)
	}
	return string(b)
}

// The port carries the identity unbracketed; the HEADER must carry it
// bracketed, or the message is not valid RFC 5322.
func TestSendRendersTheMessageIDWithBracketsFromAnUnbracketedInput(t *testing.T) {
	var raw string
	srv := sendCapture(t, &raw)
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	got, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Cc: []string{"cc@example.com"},
		Subject: "Re: pricing", Body: "As discussed.",
		MessageID:           "abc@margince.test",
		InReplyTo:           "prior@example.com",
		References:          []string{"root@example.com", "prior@example.com"},
		ListUnsubscribe:     "<https://app.test/u/tok>",
		ListUnsubscribePost: "List-Unsubscribe=One-Click",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.ProviderMessageID != "gmsg1" {
		t.Errorf("receipt = %+v, want gmsg1", got)
	}
	mime := decodeMIME(t, raw)
	for _, want := range []string{
		"Message-ID: <abc@margince.test>",
		"In-Reply-To: <prior@example.com>",
		"References: <root@example.com> <prior@example.com>",
		"To: buyer@example.com",
		"Cc: cc@example.com",
		"Subject: Re: pricing",
		"List-Unsubscribe: <https://app.test/u/tok>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"As discussed.",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("MIME missing %q\n---\n%s", want, mime)
		}
	}
	// Double brackets would be silently accepted by many servers and would break
	// the capture-side key match, so assert the exact rendering.
	if strings.Contains(mime, "<<") {
		t.Errorf("double-bracketed identity in MIME:\n%s", mime)
	}
}

func TestSendOmitsUnsubscribeHeadersForATransactionalPurpose(t *testing.T) {
	var raw string
	srv := sendCapture(t, &raw)
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	if _, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "Invoice", Body: "Attached.",
		MessageID: "inv@margince.test",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if strings.Contains(decodeMIME(t, raw), "List-Unsubscribe") {
		t.Error("a transactional send must carry no List-Unsubscribe header")
	}
}

// A hostile subject must not be able to forge headers.
//
// The assertion is on "\r\nBcc:", NOT on "Bcc:". The literal text "Bcc:" is
// SUPPOSED to survive — harmlessly, inside the Subject value. What must not
// survive is a line break in front of it, because that is what turns text into
// a header. Asserting on the bare substring makes the test unpassable: a
// correctly sanitized subject still contains it.
func TestSendStripsCRLFFromHeaderValues(t *testing.T) {
	var raw string
	srv := sendCapture(t, &raw)
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	if _, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"b@example.com"}, Body: "hi", MessageID: "x@t",
		Subject: "ok\r\nBcc: attacker@evil.test",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mime := decodeMIME(t, raw)
	if strings.Contains(mime, "\r\nBcc:") {
		t.Errorf("CRLF in the subject forged a header:\n%s", mime)
	}
	// The header block must still end where it should: exactly one blank line
	// before the body, so nothing was injected past the boundary either.
	if strings.Count(mime, "\r\n\r\n") != 1 {
		t.Errorf("header/body boundary is not intact:\n%s", mime)
	}
}

// The retransmission guard: at-least-once job delivery must not mail twice.
func TestSendOnARetryReturnsThePriorReceiptWithoutTransmitting(t *testing.T) {
	sends := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/messages" && r.URL.Query().Get("q") == "rfc822msgid:abc@margince.test":
			if _, err := w.Write([]byte(`{"messages":[{"id":"gmsg1","threadId":"gthread1"}]}`)); err != nil {
				t.Errorf("write lookup: %v", err)
			}
		case r.URL.Path == "/messages/send":
			sends++
			if _, err := w.Write([]byte(`{"id":"dup","threadId":"dup"}`)); err != nil {
				t.Errorf("write send: %v", err)
			}
		default:
			t.Errorf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	got, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "Re: pricing", Body: "As discussed.",
		MessageID: "abc@margince.test", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sends != 0 {
		t.Errorf("transmitted %d times on a retry; the lookup must short-circuit", sends)
	}
	if got.ProviderMessageID != "gmsg1" {
		t.Errorf("receipt = %+v, want the prior receipt gmsg1", got)
	}
}

func TestSendOnTheFirstAttemptDoesNotLookUp(t *testing.T) {
	lookups := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" {
			lookups++
		}
		if _, err := w.Write([]byte(`{"id":"m","threadId":"t"}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	if _, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"b@example.com"}, Subject: "s", Body: "b",
		MessageID: "first@margince.test", Attempt: 0,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if lookups != 0 {
		t.Errorf("ran %d lookups on the first attempt; the guard is for retries only", lookups)
	}
}

func TestSendRefusesWhenTheGrantLacksTheSendScope(t *testing.T) {
	c := New(fakeOAuth{access: "access-token"}, NewAPI(nil, "http://unused.invalid"))
	_, err := c.Send(context.Background(),
		authFixture(t, "https://www.googleapis.com/auth/gmail.readonly"),
		connector.OutboundMessage{To: []string{"b@example.com"}, Subject: "s", Body: "b", MessageID: "x@t"})
	if !errors.Is(err, ErrSendScopeMissing) {
		t.Errorf("err = %v, want ErrSendScopeMissing", err)
	}
}

// The identity written at send must be the identity capture derives when it
// re-reads the same message. Anything else and every sent email lands twice.
//
// This feeds the RFC822 bytes the connector actually produced back through
// c.Normalize — the same mailmap.Parse + ToRecord composition the connector
// runs in production when it re-reads its own sent message off the SENT
// label (gmail.go's Normalize). That is a stronger proof than calling
// mailmap directly: it is the exact code path, not a re-implementation of it
// in the test.
func TestTheMessageIDSurvivesARoundTripThroughMailmap(t *testing.T) {
	var raw string
	srv := sendCapture(t, &raw)
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	const want = "abc@margince.test"
	if _, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "Re: pricing",
		Body: "As discussed.", MessageID: want,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Feed the bytes we actually produced through the same normalization the
	// Gmail connector runs on a captured message. The auth fixture's Owner
	// ("rep@acme.test") is the From address buildRFC822 stamped, so it must
	// match here too or Parse would classify the message as inbound.
	c.owner = "rep@acme.test"
	recs, err := c.Normalize(context.Background(), connector.RawRecord(decodeMIME(t, raw)))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := recs[0].NaturalKey.SourceID; got != want {
		t.Errorf("round-tripped SourceID = %q, want %q — the echo will not collapse", got, want)
	}
}

// The idempotency contract has a precondition: Send is required to be
// idempotent on the message identity, and the retransmission guard finds a
// prior send by searching for exactly that identity. A message carrying none —
// or one Gmail's rfc822msgid: operator cannot match — makes the guarantee
// unkeepable, so it must be refused before anything reaches Google rather than
// mailed once per retry.
func TestSendRefusesAMessageWithNoUsableIdentityBeforeAnyProviderCall(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"empty", ""},
		{"no domain", "abc"},
		{"already bracketed", "<abc@margince.test>"},
		{"embedded newline", "abc@margince\n.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer srv.Close()

			c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
			_, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
				To: []string{"buyer@example.com"}, Subject: "Re: pricing",
				Body: "As discussed.", MessageID: tc.id,
			})
			if !errors.Is(err, connector.ErrInvalidMessageID) {
				t.Fatalf("Send with identity %q → %v, want ErrInvalidMessageID", tc.id, err)
			}
			if calls != 0 {
				t.Errorf("%d provider calls for a message that cannot be retried safely", calls)
			}
		})
	}
}

// The consent gate matches a trimmed address, so the header must carry the
// trimmed one too: padding around an addr-spec is folding whitespace some
// clients render raw, and the address a recipient was asked about should be the
// address the message says it went to.
func TestSendTrimsTheAddressesItRenders(t *testing.T) {
	var raw string
	srv := sendCapture(t, &raw)
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	if _, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{" buyer@example.com ", ""}, Cc: []string{"\tcc@example.com"},
		Subject: "Re: pricing", Body: "As discussed.", MessageID: "abc@margince.test",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mime := decodeMIME(t, raw)
	if !strings.Contains(mime, "To: buyer@example.com\r\n") {
		t.Errorf("To header is not the trimmed address:\n%s", mime)
	}
	if !strings.Contains(mime, "Cc: cc@example.com\r\n") {
		t.Errorf("Cc header is not the trimmed address:\n%s", mime)
	}
}

// sendThenGet serves messages.send and the messages.get read-back. getRaw is
// the RFC822 the read-back returns; an empty getRaw makes the read-back 404,
// which is the graceful-degradation case.
func sendThenGet(t *testing.T, getRaw string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if _, err := w.Write([]byte(`{"id":"gmsg1"}`)); err != nil {
				t.Errorf("write send response: %v", err)
			}
			return
		}
		if getRaw == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := map[string]any{
			"raw":      base64.RawURLEncoding.EncodeToString([]byte(getRaw)),
			"labelIds": []string{"SENT"},
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write get response: %v", err)
		}
	}))
}

func sentMessage(messageID string) string {
	return "From: rep@acme.test\r\n" +
		"To: buyer@example.com\r\n" +
		"Subject: pricing\r\n" +
		"Message-ID: <" + messageID + ">\r\n" +
		"\r\n" +
		"As discussed.\r\n"
}

func sendOne(t *testing.T, srv *httptest.Server) (connector.SendReceipt, error) {
	t.Helper()
	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	return c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "pricing", Body: "As discussed.",
		MessageID: "minted@margince.test",
	})
}

// Gmail discards the client's Message-ID and mints its own. The receipt must
// report what the wire carries, or every sent mail is filed under an identity
// no reply will ever quote.
func TestSendReportsTheIdentityGmailStampedWhenItRewroteIt(t *testing.T) {
	srv := sendThenGet(t, sentMessage("CAFAR1tx@mail.gmail.com"))
	defer srv.Close()

	got, err := sendOne(t, srv)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.RFC822MessageID != "CAFAR1tx@mail.gmail.com" {
		t.Errorf("RFC822MessageID = %q, want Gmail's stamped identity", got.RFC822MessageID)
	}
}

// A provider that honoured the identity reports it unchanged, which the
// reconcile then recognises as a no-op.
func TestSendReportsTheMintedIdentityUnchangedWhenGmailHonouredIt(t *testing.T) {
	srv := sendThenGet(t, sentMessage("minted@margince.test"))
	defer srv.Close()

	got, err := sendOne(t, srv)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.RFC822MessageID != "minted@margince.test" {
		t.Errorf("RFC822MessageID = %q, want the honoured identity", got.RFC822MessageID)
	}
}

// THE RE-MAIL GUARD. The message has already left when the read-back runs, so
// a read-back failure must never surface as a Send error: an error hands the
// delivery back to a retry ladder whose prior-send lookup cannot see a
// rewritten identity, and the recipient is mailed twice. Degrading to an empty
// identity costs one duplicate timeline row and nothing else.
func TestSendSucceedsWithNoIdentityWhenTheReadBackFails(t *testing.T) {
	srv := sendThenGet(t, "")
	defer srv.Close()

	got, err := sendOne(t, srv)
	if err != nil {
		t.Fatalf("Send returned an error after the message was already transmitted: %v", err)
	}
	if got.ProviderMessageID != "gmsg1" {
		t.Errorf("ProviderMessageID = %q, want the receipt to survive a failed read-back", got.ProviderMessageID)
	}
	if got.RFC822MessageID != "" {
		t.Errorf("RFC822MessageID = %q, want empty when the read-back could not answer", got.RFC822MessageID)
	}
}

// Finding a prior send by rfc822msgid: proves the identity was honoured, so
// the retry path owes no read-back at all.
func TestSendOnRetryFindsThePriorSendAndReadsNothingBack(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/messages") && r.URL.Query().Get("q") != "" {
			if _, err := w.Write([]byte(`{"messages":[{"id":"gmsg1"}]}`)); err != nil {
				t.Errorf("write search response: %v", err)
			}
			return
		}
		gets++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(fakeOAuth{access: "access-token"}, NewAPI(srv.Client(), srv.URL))
	got, err := c.Send(context.Background(), authFixture(t, SendScope), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "pricing", Body: "As discussed.",
		MessageID: "minted@margince.test", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.RFC822MessageID != "" {
		t.Errorf("RFC822MessageID = %q, want empty: found by rfc822msgid means the identity was honoured", got.RFC822MessageID)
	}
	if gets != 0 {
		t.Errorf("read-back ran %d times on the retry path, want 0", gets)
	}
}
