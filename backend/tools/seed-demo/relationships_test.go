// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "testing"

func cfgWithOverrides(o map[string][]string) demoConfig {
	return demoConfig{RelTypes: demoRelTypes{Overrides: o}}
}

// TestRelationshipTypesDeriveFromLifecycle pins the rule that keeps a company
// ingested next month from landing with no type at all — the state all 187
// non-partner companies were in before this existed.
func TestRelationshipTypesDeriveFromLifecycle(t *testing.T) {
	cfg := cfgWithOverrides(nil)
	for _, tc := range []struct {
		lifecycle string
		want      string
	}{
		{"customer", "customer"},
		{"former_customer", "customer"},
		{"prospect", "other"},
		{"target", "other"},
		{"opportunity", "other"},
		{"unknown", "other"},
	} {
		got := relationshipTypesFor("example.com", tc.lifecycle, cfg)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("lifecycle %q gave %v, want [%s]", tc.lifecycle, got, tc.want)
		}
	}
}

// TestAnOverrideWinsOverTheRule covers the dual-role case ADR-0079 calls the
// basis of the partner program: a company that is a customer AND a partner.
func TestAnOverrideWinsOverTheRule(t *testing.T) {
	cfg := cfgWithOverrides(map[string][]string{"bestit.de": {"customer", "partner"}})
	got := relationshipTypesFor("bestit.de", "customer", cfg)
	if len(got) != 2 || got[0] != "customer" || got[1] != "partner" {
		t.Fatalf("got %v, want [customer partner]", got)
	}
	// The lookup is case-insensitive: refs index domains lowercased, and a
	// dataset entry spelled otherwise must not silently miss its override.
	if got := relationshipTypesFor("BestIT.de", "customer", cfg); len(got) != 2 {
		t.Errorf("uppercase domain missed its override: %v", got)
	}
}

// TestPartnerSurvivesAReplaceSet is the 422 this guards against.
//
// relationship_types is a replace-set and removing `partner` while the org
// still has a partner row is refused, because the two are one invariant. A
// rule that computed plain "customer" for a promoted company would be asking
// the server to break it.
func TestPartnerSurvivesAReplaceSet(t *testing.T) {
	got := withPartnerKept([]string{"customer"}, []string{"customer", "partner"})
	if len(got) != 2 || got[1] != "partner" {
		t.Fatalf("partner was dropped: %v", got)
	}
	// Already asking for it: no duplicate.
	if got := withPartnerKept([]string{"customer", "partner"}, []string{"partner"}); len(got) != 2 {
		t.Errorf("partner duplicated: %v", got)
	}
	// Not a partner: nothing added.
	if got := withPartnerKept([]string{"customer"}, []string{"customer"}); len(got) != 1 {
		t.Errorf("partner invented from nothing: %v", got)
	}
}

// TestSameTypesIgnoresOrder keeps a re-run from rewriting a record whose
// types the server happened to return in another order.
func TestSameTypesIgnoresOrder(t *testing.T) {
	if !sameTypes([]string{"customer", "partner"}, []string{"partner", "customer"}) {
		t.Error("order made two equal sets look different — every re-seed would rewrite them")
	}
	if sameTypes([]string{"customer"}, []string{"customer", "partner"}) {
		t.Error("a longer set compared equal")
	}
}

// TestInventedDomainIsUndeliverable is the rule that keeps invented people
// unmailable. The real company domain would be deliverable to whoever reads
// that mailbox, which is exactly the harm the dataset's defang pass exists to
// prevent.
func TestInventedDomainIsUndeliverable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"tipa.or.kr", "tipa-or-kr.example"},
		{"tinthanhphat.vn", "tinthanhphat-vn.example"},
		{"mv21.kr", "mv21-kr.example"},
	} {
		if got := inventedDomain(tc.in); got != tc.want {
			t.Errorf("inventedDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The whole point: an invented address never lands on the real domain.
	if got := partnerEmail("Park Jun-seo", inventedDomain("tipa.or.kr")); got != "park.junseo@tipa-or-kr.example" {
		t.Errorf("got %q", got)
	}
}

// TestADualRolePartnerIsPromotedBeforeItIsTyped pins a phase-ordering rule
// the server taught us with a 422.
//
// An org IS a partner iff it carries the `partner` TYPE and has a partner
// ROW, and that invariant binds both directions: adding the type to a company
// with no row is refused `partner_row_missing`, just as removing the type
// from one that has a row is refused. A first attempt typed bestit.de as
// customer+partner before promoting it, and the seed stopped there.
//
// The test asserts the dataset's own consistency, which is what a reordering
// would break: every company whose override claims `partner` must also be
// named as a dual-role partner, so the row exists by the time the type is
// written.
func TestADualRolePartnerIsPromotedBeforeItIsTyped(t *testing.T) {
	cfg := demoConfig{
		RelTypes: demoRelTypes{Overrides: map[string][]string{
			"bestit.de":      {"customer", "partner"},
			"aws.amazon.com": {"supplier"},
		}},
		DualRolePartners: []demoDualPartner{{Company: "bestit.de", PartnerRole: "consulting"}},
	}
	promoted := map[string]bool{}
	for _, d := range cfg.DualRolePartners {
		promoted[d.Company] = true
	}
	for domain, types := range cfg.RelTypes.Overrides {
		for _, tt := range types {
			if tt != "partner" {
				continue
			}
			if !promoted[domain] {
				t.Errorf("%s is typed `partner` but nothing promotes it — the type write will 422 with partner_row_missing", domain)
			}
		}
	}
}
