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

// Two questions, deliberately answered by two different rules, because they
// pull opposite ways on the same headers. Keeping transactional mail on the
// timeline means the drop filter must be narrow; refusing to let an
// autoresponder vouch for a stranger means the attestation veto must be wide.
//
// This one is the drop: only a reply nobody chose to write stays off the
// timeline. Everything else — a newsletter, a signed envelope, any bulk-family
// marker — reaches the tier gate that decides what to do about it.
func TestOnlyAutoRepliesAreKeptOffTheTimeline(t *testing.T) {
	reaches := map[string][]string{
		"bulk newsletter":     {"Precedence: bulk"},
		"list mail":           {"Precedence: list"},
		"junk-marked bulk":    {"Precedence: junk"},
		"auto-generated note": {"Auto-Submitted: auto-generated"},
		// RFC 3834 §5 lets the value carry parameters; the keyword decides.
		"parameterized auto-generated": {"Auto-Submitted: auto-generated; owner-email=ops@vendor.example"},
		// RFC 5322 permits comments around it. With unknown values defaulting
		// to drop, a comment left in place would discard ordinary mail.
		"commented auto-generated": {"Auto-Submitted: auto-generated (invoice run)"},
		"leading comment on no":    {"Auto-Submitted: (sent by hand) no"},
		"nested comment":           {"Auto-Submitted: auto-generated (batch (nightly))"},
	}
	for name, headers := range reaches {
		t.Run(name, func(t *testing.T) {
			if reason, drop := parseWith(t, headers).SkipReason(); drop {
				t.Fatalf("%s was dropped as %q — the tier gate never sees it", name, reason)
			}
		})
	}

	dropped := map[string][]string{
		"vacation responder":       {"Auto-Submitted: auto-replied"},
		"parameterized auto-reply": {"Auto-Submitted: auto-replied; owner-email=ops@vendor.example"},
		"commented auto-reply":     {"Auto-Submitted: auto-replied (out of office)"},
		"precedence auto-reply":    {"Precedence: auto_reply"},
		// A keyword we do not recognize is still an automatic message, and the
		// safe reading of one is the reading that cannot buy a spare.
		"unrecognized keyword": {"Auto-Submitted: auto-forwarded"},
		// Present but yielding no keyword is unreadable, not absent — an
		// unclosed comment swallows the value.
		"unclosed leading comment": {"Auto-Submitted: (swallows auto-replied"},
		"empty comment only":       {"Auto-Submitted: ()"},
		"present but empty":        {"Auto-Submitted:"},
		// A relay that PREPENDS its own header must not mask the reply beneath.
		"reply under a prepended relay header": {"Auto-Submitted: auto-generated", "Auto-Submitted: auto-replied"},
	}
	for name, headers := range dropped {
		t.Run(name, func(t *testing.T) {
			if _, drop := parseWith(t, headers).SkipReason(); !drop {
				t.Fatalf("%s reached the tier gate — nobody chose to write it", name)
			}
		})
	}
}

// The other question: what may vouch for an address. A machine-touched message
// never attests however the provider filed it, because an autoresponder's reply
// is genuinely owner-authored and genuinely in Sent — nothing downstream could
// tell it from correspondence the owner chose, and it would spare an address
// the owner never chose to write to (ADR-0072 residual (b)).
func TestMachineTouchedMailNeverAttestsCorrespondence(t *testing.T) {
	vetoed := map[string][]string{
		"vacation responder":    {"Auto-Submitted: auto-replied"},
		"bulk newsletter":       {"Precedence: bulk"},
		"junk-marked bulk":      {"Precedence: junk"},
		"legacy autoreply":      {"X-Autoreply: yes"},
		"legacy autorespond":    {"X-Autorespond: yes"},
		"homegrown loop marker": {"X-Loop: owner@myco.com"},
		// The modern spelling of Precedence: list must land the same side.
		"list-id":          {"List-Id: <dev.example.com>"},
		"list-unsubscribe": {"List-Unsubscribe: <https://x/u>"},
		// An unclosed comment swallows whatever followed it, so the survivors
		// are not the value — even when what survives reads as "no".
		"not-automatic behind an unclosed comment": {"Auto-Submitted: no (swallows auto-replied"},
		"unclosed comment on precedence":           {"Precedence: bulk (unclosed"},
		// Legal RFC 3834 parameters on `no` must not read as machine-touched:
		// both rules read the keyword, so this stays ordinary owner mail.
		"auto-generated": {"Auto-Submitted: auto-generated"},
	}
	for name, headers := range vetoed {
		t.Run(name, func(t *testing.T) {
			rec := parseWith(t, headers).AttestSentByOwner(true).ToRecord("imap", []byte("x"))
			if rec.Counterparty.SentByOwner() {
				t.Fatalf("%s attested correspondence — a machine had a hand in it", name)
			}
		})
	}

	// The veto must not eat the evidence the T1 gate is built on. Both an
	// unadorned message and one carrying RFC 3834's own "not automatic" value,
	// with or without the parameters that value may legally carry, still vouch.
	for name, headers := range map[string][]string{
		"plain owner mail":          nil,
		"explicit not-automatic":    {"Auto-Submitted: no"},
		"not-automatic with params": {"Auto-Submitted: no; owner-email=ops@myco.com"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := parseWith(t, headers).AttestSentByOwner(true).ToRecord("imap", []byte("x"))
			if !rec.Counterparty.SentByOwner() {
				t.Fatalf("%s failed to attest — the gate is starved of real evidence", name)
			}
		})
	}
}

// parseWith builds an outbound message from the owner carrying headers.
func parseWith(t *testing.T, headers []string) Message {
	t.Helper()
	lines := append([]string{"From: me@myco.com", "To: them@vendor.example", "Subject: s"}, headers...)
	lines = append(lines, "Message-ID: <h1@myco.com>", "Content-Type: text/plain", "", "body", "")
	msg, err := Parse(crlf(lines...), "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return msg
}

// Bounces do not arrive with a tidy localpart. VERP encodes the original
// recipient into the address and BATV signs the return path, so the shapes a
// real mail system emits carry tags a plain equality check never sees.
func TestDeliverySystemSendersAreRecognizedInTheShapesTheyArriveIn(t *testing.T) {
	drops := []string{
		"MAILER-DAEMON@x.com", "m.a.i.l.e.r-daemon@x.com", "post-master@x.com",
		"mailer_daemon@x.com", // `_` too — the T2 registry strips it, so this must
		"bounces-12345@x.com", "bounce+tag@x.com",
		"prvs=1234abcd=owner@x.com", "msprvs1=abc=bounces@x.com",
	}
	for _, addr := range drops {
		if !isDeliverySystemSender(addr) {
			t.Errorf("%s reached the tier gate — there is no correspondent behind the transport system", addr)
		}
	}
	keeps := []string{"no-reply@x.com", "notifications@x.com", "alice@x.com", "bouncer@x.com", "postmaster.team@x.com"}
	for _, addr := range keeps {
		if isDeliverySystemSender(addr) {
			t.Errorf("%s was dropped as delivery-system mail — a real sender is behind it", addr)
		}
	}
}
