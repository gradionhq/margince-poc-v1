// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The dossier without a model (DOSS-PARAM-7, DOSS-AC-7).
//
// One sentence per populated profile field, each citing that field's own row.
// It never infers: no "they seem to be growing", no "a good fit for us",
// because a sentence nobody can check is worth less than the fact it
// paraphrases. This is the FLOOR, not a degraded mode to apologise for — a
// deployment with no model configured gets a plainer dossier assembled from the
// same facts, and the surface says which produced it.

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// Section is one part of the dossier: a heading's worth of claims about one
// question.
type Section struct {
	Kind      string            `json:"kind"`
	Sentences []claims.Sentence `json:"sentences"`
}

// The section kinds, DERIVED from the contract's enum rather than re-spelled,
// so a rename upstream fails to compile here instead of laundering a
// hand-typed string past the type.
var (
	sectionSummary         = string(crmcontracts.OrganizationDossierSectionKindSummary)
	sectionProductsService = string(crmcontracts.OrganizationDossierSectionKindProductsServices)
	sectionMarkets         = string(crmcontracts.OrganizationDossierSectionKindMarkets)
	sectionBuyingCenter    = string(crmcontracts.OrganizationDossierSectionKindBuyingCenter)
	sectionDifferentiation = string(crmcontracts.OrganizationDossierSectionKindDifferentiation)
	sectionFirmographics   = string(crmcontracts.OrganizationDossierSectionKindFirmographics)
)

// natureFact is the only nature the floor produces. The floor restates recorded
// values and draws no conclusions, so every sentence it writes is a fact by
// construction — labelling one an assessment would be a claim about a judgment
// nobody made.
var natureFact = string(crmcontracts.Fact)

// fieldSections routes a profile field to the section a reader would look for
// it under. A field with no home here still reaches the summary, because
// dropping a recorded value because this table has not learned about it yet
// would hide a fact the product holds.
var fieldSections = map[string]string{
	"icp":             sectionMarkets,
	"industry":        sectionMarkets,
	"offer_summary":   sectionProductsService,
	"products":        sectionProductsService,
	"services":        sectionProductsService,
	"buying_center":   sectionBuyingCenter,
	"buying_intents":  sectionBuyingCenter,
	"differentiation": sectionDifferentiation,
	"legal_name":      sectionFirmographics,
	"register_vat":    sectionFirmographics,
	"founded_year":    sectionFirmographics,
	"employee_range":  sectionFirmographics,
	"headquarters":    sectionFirmographics,
}

// sectionOrder is the order a reader asks the questions, and therefore the
// order the sections render in. It is a list rather than a map iteration
// because a dossier whose sections reshuffled between reads would read as a
// different answer to the same question.
var sectionOrder = []string{
	sectionSummary, sectionProductsService, sectionMarkets,
	sectionBuyingCenter, sectionDifferentiation, sectionFirmographics,
}

// Deterministic writes the dossier without a model. Every sentence cites the
// row it came from, exactly as the model path's must, so a sentence is
// checkable whichever wrote it.
func Deterministic(in Input) []Section {
	bySection := map[string][]claims.Sentence{}
	for _, field := range in.ProfileFields {
		sentence, ok := fieldSentence(field)
		if !ok {
			continue
		}
		kind, known := fieldSections[string(field.Field)]
		if !known {
			kind = sectionSummary
		}
		bySection[kind] = append(bySection[kind], sentence)
	}
	return orderedSections(bySection)
}

// fieldSentence renders one profile field as one cited sentence.
//
// A field with no row id is SKIPPED rather than cited against the organization.
// The citation is the reader's way to check the claim, and pointing them at the
// company record instead tells them where to look but not at what — the
// grounding filter would drop such a sentence anyway, so producing one here
// would only make the floor and the filter disagree.
func fieldSentence(field crmcontracts.CompanyProfileField) (claims.Sentence, bool) {
	value := strings.TrimSpace(field.Value)
	if value == "" || field.Id == nil {
		return claims.Sentence{}, false
	}
	return claims.Sentence{
		Text:   fieldLabel(string(field.Field)) + ": " + value + ".",
		Nature: natureFact,
		Evidence: []claims.Evidence{{
			EntityType: citeProfileField,
			EntityID:   field.Id.String(),
		}},
	}, true
}

// fieldLabel turns a column name into something a person reads. An unmapped
// field falls back to its own name with the underscores opened out, which is
// plainer than a lookup miss and still true.
func fieldLabel(field string) string {
	if label, ok := fieldLabels[field]; ok {
		return label
	}
	return strings.ReplaceAll(field, "_", " ")
}

var fieldLabels = map[string]string{
	"icp":             "Ideal customer",
	"industry":        "Industry",
	"offer_summary":   "What they offer",
	"buying_center":   "Buying centre",
	"buying_intents":  "Buying intents",
	"differentiation": "What sets them apart",
	"legal_name":      "Legal name",
	"register_vat":    "Registration",
	"founded_year":    "Founded",
	"employee_range":  "Size",
	"headquarters":    "Headquarters",
}

// orderedSections renders the populated sections in reading order. A section
// with nothing to say is ABSENT rather than empty: a heading over silence reads
// as a finding of nothing, which is a different claim.
func orderedSections(bySection map[string][]claims.Sentence) []Section {
	out := make([]Section, 0, len(sectionOrder))
	for _, kind := range sectionOrder {
		sentences := bySection[kind]
		if len(sentences) == 0 {
			continue
		}
		out = append(out, Section{Kind: kind, Sentences: sentences})
	}
	return out
}
