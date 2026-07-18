// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The crawl merge's wrong-company guards: only a shallow legal page
// overrides the legal trio, and disagreeing legal pages cancel the
// override entirely — a missing legal field beats another company's.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func legalNameField(value, sourceURL string) evidencedField {
	return evidencedField{
		Field: string(crmcontracts.LegalName), Value: value,
		EvidenceSnippet: value, SourceURL: sourceURL, Confidence: 0.8,
	}
}

func TestMergeCrawlFieldsShallowLegalPageOverridesTheSeedGuess(t *testing.T) {
	merged, conflict := mergeCrawlFields([]pageFields{
		{
			url: seedURL, kind: crmcontracts.SiteReadPageKindHome,
			fields: []evidencedField{legalNameField("Acme (guessed)", seedURL)},
		},
		{
			url: seedURL + "/de/impressum", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("Acme Robotics GmbH", seedURL+"/de/impressum")},
		},
	})
	if conflict {
		t.Fatal("one legal page is no conflict")
	}
	if len(merged) != 1 || merged[0].Value != "Acme Robotics GmbH" {
		t.Fatalf("the shallow Impressum should win the legal name: %+v", merged)
	}
}

func TestMergeCrawlFieldsDeepLegalPathHasNoOverridePower(t *testing.T) {
	merged, conflict := mergeCrawlFields([]pageFields{
		{
			url: seedURL, kind: crmcontracts.SiteReadPageKindHome,
			fields: []evidencedField{legalNameField("Acme Robotics", seedURL)},
		},
		// A customer's imprint hosted under a deep path classifies as
		// impressum but must merge as an ordinary page.
		{
			url: seedURL + "/customers/other-co/legal", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("Other Co AG", seedURL+"/customers/other-co/legal")},
		},
	})
	if conflict {
		t.Fatal("a deep legal page joins the seed side; it cannot conflict")
	}
	if len(merged) != 1 || merged[0].Value != "Acme Robotics" {
		t.Fatalf("the deep legal page overrode the seed: %+v", merged)
	}
}

func TestMergeCrawlFieldsDisagreeingLegalPagesAbstainFromTheWholeLegalTrio(t *testing.T) {
	seedFields := []evidencedField{
		legalNameField("Acme Robotics", seedURL),
		{Field: string(crmcontracts.DisplayName), Value: "Acme", EvidenceSnippet: "Acme", SourceURL: seedURL, Confidence: 0.9},
	}
	merged, conflict := mergeCrawlFields([]pageFields{
		{
			url: seedURL, kind: crmcontracts.SiteReadPageKindHome,
			fields: seedFields,
		},
		{
			url: seedURL + "/impressum", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("Acme Robotics GmbH", seedURL+"/impressum")},
		},
		{
			url: seedURL + "/de/impressum", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("Beispiel Holding AG", seedURL+"/de/impressum")},
		},
	})
	if !conflict {
		t.Fatal("two disagreeing legal names must flag the multi-entity domain")
	}
	// With the entity in dispute, NO legal identity is proposed — not the
	// losing legal page's, and not a marketing page's either. Non-legal
	// fields survive untouched.
	if len(merged) != 1 || merged[0].Field != string(crmcontracts.DisplayName) {
		t.Fatalf("the abstention must strip the legal trio and keep the rest: %+v", merged)
	}
}

func TestMergeCrawlFieldsAgreeingLegalPagesStillOverride(t *testing.T) {
	// The same legal name reflowed with typography differences is one
	// entity, not a conflict.
	merged, conflict := mergeCrawlFields([]pageFields{
		{
			url: seedURL, kind: crmcontracts.SiteReadPageKindHome,
			fields: []evidencedField{legalNameField("Acme (guessed)", seedURL)},
		},
		{
			url: seedURL + "/impressum", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("Acme Robotics GmbH", seedURL+"/impressum")},
		},
		{
			url: seedURL + "/de/impressum", kind: crmcontracts.SiteReadPageKindImpressum,
			fields: []evidencedField{legalNameField("acme  robotics GmbH", seedURL+"/de/impressum")},
		},
	})
	if conflict {
		t.Fatal("normalization-equal legal names are one entity, not a conflict")
	}
	if len(merged) != 1 || merged[0].SourceURL == seedURL {
		t.Fatalf("agreeing legal pages should still override the seed guess: %+v", merged)
	}
}
