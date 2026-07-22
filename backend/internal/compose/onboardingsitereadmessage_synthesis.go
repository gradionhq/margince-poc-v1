// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "github.com/gradionhq/margince/backend/internal/modules/people"

// Factual identity fields deliberately do not appear here: a legal name,
// address, registration, VAT number, or display name must occur in its cited
// evidence. Interpretive fields may combine only the dossier concepts mapped
// to them below.
var companySynthesisEvidence = map[string]map[string]struct{}{
	fieldIndustry:         evidenceFields(fieldIndustry, people.FactServedIndustry, people.FactService, people.FactProduct, people.FactCapability),
	fieldOfferSummary:     evidenceFields(fieldOfferSummary, people.FactService, people.FactProduct, people.FactCapability),
	fieldICP:              evidenceFields(fieldICP, fieldOfferSummary, people.FactServedIndustry, people.FactCompanySize, people.FactGeography, people.FactNamedCustomer, people.FactService, people.FactProduct, people.FactCapability),
	fieldValueProposition: evidenceFields(fieldValueProposition, fieldOfferSummary, fieldCustomerPains, fieldDesiredOutcomes, people.FactQuantifiedOutcome),
	fieldUSP:              evidenceFields(fieldUSP, fieldValueProposition, people.FactCapability, people.FactTechnology, people.FactCertification, people.FactQuantifiedOutcome),
	fieldCustomerPains:    evidenceFields(fieldCustomerPains, fieldOfferSummary, people.FactService, people.FactProduct, people.FactCapability),
	fieldDesiredOutcomes:  evidenceFields(fieldDesiredOutcomes, fieldValueProposition, people.FactQuantifiedOutcome),
	fieldBuyingCenter:     evidenceFields(fieldBuyingCenter, fieldICP, people.FactNamedCustomer),
	fieldBuyingIntents:    evidenceFields(fieldBuyingIntents, fieldCustomerPains, fieldOfferSummary, people.FactService, people.FactProduct),
	fieldCommonObjections: evidenceFields(fieldCommonObjections, fieldCustomerPains),
	fieldSalesMotion:      evidenceFields(fieldSalesMotion, fieldBuyingCenter, people.FactContactEmail),
	fieldHistory:          evidenceFields(fieldHistory, people.FactFoundedYear, people.FactLocation),
}

func evidenceFields(fields ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

func companyRecommendationSupportsSynthesis(replyKind, targetField string, sourceIDs map[string]struct{}, known map[string]companyReadEvidence) bool {
	if replyKind != companyConversationRecommendation {
		return false
	}
	relevant, ok := companySynthesisEvidence[targetField]
	if !ok {
		return false
	}
	for sourceID := range sourceIDs {
		if _, ok := relevant[known[sourceID].Field]; ok {
			return true
		}
	}
	return false
}
