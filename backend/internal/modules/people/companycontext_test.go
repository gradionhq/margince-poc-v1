// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAssembleCompanyContextIsCanonicalAndScoped(t *testing.T) {
	generatedAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	company := Company{
		OrganizationID:         ids.From[ids.OrganizationKind](ids.MustParse("018f3a1b-0000-7000-8000-0000000000a1")),
		DisplayName:            "Gradion",
		OrganizationSource:     "manual",
		OrganizationCapturedBy: "human:owner",
		ProfileFields: []CompanyProfileField{
			{Field: fieldICP, Value: "Mid-market manufacturers", Source: companySourceHuman, CapturedBy: "human:owner", Confidence: 1},
			{Field: fieldOfferSummary, Value: "Revenue software", Source: companySourceSiteRead, CapturedBy: "agent:site-read", SourceURL: "https://gradion.com", Confidence: 0.9},
			{Field: fieldDisplayName, Value: "Gradion", Source: companySourceHuman, CapturedBy: "human:owner", Confidence: 1},
		},
		Facts: []CompanyFact{
			{Category: "signal", Field: "named_customer", Value: "Acme", ValueKey: "acme", Source: companySourceSiteRead, CapturedBy: "agent:site-read", Confidence: 0.8},
			{Category: "signal", Field: "technology", Value: "Go", ValueKey: "go", Source: companySourceSiteRead, CapturedBy: "agent:site-read", Confidence: 0.8},
			{Category: "offering", Field: "service", Value: "Advisory", ValueKey: "advisory", Source: companySourceHuman, CapturedBy: "human:owner", Confidence: 1},
		},
	}

	context := assembleCompanyContext(company, []CompanyContextScope{
		CompanyContextProof, CompanyContextOffer, CompanyContextPositioning, CompanyContextOffer,
	}, generatedAt)

	wantScopes := []CompanyContextScope{CompanyContextPositioning, CompanyContextOffer, CompanyContextProof}
	if len(context.Scopes) != len(wantScopes) {
		t.Fatalf("scopes = %d, want %d", len(context.Scopes), len(wantScopes))
	}
	for i, want := range wantScopes {
		if context.Scopes[i].Scope != want {
			t.Fatalf("scope[%d] = %q, want %q", i, context.Scopes[i].Scope, want)
		}
	}
	if got := context.Scopes[1].Items; len(got) != 3 || got[0].Key != fieldOfferSummary || got[1].Key != "service" || got[2].Key != "technology" {
		t.Fatalf("offer items = %#v, want offer_summary, service, technology", got)
	}
	if got := context.Scopes[2].Items; len(got) != 1 || got[0].Key != "named_customer" {
		t.Fatalf("proof items = %#v, want named_customer only", got)
	}
	if context.GeneratedAt != generatedAt || context.SchemaVersion != 1 || len(context.Fingerprint) != 64 {
		t.Fatalf("metadata = version %d fingerprint %q at %s", context.SchemaVersion, context.Fingerprint, context.GeneratedAt)
	}

	reordered := company
	reordered.ProfileFields = []CompanyProfileField{company.ProfileFields[2], company.ProfileFields[0], company.ProfileFields[1]}
	reordered.Facts = []CompanyFact{company.Facts[2], company.Facts[0], company.Facts[1]}
	again := assembleCompanyContext(reordered, []CompanyContextScope{
		CompanyContextOffer, CompanyContextPositioning, CompanyContextProof,
	}, generatedAt.Add(time.Hour))
	if context.Fingerprint != again.Fingerprint {
		t.Fatalf("fingerprint changed with input order: %q != %q", context.Fingerprint, again.Fingerprint)
	}

	reordered.ProfileFields[1].Value = "Enterprise manufacturers"
	changed := assembleCompanyContext(reordered, wantScopes, generatedAt)
	if context.Fingerprint == changed.Fingerprint {
		t.Fatal("fingerprint did not change when a contributing value changed")
	}
}

func TestAssembleCompanyContextFallsBackToAnchorIdentity(t *testing.T) {
	website := "gradion.com"
	company := Company{
		OrganizationID:         ids.From[ids.OrganizationKind](ids.MustParse("018f3a1b-0000-7000-8000-0000000000a1")),
		DisplayName:            "Gradion",
		Website:                &website,
		OrganizationSource:     "manual",
		OrganizationCapturedBy: "human:owner",
	}

	context := assembleCompanyContext(company, []CompanyContextScope{CompanyContextIdentity}, time.Time{})
	items := context.Scopes[0].Items
	if len(items) != 2 || items[0].Key != fieldDisplayName || items[1].Key != "primary_domain" {
		t.Fatalf("identity items = %#v, want display_name and primary_domain", items)
	}
	for _, item := range items {
		if item.Source != companySourceHuman || item.CapturedBy != "human:owner" {
			t.Fatalf("fallback provenance = %q/%q, want human/human:owner", item.Source, item.CapturedBy)
		}
	}
}
