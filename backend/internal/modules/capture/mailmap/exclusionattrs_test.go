// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import (
	"slices"
	"testing"
)

func TestToRecordPopulatesExclusionAttrs(t *testing.T) {
	msg, err := Parse(inboundFixture(), "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.ToRecord("gmail", inboundFixture())
	if rec.Match.SenderDomain != "acme.com" {
		t.Errorf("SenderDomain = %q, want acme.com", rec.Match.SenderDomain)
	}
	if !slices.Contains(rec.Match.RecipientDomains, "myco.com") {
		t.Errorf("RecipientDomains = %v, want to contain myco.com", rec.Match.RecipientDomains)
	}
}

func TestToRecordCollectsEveryRecipientDomain(t *testing.T) {
	raw := crlf(
		"From: me@myco.com",
		"To: Bob <bob@target.com>, Carol <carol@personal-family.example>",
		"Subject: Re: intro",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <multi1@myco.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.ToRecord("gmail", raw)
	// Outbound from the owner: the sender domain is the owner's own.
	if rec.Match.SenderDomain != "myco.com" {
		t.Errorf("SenderDomain = %q, want myco.com", rec.Match.SenderDomain)
	}
	for _, want := range []string{"target.com", "personal-family.example"} {
		if !slices.Contains(rec.Match.RecipientDomains, want) {
			t.Errorf("RecipientDomains = %v, want to contain %s", rec.Match.RecipientDomains, want)
		}
	}
}

func TestExclusionDomainsAreLowercased(t *testing.T) {
	raw := crlf(
		"From: Alice <alice@ACME.com>",
		"To: ME@MyCo.com",
		"Subject: hi",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <case1@acme.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body",
		"",
	)
	msg, err := Parse(raw, "different-owner@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rec := msg.ToRecord("gmail", raw)
	if rec.Match.SenderDomain != "acme.com" {
		t.Errorf("SenderDomain = %q, want acme.com (lowercased)", rec.Match.SenderDomain)
	}
	if !slices.Contains(rec.Match.RecipientDomains, "myco.com") {
		t.Errorf("RecipientDomains = %v, want lowercased myco.com", rec.Match.RecipientDomains)
	}
}
