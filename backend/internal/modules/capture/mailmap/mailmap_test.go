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

func TestParseCarriesListUnsubscribeOntoCounterparty(t *testing.T) {
	// A bulk-mail List-Unsubscribe header from a HUMAN localpart is NOT
	// skipped (SkipReason keeps it — it may be a real contact's newsletter),
	// but it surfaces as the transactional-gate corroboration signal.
	withHeader := crlf(
		"From: hello@event.gitex.com",
		"To: me@myco.com",
		"Subject: Join us at GITEX",
		"List-Unsubscribe: <https://gitex.com/unsub?x=1>",
		"Message-ID: <blast1@event.gitex.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Register now.",
		"",
	)
	msg, err := Parse(withHeader, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, drop := msg.SkipReason(); drop {
		t.Fatalf("a human-localpart bulk mail must not be skipped outright")
	}
	if !msg.ToRecord("imap", withHeader).Counterparty.ListUnsubscribe {
		t.Fatalf("Counterparty.ListUnsubscribe = false, want true when the header is present")
	}

	without := crlf(
		"From: jane@acme.com", "To: me@myco.com", "Subject: hi",
		"Message-ID: <m1@acme.com>", "Content-Type: text/plain", "", "body", "",
	)
	msg, err = Parse(without, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.ToRecord("imap", without).Counterparty.ListUnsubscribe {
		t.Fatalf("Counterparty.ListUnsubscribe = true, want false with no header")
	}
}

func TestSkipReasonDropsDeliverySystemMail(t *testing.T) {
	cases := map[string][]byte{
		"delivery-system sender": crlf(
			"From: mailer-daemon@newsletter.com", "To: me@myco.com", "Subject: Weekly digest",
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

// The T1 correspondence gate's evidence is the provider's, not the message's
// (ADR-0072 §1). Parse must never infer it: a message whose From header names
// the mailbox owner parses as outbound — that is what direction means — and it
// still attests nothing, because anyone can write that header. Only a
// connector holding an authenticated provider handle may set the attestation.
func TestSentByOwnerComesFromTheProviderNotTheHeaders(t *testing.T) {
	spoofed := crlf(
		"From: me@myco.com", "To: victim@evil.example", "Subject: pay this invoice",
		"Message-ID: <spoof1@evil.example>", "Content-Type: text/plain", "", "wire it", "",
	)
	msg, err := Parse(spoofed, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.ToRecord("imap", spoofed)
	if rec.Counterparty.Direction != "outbound" {
		t.Fatalf("Direction = %q, want outbound — the From header claims the owner", rec.Counterparty.Direction)
	}
	if rec.Counterparty.SentByOwner() {
		t.Fatal("SentByOwner = true from headers alone: a forged From:owner would whitelist its recipient past suppression")
	}
	if attested := msg.AttestSentByOwner(true).ToRecord("imap", spoofed); !attested.Counterparty.SentByOwner() {
		t.Fatal("AttestSentByOwner(true) must carry the provider's attestation onto the record")
	}
}

// Placement is not authorship. A server-side rule can file a third party's
// message into a \Sent mailbox or Sent Items, and the counterparty of such a
// message is its SENDER — attesting on the provider's filing alone would stamp
// a stranger's address as the workspace's own correspondence and hand them the
// T1 spare past T2 suppression, which is the same bypass the forged header
// would have bought.
func TestAttestationRequiresAuthorshipNotJustPlacement(t *testing.T) {
	filed := crlf(
		"From: blast@sendgrid.net", "To: me@myco.com", "Subject: deal",
		"Message-ID: <filed1@sendgrid.net>", "Content-Type: text/plain", "", "hi", "",
	)
	msg, err := Parse(filed, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.AttestSentByOwner(true).ToRecord("imap", filed)
	if rec.Counterparty.Email != "blast@sendgrid.net" {
		t.Fatalf("Counterparty = %q, want the sender — this message is inbound", rec.Counterparty.Email)
	}
	if rec.Counterparty.SentByOwner() {
		t.Fatal("a third party's message filed into the sent container attested the owner's authorship")
	}
}

// ADR-0072 §1 promises that a transactional sender's message keeps its place on
// the timeline while the tier gate suppresses the person and company it would
// otherwise derive. A no-reply localpart is the ordinary shape of exactly that
// mail — a signed envelope, an invoice, a shipping notice — so dropping it here
// would make the promise false before the gate ever ran, and would starve the
// T2 corroboration rule of the machine localparts it exists to recognize.
func TestANoReplyVendorMessageReachesTheTierGate(t *testing.T) {
	envelope := crlf(
		"From: no-reply@eu.docusign.net", "To: me@myco.com", "Subject: Completed: Order form",
		"Message-ID: <ds1@eu.docusign.net>", "Content-Type: text/plain", "", "signed", "",
	)
	msg, err := Parse(envelope, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if reason, drop := msg.SkipReason(); drop {
		t.Fatalf("a no-reply vendor envelope was dropped as %q — the tier gate never sees it", reason)
	}

	// The transport system's own mail is different: there is no correspondent
	// behind a bounce, so nothing downstream could act on it.
	bounce := crlf(
		"From: mailer-daemon@eu.docusign.net", "To: me@myco.com", "Subject: Undeliverable",
		"Message-ID: <b1@eu.docusign.net>", "Content-Type: text/plain", "", "failed", "",
	)
	msg, err = Parse(bounce, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, drop := msg.SkipReason(); !drop {
		t.Fatal("a bounce reached the tier gate — there is no counterparty behind the transport system")
	}
}

// The second door into the same contradiction. Narrowing the machine-sender
// filter is not enough on its own: a newsletter carries `Precedence: bulk` and
// a signed envelope carries `Auto-Submitted: auto-generated`, so dropping those
// would keep the tier gate from ever seeing the mail it is built to judge —
// and `Precedence: bulk` is itself T2 corroboration.
//
// An auto-REPLY stays dropped, and not only for noise: an autoresponder
// answering a stranger is genuine owner-authored mail, the one shape that could
// buy a T1 correspondence spare for an address nobody chose to write to.
func TestOnlyAutoRepliesAreDroppedBeforeTheTierGate(t *testing.T) {
	reaches := map[string][]string{
		"bulk newsletter":     {"Precedence: bulk"},
		"list mail":           {"Precedence: list"},
		"auto-generated note": {"Auto-Submitted: auto-generated"},
		// RFC 3834 §5 lets the value carry parameters; the keyword still decides.
		"parameterized auto-generated": {"Auto-Submitted: auto-generated; owner-email=ops@vendor.example"},
	}
	for name, headers := range reaches {
		t.Run(name, func(t *testing.T) {
			lines := append([]string{"From: hello@vendor.example", "To: me@myco.com", "Subject: s"}, headers...)
			lines = append(lines, "Message-ID: <r1@vendor.example>", "Content-Type: text/plain", "", "body", "")
			msg, err := Parse(crlf(lines...), "me@myco.com")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if reason, drop := msg.SkipReason(); drop {
				t.Fatalf("dropped as %q — the tier gate never sees it", reason)
			}
		})
	}

	dropped := map[string][]string{
		"vacation responder": {"Auto-Submitted: auto-replied"},
		"junk precedence":    {"Precedence: junk"},
		// The same parameters on the reply side must not smuggle it past —
		// matching the whole value instead of the keyword is how that happens.
		"parameterized auto-reply": {"Auto-Submitted: auto-replied; owner-email=ops@vendor.example"},
		// An extension token nobody has defined yet is still an automatic
		// message; unknown resolves toward the reading that cannot buy a spare.
		"unknown extension token": {"Auto-Submitted: auto-forwarded"},
	}
	for name, headers := range dropped {
		t.Run(name, func(t *testing.T) {
			lines := append([]string{"From: hello@vendor.example", "To: me@myco.com", "Subject: s"}, headers...)
			lines = append(lines, "Message-ID: <d1@vendor.example>", "Content-Type: text/plain", "", "body", "")
			msg, err := Parse(crlf(lines...), "me@myco.com")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if _, drop := msg.SkipReason(); !drop {
				t.Fatal("an auto-reply reached the tier gate — nobody chose to write it")
			}
		})
	}
}
