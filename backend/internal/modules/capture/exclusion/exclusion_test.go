// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package exclusion_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture/exclusion"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestMatch_senderDomain(t *testing.T) {
	attrs := connector.ExclusionAttrs{SenderDomain: "personal-family.example"}
	rule, ok := exclusion.Match(attrs, []exclusion.Rule{{Kind: "sender_domain", Value: "personal-family.example"}})
	if !ok {
		t.Fatal("sender-domain rule should match its own domain")
	}
	if rule.Kind != "sender_domain" {
		t.Errorf("matched rule kind = %q, want sender_domain", rule.Kind)
	}
}

func TestMatch_recipientDomainAnyRecipient(t *testing.T) {
	attrs := connector.ExclusionAttrs{RecipientDomains: []string{"work.example", "personal-family.example"}}
	if _, ok := exclusion.Match(attrs, []exclusion.Rule{{Kind: "recipient_domain", Value: "personal-family.example"}}); !ok {
		t.Error("a recipient-domain rule should match when ANY recipient is in that domain")
	}
}

func TestMatch_label(t *testing.T) {
	attrs := connector.ExclusionAttrs{Labels: []string{"Personal", "Family"}}
	if _, ok := exclusion.Match(attrs, []exclusion.Rule{{Kind: "label", Value: "Personal"}}); !ok {
		t.Error("a label rule should match a present label")
	}
}

func TestMatch_caseInsensitive(t *testing.T) {
	attrs := connector.ExclusionAttrs{SenderDomain: "Personal-Family.Example"}
	if _, ok := exclusion.Match(attrs, []exclusion.Rule{{Kind: "sender_domain", Value: "personal-family.example"}}); !ok {
		t.Error("domain comparison must be case-insensitive")
	}
}

func TestMatch_noMatch(t *testing.T) {
	attrs := connector.ExclusionAttrs{SenderDomain: "work.example", RecipientDomains: []string{"client.example"}}
	if rule, ok := exclusion.Match(attrs, []exclusion.Rule{
		{Kind: "sender_domain", Value: "personal-family.example"},
		{Kind: "recipient_domain", Value: "other.example"},
		{Kind: "label", Value: "Personal"},
	}); ok {
		t.Errorf("no rule should match; got %+v", rule)
	}
}

func TestMatch_emptyAttrsNeverMatch(t *testing.T) {
	// A record with no mail attributes (a lead, a non-mail activity) must
	// pass the gate untouched, even against a full rule set.
	if _, ok := exclusion.Match(connector.ExclusionAttrs{}, []exclusion.Rule{
		{Kind: "sender_domain", Value: ""},
		{Kind: "recipient_domain", Value: "x.example"},
	}); ok {
		t.Error("empty attributes must never match a rule")
	}
}

func TestMatch_emptyRuleSet(t *testing.T) {
	attrs := connector.ExclusionAttrs{SenderDomain: "personal-family.example"}
	if _, ok := exclusion.Match(attrs, nil); ok {
		t.Error("an empty rule set matches nothing")
	}
}

func TestMatch_wrongKindDoesNotCross(t *testing.T) {
	// A sender-domain value must not be matched by a recipient-domain rule
	// (or vice versa): the kinds are distinct axes, never conflated.
	attrs := connector.ExclusionAttrs{SenderDomain: "personal-family.example"}
	if _, ok := exclusion.Match(attrs, []exclusion.Rule{{Kind: "recipient_domain", Value: "personal-family.example"}}); ok {
		t.Error("a recipient_domain rule must not match on the sender domain")
	}
}
