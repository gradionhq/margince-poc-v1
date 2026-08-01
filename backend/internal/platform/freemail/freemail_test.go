// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package freemail

import "testing"

func TestRegistrableNarrowsToTheEffectiveTLDPlusOne(t *testing.T) {
	cases := map[string]string{
		"eu.docusign.net":   "docusign.net",
		"news.acme.co.uk":   "acme.co.uk",
		"ACME.COM":          "acme.com",
		"  acme.com.  ":     "acme.com",
		"müll.email":        "xn--mll-hoa.email",
		"xn--mll-hoa.email": "xn--mll-hoa.email",
		"localhost":         "localhost", // no known suffix: honest passthrough
		"co.uk":             "co.uk",     // a bare suffix cannot be narrowed
		"":                  "",
	}
	for in, want := range cases {
		if got := Registrable(in); got != want {
			t.Errorf("Registrable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsConsumerCoversTheDefectsAHandPinnedListLeft(t *testing.T) {
	m := New(nil, nil)

	// The whole reason the dataset replaced the hand-pinned map: every one of
	// these produced a fake company from a consumer mailbox.
	consumer := []string{
		"live.fr", "hotmail.co.uk", "hotmail.fr", "yahoo.co.jp", "laposte.net",
		"gmail.com", "web.de", "gmx.de", "t-online.de", "proton.me",
		// Carried by pinnedBaseline because the dataset lacks them.
		"fastmail.com", "posteo.de", "tutanota.com", "duck.com", "gmx.ch",
		"ziggo.nl", "tuta.io", "0-mail.com",
	}
	for _, domain := range consumer {
		if !m.IsConsumer(domain) {
			t.Errorf("IsConsumer(%q) = false, want true", domain)
		}
	}

	// A company domain must never be one, least of all the personal domains
	// that started this: they are a person's own domain, not a mailbox vendor.
	company := []string{
		"herpertz.net", "richardnguyen.me", "acme.example", "gradion.com",
		"gmail.com.example", // suffix trickery is not a match
		"",
	}
	for _, domain := range company {
		if m.IsConsumer(domain) {
			t.Errorf("IsConsumer(%q) = true, want false", domain)
		}
	}
}

func TestIsConsumerWalksSubdomainsDownToTheRegistrableDomain(t *testing.T) {
	m := New(nil, nil)

	// A subdomain of a listed provider is the same mailbox service.
	for _, domain := range []string{"mail.gmx.net", "a.b.yahoo.com", "MAIL.GMAIL.COM"} {
		if !m.IsConsumer(domain) {
			t.Errorf("IsConsumer(%q) = false, want true", domain)
		}
	}
	// The walk must stop at the registrable domain. Reaching the public suffix
	// would make every .com address consumer mail.
	for _, domain := range []string{"com", "co.uk", "acme.co.uk"} {
		if m.IsConsumer(domain) {
			t.Errorf("IsConsumer(%q) = true, want false", domain)
		}
	}
}

func TestOverlayAddsAndCarvesOut(t *testing.T) {
	m := New([]string{" Corp-Mailbox.Example ", ""}, []string{"gmx.de"})

	if !m.IsConsumer("corp-mailbox.example") {
		t.Error("a configured extra must match, trimmed and case-folded")
	}
	if !m.IsConsumer("mail.corp-mailbox.example") {
		t.Error("a configured extra must cover its subdomains like the baseline does")
	}
	if m.IsConsumer("gmx.de") {
		t.Error("a carve-out must beat the baseline — it is the only way back in")
	}
	if m.IsConsumer("mail.gmx.de") {
		t.Error("a carve-out must cover the subdomains its baseline entry would have")
	}
	if !m.IsConsumer("gmx.net") {
		t.Error("carving out one domain must not disarm the rest of the baseline")
	}
	if New(nil, nil).IsConsumer("corp-mailbox.example") {
		t.Error("an extra must not leak into a matcher that was not given it")
	}
}

func TestBaselineSanitizesTheVendoredDataset(t *testing.T) {
	set := baseline()

	if len(set) < 8000 {
		t.Fatalf("baseline has %d domains, want the vendored dataset (~8.7k)", len(set))
	}
	// The four upstream defects data/README.md records.
	if _, present := set["atlanticbb.net"]; !present {
		t.Error("a trailing space must be trimmed, not carried into the key")
	}
	if _, present := set["housefancom"]; present {
		t.Error("an entry with no dot cannot be a mail domain and must be dropped")
	}
	if _, present := set["xn--mll-hoa.email"]; !present {
		t.Error("a Unicode entry must be folded to the punycode a mail header carries")
	}
	if _, present := set["müll.email"]; present {
		t.Error("the Unicode spelling must not survive beside its punycode form")
	}
	for _, domain := range pinnedBaseline {
		if _, present := set[domain]; !present {
			t.Errorf("pinned domain %q is missing from the baseline", domain)
		}
	}
}
