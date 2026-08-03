// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The parts of the one send path that need no database: which stored
// identifiers may thread a message, the minted message identity, and the To/Cc
// split. Everything the path does once it reaches Postgres — including every
// guard, which now answers only after the anchor has been read — is proven in
// email_integration_test.go and email_refusals_integration_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubUnsubscribeLinker stands in for the consent module's preference-token
// mint: ok=false is how a locked (transactional) purpose answers.
type stubUnsubscribeLinker struct {
	token string
	ok    bool
	err   error
}

func (l stubUnsubscribeLinker) UnsubscribeToken(context.Context, string, string) (string, bool, error) {
	return l.token, l.ok, l.err
}

// stubSendAuthority stands in for the connection registry's pre-flight answer,
// and remembers WHICH provider it was asked about: the two transports ask about
// different credentials, and an authority answering for the wrong one is the
// defect that refuses every channel reply.
type stubSendAuthority struct {
	capable bool
	err     error
	asked   []string
}

func (m *stubSendAuthority) SendCapable(_ context.Context, provider string) (bool, error) {
	m.asked = append(m.asked, provider)
	return m.capable, m.err
}

// draftOutcomeCall is what the send path handed the learning loop: the
// reference it carried, and the body it asks to be judged.
type draftOutcomeCall struct{ draftRef, finalBody string }

// recordingDraftOutcome stands in for the ai module's voice-signal writer. It
// answers either of the two ways the seam allows — recorded/not-recorded, or a
// fault — and remembers WHETHER it was asked, because "never consulted" is the
// only proof that a send with no draft reference costs no query.
type recordingDraftOutcome struct {
	recorded bool
	err      error
	calls    []draftOutcomeCall
}

func (r *recordingDraftOutcome) RecordSendOutcomeTx(_ context.Context, _ pgx.Tx, draftRef, finalBody string) (bool, error) {
	r.calls = append(r.calls, draftOutcomeCall{draftRef: draftRef, finalBody: finalBody})
	return r.recorded, r.err
}

// Threading headers are derived from an anchor's stored identifiers, and only a
// MAIL activity's are RFC822 message identities. A calendar event's iCalUID is
// spelled like one and would pass a shape test alone; an imported email's
// source_id is opaque and would pass a kind test alone. Emitting either as
// In-Reply-To produces a header no mail client can resolve.
func TestOnlyAMailActivitysWellFormedIdentityThreadsAMessage(t *testing.T) {
	for _, tc := range []struct {
		name, kind, value string
		want              string
	}{
		{"a captured mail identity", "email", "parent@buyer.test", "parent@buyer.test"},
		{"a calendar uid that merely looks like one", "meeting", "abc123@google.com", ""},
		{"an opaque identifier on a mail activity", "email", "crm-import-8842", ""},
		{"an already-bracketed identity", "email", "<parent@buyer.test>", ""},
		{"an identity with two at-signs", "email", "a@b@buyer.test", ""},
		{"an empty local part", "email", "@buyer.test", ""},
		{"no identity at all", "email", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageIdentity(tc.kind, tc.value); got != tc.want {
				t.Errorf("messageIdentity(%q, %q) = %q, want %q", tc.kind, tc.value, got, tc.want)
			}
		})
	}
}

// The minted identity is the natural key capture derives from the provider's
// own copy of this message: unbracketed, or the sent copy lands on the
// timeline a second time. It is fresh per message — it is also the provider's
// retransmission-idempotency key.
func TestMintMessageIDIsUnbracketedAndFreshPerMessage(t *testing.T) {
	first := MintMessageID("margince.test")
	second := MintMessageID("margince.test")

	for _, id := range []string{first, second} {
		if strings.ContainsAny(id, "<>") {
			t.Fatalf("minted message id %q carries angle brackets; capture stores them stripped", id)
		}
		if !strings.HasSuffix(id, "@margince.test") {
			t.Fatalf("minted message id %q does not end in the sending domain", id)
		}
		if strings.TrimSuffix(id, "@margince.test") == "" {
			t.Fatalf("minted message id %q has an empty local part", id)
		}
	}
	if first == second {
		t.Fatalf("two mints returned the same identity %q; it is also the retransmission key", first)
	}
}

// Recipients is the MERGED consent list (to + cc) by design, so the delivery's
// To: is what remains once the Cc: addresses come out — rendering the merged
// list as To: would copy every cc'd person twice and expose the cc list as
// primary recipients.
func TestDeliveryToRecipientsExcludeTheCcAddresses(t *testing.T) {
	to := toRecipients(
		[]string{"buyer@example.test", "boss@example.test", "Watcher@Example.test"},
		[]string{"boss@example.test", "watcher@example.test "},
	)
	if len(to) != 1 || to[0] != "buyer@example.test" {
		t.Fatalf("To: = %v, want only the non-cc'd recipient (case and padding are not a different address)", to)
	}
}

// The send path's configuration is spread across several With… options, each
// returning a COPY of the store. They have to accumulate on one store or the
// last option silently drops the earlier ones — and a store that kept the base
// URL but lost the token linker looks configured while deriving nothing.
func TestSendPathOptionsAccumulateOnOneStore(t *testing.T) {
	handlers := NewHandlers(nil).
		WithUnsubscribe(stubUnsubscribeLinker{token: "tok", ok: true}).
		WithPublicBaseURL(" https://mail.example.test/ ").
		WithSendAuthority(&stubSendAuthority{capable: true}).
		WithDraftOutcome(&recordingDraftOutcome{})

	if handlers.store.unsubscribe == nil {
		t.Fatal("the unsubscribe linker did not survive the later options")
	}
	if handlers.store.sendAuthority == nil {
		t.Fatal("the send pre-flight did not survive the option chain")
	}
	// The handler option is the half the MCP transport does NOT use, so it is
	// also the half nothing else would notice missing: a store that reached the
	// send path without it closes no learning signal for HTTP sends.
	if handlers.store.draftOutcome == nil {
		t.Fatal("the draft-outcome recorder did not reach the handlers' own store")
	}
	// Trimmed of whitespace and of the trailing slash, so the links built from
	// it never carry a doubled separator.
	if handlers.store.publicBaseURL != "https://mail.example.test" {
		t.Fatalf("public base URL = %q, want it normalized onto the same store", handlers.store.publicBaseURL)
	}
}
