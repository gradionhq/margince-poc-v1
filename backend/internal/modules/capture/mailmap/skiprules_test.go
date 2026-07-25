// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

// The two rules of skiprules.go, proven separately because they answer
// different obligations: what never reaches the timeline, and what may never
// vouch for an address.

import "testing"

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
		// A parenthesis inside a quoted parameter opens no comment, and a
		// comment left unclosed out in the parameter region says nothing about
		// a keyword that already parsed. Reading either as malformed would
		// discard ordinary transactional mail.
		"paren inside a quoted parameter":      {`Auto-Submitted: auto-generated; owner-email="a(b@x.com"`},
		"unclosed comment after the semicolon": {"Auto-Submitted: auto-generated; owner-email=x@y (unclosed"},
		"semicolon inside a comment":           {"Auto-Submitted: auto-generated (a;b)"},
		// An unreadable Precedence must NOT drop the message: refusing it would
		// discard a newsletter the tier gate exists to judge. The veto reads the
		// same value as machine-touched, so nothing is granted by keeping it.
		"unclosed comment on a bulk marker": {"Precedence: bulk (unclosed"},
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
		// An unclosed comment swallows whatever followed, so the value is
		// unreadable rather than absent.
		"unclosed leading comment": {"Auto-Submitted: (swallows auto-replied"},
		// These two parse cleanly and name nothing. An empty keyword is not one
		// of the two values that mean a person wrote this, and nothing is not
		// permission.
		"empty comment only": {"Auto-Submitted: ()"},
		"present but empty":  {"Auto-Submitted:"},
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
		// RFC 3834 §5 parameters are attribute=value pairs. A bare token out
		// there is not one, so the value does not mean what its keyword claims
		// — and reading the keyword alone would let it vouch for an address.
		"bare token where a parameter belongs": {"Auto-Submitted: no;auto-replied"},
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
