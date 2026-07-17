// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The category extraction's own no-guess contract: evidence must be
// verbatim on the page, multi-value facts dedupe on their normalized
// value_key, and the cross-page merge lets the more specific page kind
// answer a single-value fact.

import (
	"context"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func TestExtractCategoryDropsAFactWhoseEvidenceIsNotVerbatim(t *testing.T) {
	page := strings.Repeat("Acme serves the mid-market. ", 5) + "Certified to ISO 27001."
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"certification","value":"ISO 27001 — information security","evidence_snippet":"Certified to ISO 27001","confidence":0.9},
		{"field":"certification","value":"SOC 2 — invented","evidence_snippet":"nowhere on this page","confidence":0.95},
		{"field":"partner","value":"Empty Corp — no evidence","evidence_snippet":"  ","confidence":0.9},
		{"field":"named_customer","value":"Overconfident — bad number","evidence_snippet":"Acme serves the mid-market.","confidence":1.5},
		{"field":"icp","value":"not a signal field","evidence_snippet":"Acme serves the mid-market.","confidence":0.8}]}`)
	x := evidenceExtractor{brain: brain}

	facts, err := x.extractCategory(context.Background(), "signal", "Page", page, "https://acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Field != "certification" || facts[0].Value != "ISO 27001 — information security" {
		t.Fatalf("facts = %+v, want only the verbatim-evidenced certification — hallucinated, empty-evidence, out-of-range and off-vocabulary entries all dropped", facts)
	}
	if facts[0].ValueKey != "iso 27001" {
		t.Fatalf("value_key = %q, want the normalized name before the separator", facts[0].ValueKey)
	}
}

func TestExtractCategoryDedupesMultiValueOnValueKeyKeepingHighestConfidence(t *testing.T) {
	page := strings.Repeat("What we do. ", 5) + "CRM Rollout projects, done right. Data migration too."
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"service","value":"CRM Rollout — projects","evidence_snippet":"CRM Rollout projects","confidence":0.5},
		{"field":"service","value":"crm  rollout — done right","evidence_snippet":"CRM Rollout projects, done right","confidence":0.9},
		{"field":"service","value":"Data Migration — moving data","evidence_snippet":"Data migration too","confidence":0.7}]}`)
	x := evidenceExtractor{brain: brain}

	facts, err := x.extractCategory(context.Background(), "offering", "Page", page, "https://acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %+v, want the two distinct value_keys", facts)
	}
	// The two spellings of "CRM Rollout" share one normalized key; the
	// surer read wins and keeps the first-seen position.
	if facts[0].ValueKey != "crm rollout" || facts[0].Confidence != 0.9 {
		t.Fatalf("deduped service = %+v, want value_key 'crm rollout' at confidence 0.9", facts[0])
	}
	if facts[1].ValueKey != "data migration" {
		t.Fatalf("second service = %+v, want 'data migration'", facts[1])
	}
}

func TestMergeCategoryFactsLetsTheMoreSpecificPageKindAnswerSingleValueFacts(t *testing.T) {
	fact := func(field, value string, confidence float32) people.DeepReadFact {
		return people.DeepReadFact{
			Category: "company", Field: field, Value: value,
			EvidenceSnippet: value, SourceURL: "https://acme.example", Confidence: confidence,
		}
	}
	merged := mergeCategoryFacts([]pageFields{
		// The contact page states a phone at HIGH confidence; the Impressum
		// later states another at lower confidence — source specificity
		// outranks self-reported confidence, so the Impressum wins.
		{kind: crmcontracts.SiteReadPageKindContact, facts: []people.DeepReadFact{
			fact("phone", "+49 30 111", 0.95),
			fact("contact_email", "hello@acme.example", 0.6),
		}},
		{kind: crmcontracts.SiteReadPageKindImpressum, facts: []people.DeepReadFact{
			fact("phone", "+49 711 222", 0.7),
		}},
		// A second contact-kind page at the SAME rank only wins on higher
		// confidence.
		{kind: crmcontracts.SiteReadPageKindContact, facts: []people.DeepReadFact{
			fact("contact_email", "sales@acme.example", 0.8),
		}},
	})

	byField := map[string]people.DeepReadFact{}
	for _, f := range merged {
		byField[f.Field] = f
	}
	if len(merged) != 2 {
		t.Fatalf("merged = %+v, want one answer per single-value field", merged)
	}
	if byField["phone"].Value != "+49 711 222" {
		t.Fatalf("phone = %q, want the Impressum's statement over the contact page's higher-confidence one", byField["phone"].Value)
	}
	if byField["contact_email"].Value != "sales@acme.example" {
		t.Fatalf("contact_email = %q, want the same-rank higher-confidence answer", byField["contact_email"].Value)
	}
}

func TestMergeCategoryFactsUnionsMultiValueAcrossPages(t *testing.T) {
	offering := func(value string, confidence float32) people.DeepReadFact {
		return people.DeepReadFact{
			Category: "offering", Field: "service", Value: value,
			ValueKey: people.NormalizeFactValueKey(value), EvidenceSnippet: value,
			SourceURL: "https://acme.example", Confidence: confidence,
		}
	}
	merged := mergeCategoryFacts([]pageFields{
		{kind: crmcontracts.SiteReadPageKindServices, facts: []people.DeepReadFact{
			offering("CRM Rollout — projects", 0.6),
			offering("Data Migration — moving data", 0.7),
		}},
		{kind: crmcontracts.SiteReadPageKindProducts, facts: []people.DeepReadFact{
			offering("CRM Rollout — the surer restatement", 0.9),
			offering("Training — enablement", 0.8),
		}},
	})
	if len(merged) != 3 {
		t.Fatalf("merged = %+v, want the union of the three value_keys", merged)
	}
	if merged[0].Value != "CRM Rollout — the surer restatement" {
		t.Fatalf("shared value_key kept %q, want the higher-confidence restatement", merged[0].Value)
	}
}

func TestFactCategoryForPageKindCoversExactlyTheFactBearingKinds(t *testing.T) {
	want := map[crmcontracts.SiteReadPageKind]string{
		crmcontracts.SiteReadPageKindImpressum: "company",
		crmcontracts.SiteReadPageKindContact:   "company",
		crmcontracts.SiteReadPageKindServices:  "offering",
		crmcontracts.SiteReadPageKindProducts:  "offering",
		crmcontracts.SiteReadPageKindHome:      "signal",
		crmcontracts.SiteReadPageKindAbout:     "signal",
	}
	for kind, category := range want {
		got, ok := factCategoryForPageKind(kind)
		if !ok || got != category {
			t.Fatalf("factCategoryForPageKind(%s) = %q/%v, want %q — the page kind lost its category call", kind, got, ok, category)
		}
	}
	for _, kind := range []crmcontracts.SiteReadPageKind{crmcontracts.SiteReadPageKindTeam, crmcontracts.SiteReadPageKindOther} {
		if category, ok := factCategoryForPageKind(kind); ok {
			t.Fatalf("factCategoryForPageKind(%s) = %q, want no category — the per-page call budget allows none here", kind, category)
		}
	}
}
