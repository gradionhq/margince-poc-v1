// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// crlf joins RFC822 lines with the wire's CRLF so the parser sees a
// well-formed message regardless of this file's own line endings.
func crlf(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n"))
}

func inboundFixture() []byte {
	return crlf(
		"From: Alice Example <alice@acme.com>",
		"To: me@myco.com",
		"Subject: Quote request",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <abc123@acme.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hi, please send pricing for 10 seats.",
		"",
	)
}

// ToRecord is parameterised by connector name — the same mapping serves both
// the imap and gmail connectors, stamping provenance with whichever read it.
func TestToRecordStampsConnectorName(t *testing.T) {
	msg, err := Parse(inboundFixture(), "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, name := range []string{"imap", "gmail"} {
		rec := msg.ToRecord(name, inboundFixture())
		if rec.EntityType != datasource.EntityActivity {
			t.Errorf("[%s] EntityType = %q, want activity", name, rec.EntityType)
		}
		if rec.NaturalKey.SourceSystem != name || rec.NaturalKey.SourceID != "abc123@acme.com" {
			t.Errorf("[%s] NaturalKey = %+v, want {%s, abc123@acme.com}", name, rec.NaturalKey, name)
		}
		if rec.Source != name+":abc123@acme.com" {
			t.Errorf("[%s] Source = %q, want %s:abc123@acme.com", name, rec.Source, name)
		}
		if rec.CapturedBy != "connector:"+name {
			t.Errorf("[%s] CapturedBy = %q, want connector:%s", name, rec.CapturedBy, name)
		}
		fields, ok := rec.Fields.(capture.ActivityFields)
		if !ok {
			t.Fatalf("[%s] Fields is %T, want capture.ActivityFields", name, rec.Fields)
		}
		if fields.Kind != "email" || fields.Subject != "Quote request" || fields.Direction != "inbound" {
			t.Errorf("[%s] fields = %+v, want email/Quote request/inbound", name, fields)
		}
		if !strings.Contains(fields.Body, "please send pricing") || !strings.Contains(fields.Body, "alice@acme.com") {
			t.Errorf("[%s] body should carry text + counterparty: %q", name, fields.Body)
		}
		if got := fields.OccurredAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-06-04T08:00:00Z" {
			t.Errorf("[%s] OccurredAt = %s, want 2026-06-04T08:00:00Z", name, got)
		}
	}
}

func TestParseClassifiesOutboundByOwner(t *testing.T) {
	raw := crlf(
		"From: me@myco.com",
		"To: Bob Buyer <bob@target.com>",
		"Subject: Re: Quote request",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <reply1@myco.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Here is your pricing.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.ToRecord("gmail", raw)
	fields := rec.Fields.(capture.ActivityFields)
	if fields.Direction != "outbound" {
		t.Errorf("Direction = %q, want outbound", fields.Direction)
	}
	if !strings.Contains(fields.Body, "bob@target.com") {
		t.Errorf("outbound counterparty (To) should be surfaced: %q", fields.Body)
	}
}

func TestSkipReasonDropsAutomatedMail(t *testing.T) {
	cases := map[string][]byte{
		"no-reply sender": crlf(
			"From: no-reply@newsletter.com", "To: me@myco.com", "Subject: Weekly digest",
			"Message-ID: <n1@newsletter.com>", "Content-Type: text/plain", "", "news", "",
		),
		"auto-submitted header": crlf(
			"From: system@acme.com", "To: me@myco.com", "Subject: Out of office",
			"Auto-Submitted: auto-replied", "Message-ID: <ooo1@acme.com>", "Content-Type: text/plain", "", "away", "",
		),
		"no message id": crlf(
			"From: someone@acme.com", "To: me@myco.com", "Subject: hi", "Content-Type: text/plain", "", "body", "",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			msg, err := Parse(raw, "me@myco.com")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if _, drop := msg.SkipReason(); !drop {
				t.Fatalf("want drop=true for %s", name)
			}
		})
	}
}

func TestParseFallsBackToHTMLWhenNoPlainPart(t *testing.T) {
	raw := crlf(
		"From: Carol <carol@acme.com>", "To: me@myco.com", "Subject: HTML only",
		"Message-ID: <html1@acme.com>", "Content-Type: text/html; charset=utf-8", "",
		"<p>Hello <b>there</b></p>", "",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	fields := msg.ToRecord("imap", raw).Fields.(capture.ActivityFields)
	if !strings.Contains(fields.Body, "Hello") || strings.Contains(fields.Body, "<p>") {
		t.Errorf("HTML should be tag-stripped into readable text: %q", fields.Body)
	}
}

// The counterparty and thread identity feed the auto-create pipeline
// (ADR-0063): who the mail was with, and which conversation it belongs to.
func TestParseCarriesCounterpartyAndThreadKey(t *testing.T) {
	t.Run("inbound counterparty is the sender, thread roots at its own id", func(t *testing.T) {
		msg, err := Parse(inboundFixture(), "me@myco.com")
		if err != nil {
			t.Fatal(err)
		}
		rec := msg.ToRecord("imap", inboundFixture())
		cp := rec.Counterparty
		if cp.Email != "alice@acme.com" || cp.DisplayName != "Alice Example" || cp.Domain != "acme.com" || cp.Direction != "inbound" {
			t.Fatalf("counterparty = %+v", cp)
		}
		if rec.ThreadKey != "abc123@acme.com" {
			t.Fatalf("a fresh message roots its own thread, got %q", rec.ThreadKey)
		}
	})

	t.Run("a reply joins the References root, never the subject", func(t *testing.T) {
		reply := crlf(
			"From: me@myco.com",
			"To: Alice Example <alice@acme.com>",
			"Subject: Re: Quote request",
			"Date: Wed, 04 Jun 2026 09:00:00 +0000",
			"Message-ID: <def456@myco.com>",
			"References: <abc123@acme.com> <mid1@acme.com>",
			"In-Reply-To: <mid1@acme.com>",
			"Content-Type: text/plain",
			"",
			"On it.",
			"",
		)
		msg, err := Parse(reply, "me@myco.com")
		if err != nil {
			t.Fatal(err)
		}
		rec := msg.ToRecord("imap", reply)
		if rec.ThreadKey != "abc123@acme.com" {
			t.Fatalf("thread key = %q, want the References ROOT", rec.ThreadKey)
		}
		if rec.Counterparty.Email != "alice@acme.com" || rec.Counterparty.Direction != "outbound" {
			t.Fatalf("outbound counterparty = %+v, want the recipient", rec.Counterparty)
		}
		if rec.Counterparty.DisplayName != "Alice Example" {
			t.Fatalf("display name = %q, want the recipient header name", rec.Counterparty.DisplayName)
		}
	})

	t.Run("In-Reply-To alone still joins the thread", func(t *testing.T) {
		reply := crlf(
			"From: alice@acme.com",
			"To: me@myco.com",
			"Subject: Re: Quote request",
			"Date: Wed, 04 Jun 2026 10:00:00 +0000",
			"Message-ID: <ghi789@acme.com>",
			"In-Reply-To: <abc123@acme.com>",
			"Content-Type: text/plain",
			"",
			"Any update?",
			"",
		)
		msg, err := Parse(reply, "me@myco.com")
		if err != nil {
			t.Fatal(err)
		}
		if msg.ThreadKey() != "abc123@acme.com" {
			t.Fatalf("thread key = %q, want the In-Reply-To id", msg.ThreadKey())
		}
	})
}
