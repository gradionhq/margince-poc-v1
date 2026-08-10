// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"slices"
	"testing"
)

// CAP-AC1.3c — a registered domain covers its subdomains, a lookalike sibling
// is not covered, and the two spellings of an internationalized domain are one
// value. The subdomain arm is the one with teeth: exact string equality leaks
// the internal mail of every company that sends from a subdomain, and it looks
// exactly like working correctly.
func TestARegisteredDomainCoversItsSubdomainsAndNothingElse(t *testing.T) {
	own := NewInternalDomains([]string{"acme.com", "münchen.de"})

	covered := []string{
		"rep@acme.com",
		"rep@mail.acme.com",
		"rep@a.deep.chain.acme.com",
		"REP@ACME.COM",
		"rep@münchen.de",
		"rep@xn--mnchen-3ya.de", // the same domain, punycode
		"rep@post.münchen.de",
	}
	for _, address := range covered {
		if !own.Covers(address) {
			t.Errorf("Covers(%q) = false, want true", address)
		}
	}

	// acme.com.evil.tld is the attack the separating dot exists to refuse: it
	// ENDS with a string containing acme.com, and it belongs to evil.tld.
	notCovered := []string{
		"rep@acme.com.evil.tld",
		"rep@notacme.com",
		"rep@acme.co",
		"rep@example.com",
		"no-at-sign",
		"trailing@",
		"",
	}
	for _, address := range notCovered {
		if own.Covers(address) {
			t.Errorf("Covers(%q) = true, want false", address)
		}
	}
}

// CAP-AC1.3 — every participant internal is the zero-rows condition; one
// external participant makes the whole message external. The second half is
// what keeps the introduction working: a colleague writing to a prospect with
// the prospect copied is correspondence, not chatter.
func TestAllInternalNeedsEveryPartyAndOneOutsiderIsEnoughToKeepTheMessage(t *testing.T) {
	own := NewInternalDomains([]string{"acme.com"})

	if !own.AllInternal([]string{"rep@acme.com", "boss@mail.acme.com"}) {
		t.Error("two colleagues, one on a subdomain: want all-internal")
	}
	if own.AllInternal([]string{"rep@acme.com", "client@customer.example"}) {
		t.Error("a copied prospect makes the message external — it must be kept")
	}
	if own.AllInternal([]string{"rep@acme.com", "unparseable"}) {
		t.Error("an address with no readable domain is not internal")
	}
}

// CAP-AC1.3d — an empty own-domain set makes nothing internal, so everything
// captures. An installation that has registered no domain is making no claim
// about its people's mail, and inventing one would be wrong in most workspaces.
func TestAnEmptyOwnDomainSetMakesNothingInternal(t *testing.T) {
	own := NewInternalDomains(nil)
	if !own.empty() {
		t.Fatal("a set built from nothing must report empty")
	}
	if own.AllInternal([]string{"rep@acme.com", "boss@acme.com"}) {
		t.Error("with no registered domain nothing is internal, so the message is kept")
	}
	if own.Covers("rep@acme.com") {
		t.Error("an empty set covers nobody")
	}
}

// A message that names nobody is not internal either. A connector reporting no
// addresses is saying it could not read the parties, which is different from
// saying there were none — and the direction to fail in is toward keeping mail.
func TestAMessageNamingNobodyIsNotInternal(t *testing.T) {
	own := NewInternalDomains([]string{"acme.com"})
	if own.AllInternal(nil) {
		t.Error("no addresses is not evidence of an internal message")
	}
	if own.AllInternal([]string{"", "   "}) {
		t.Error("blank addresses are not evidence of an internal message")
	}
}

// The external set is what a captured message may create records for, in header
// order and deduplicated. It is deliberately not the message's author.
func TestExternalNamesTheOutsidersInHeaderOrder(t *testing.T) {
	own := NewInternalDomains([]string{"acme.com"})
	got := own.External([]string{
		"rep@acme.com",
		"client@customer.example",
		"boss@mail.acme.com",
		"CLIENT@customer.example", // same party, different case
		"second@other.example",
	})
	want := []string{"client@customer.example", "second@other.example"}
	if !slices.Equal(got, want) {
		t.Errorf("External = %v, want %v", got, want)
	}
}

// Registering the same domain twice, in two spellings or two cases, is one
// domain. The set is compared in one normalized form so a workspace cannot
// half-register itself.
func TestTheDomainSetNormalizesAndDeduplicates(t *testing.T) {
	own := NewInternalDomains([]string{"ACME.com", "acme.com.", "münchen.de", "xn--mnchen-3ya.de", "", "  "})
	if !own.Covers("rep@acme.com") || !own.Covers("rep@münchen.de") {
		t.Fatal("normalized spellings must all resolve")
	}
	if len(own.domains) != 2 {
		t.Errorf("domains = %v, want the two distinct domains", own.domains)
	}
}

// An account label arrives in either shape a provider chooses. Taking the text
// after the last "@" of the display form yields "acme.com>", which matches no
// registered domain — so the gate would quietly never fire for that mailbox.
func TestAnAccountLabelYieldsItsDomainInEitherShape(t *testing.T) {
	for _, label := range []string{
		"rep@acme.com",
		"Rep <rep@acme.com>",
		"  Rep Example <rep@acme.com>  ",
		`"Rep, Example" <rep@acme.com>`,
	} {
		if got := domainOfAddress(bareAddress(label)); got != "acme.com" {
			t.Errorf("label %q gave domain %q, want acme.com", label, got)
		}
	}
	// An unparseable label is judged as an address, which is what it was before.
	if got := domainOfAddress(bareAddress("not an address")); got != "" {
		t.Errorf("unparseable label gave domain %q, want none", got)
	}
}

// A public suffix never enters the set, whichever writer supplied it.
//
// The three writers are the admin surface, the mailbox seed, and the company's
// own registered domains — and the last comes from a website field filled in at
// cold start, which the admin surface's validation never sees. A `co.uk` in the
// set would make every company beneath it internal and stop the workspace
// keeping their correspondence, with no way to recover what was skipped.
func TestAPublicSuffixIsNeverPartOfTheOwnDomainSet(t *testing.T) {
	own := NewInternalDomains([]string{"com", "co.uk", "com.br", "de", "acme.co.uk"})

	for _, address := range []string{
		"someone@bbc.co.uk",
		"someone@example.com",
		"someone@loja.com.br",
		"someone@beispiel.de",
	} {
		if own.Covers(address) {
			t.Errorf("Covers(%q) = true — a public suffix must never make a stranger internal", address)
		}
	}
	// The real registration beside them still counts.
	if !own.Covers("rep@acme.co.uk") {
		t.Error("a domain someone actually registers must still be covered")
	}
	if len(own.Domains()) != 1 {
		t.Errorf("set = %v, want only the ownable domain", own.Domains())
	}
}
