// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The onboarding clarify detectors: every open question the conversation
// asks is detected HERE, deterministically, from the persisted read —
// the model narrates a question but never invents its options. Two
// detectors exist: the legal-entity census (a group's legal notice names
// several entities / addresses and the read refuses to guess which one
// the installation is) and the human-conflict comparisons (the site
// proposes a value a human already set differently). A conflict answer
// maps 1:1 onto the existing CompanySiteReadResolution contract at
// confirm time: keeping the current value is keep_current, taking the
// site's value is accept_proposal.

import (
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// onboardingClarifyOptionLimit mirrors the contract's options maxItems:
// over-clarifying is a failure mode too, so a census with more entities
// than this presents the first six as printed.
const onboardingClarifyOptionLimit = 6

const localeDE = "de"

// withSelectedOption records the clarify option the administrator
// clicked — the strongest explicit instruction there is. It grants
// exactly the named field with the chosen value (post-trim exact match)
// and nothing else; every other change still answers to the phrase
// heuristics in companyChangeAuthorization.allows.
func (a companyChangeAuthorization) withSelectedOption(field, value string) companyChangeAuthorization {
	a.selectedField = strings.TrimSpace(field)
	a.selectedValue = strings.TrimSpace(value)
	return a
}

// onboardingClarifies runs every detector over the read and its
// comparisons, in a stable order: entity identity first (it decides who
// the company IS), then per-field conflicts.
func onboardingClarifies(read people.SiteRead, comparisons []people.SiteReadComparison, locale string) []crmcontracts.OnboardingClarify {
	out := entityClarifies(read, locale)
	return append(out, conflictClarifies(read.DraftVersion, comparisons, locale)...)
}

// onboardingClarifyID is stable per read draft version, so a re-poll of
// the same draft re-identifies the same question and a new draft version
// retires stale answers.
func onboardingClarifyID(field string, draftVersion int) string {
	return fmt.Sprintf("clarify:%s:%d", field, draftVersion)
}

// entityClarifies asks which printed legal entity (and which printed
// registered address) the installation belongs to when the legal notice
// names more than one. Option values are the exact printed strings; free
// text is off — the census question is a pick among what the site
// states, and the manual edit path stays available elsewhere.
func entityClarifies(read people.SiteRead, locale string) []crmcontracts.OnboardingClarify {
	if len(read.LegalEntities) < 2 {
		return nil
	}
	var out []crmcontracts.OnboardingClarify
	names := make([]crmcontracts.OnboardingClarifyOption, 0, len(read.LegalEntities))
	seenName := map[string]bool{}
	addresses := make([]crmcontracts.OnboardingClarifyOption, 0, len(read.LegalEntities))
	seenAddress := map[string]bool{}
	for _, entity := range read.LegalEntities {
		if name := strings.TrimSpace(entity.Name); name != "" && !seenName[name] {
			seenName[name] = true
			names = append(names, entityOption(name, name, entity, entity.RegisteredAddress))
		}
		if address := strings.TrimSpace(entity.RegisteredAddress); address != "" && !seenAddress[address] {
			seenAddress[address] = true
			addresses = append(addresses, entityOption(address, address, entity, entity.Name))
		}
	}
	if len(names) > 1 {
		out = append(out, crmcontracts.OnboardingClarify{
			Id:       onboardingClarifyID(fieldLegalName, read.DraftVersion),
			Field:    fieldLegalName,
			Question: clarifyEntityQuestion(locale),
			Options:  capClarifyOptions(names),
		})
	}
	if len(addresses) > 1 {
		out = append(out, crmcontracts.OnboardingClarify{
			Id:       onboardingClarifyID(fieldRegisteredAddress, read.DraftVersion),
			Field:    fieldRegisteredAddress,
			Question: clarifyAddressQuestion(locale),
			Options:  capClarifyOptions(addresses),
		})
	}
	return out
}

func entityOption(value, label string, entity people.SiteReadLegalEntity, detail string) crmcontracts.OnboardingClarifyOption {
	option := crmcontracts.OnboardingClarifyOption{Value: value, Label: label}
	if url := strings.TrimSpace(entity.SourceURL); url != "" {
		option.EvidenceUrl = &url
	}
	if snippet := strings.TrimSpace(entity.EvidenceSnippet); snippet != "" {
		option.EvidenceSnippet = &snippet
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		option.Detail = &detail
	}
	return option
}

func capClarifyOptions(options []crmcontracts.OnboardingClarifyOption) []crmcontracts.OnboardingClarifyOption {
	if len(options) > onboardingClarifyOptionLimit {
		return options[:onboardingClarifyOptionLimit]
	}
	return options
}

// conflictClarifies turns each human-conflict comparison into a
// keep-yours-vs-take-the-site's question. Both option values are the
// exact stored strings, so the answer round-trips loss-free into a
// resolution: option one is keep_current, option two accept_proposal.
// Free text stays allowed — use_value is a legal resolution too.
func conflictClarifies(draftVersion int, comparisons []people.SiteReadComparison, locale string) []crmcontracts.OnboardingClarify {
	var out []crmcontracts.OnboardingClarify
	for _, comparison := range comparisons {
		if crmcontracts.CompanySiteReadComparisonClassification(comparison.Classification) != crmcontracts.CompanySiteReadComparisonClassificationHumanConflict ||
			comparison.CurrentValue == nil {
			continue
		}
		allowFreeText := true
		out = append(out, crmcontracts.OnboardingClarify{
			Id:            onboardingClarifyID(comparison.Key, draftVersion),
			Field:         comparison.Key,
			Question:      clarifyConflictQuestion(locale, comparison.Key),
			AllowFreeText: &allowFreeText,
			Options: []crmcontracts.OnboardingClarifyOption{
				conflictOption(*comparison.CurrentValue, clarifyKeepLabel(locale), clarifyKeepDetail(locale)),
				conflictOption(comparison.ProposedValue, clarifyTakeLabel(locale), clarifyTakeDetail(locale)),
			},
		})
	}
	return out
}

func conflictOption(value, label, detail string) crmcontracts.OnboardingClarifyOption {
	return crmcontracts.OnboardingClarifyOption{Value: value, Label: label, Detail: &detail}
}

func clarifyEntityQuestion(locale string) string {
	if locale == localeDE {
		return "Die rechtlichen Angaben der Website nennen mehrere juristische Personen. Welche ist Ihr Unternehmen?"
	}
	return "The legal notice names more than one legal entity. Which one is your company?"
}

func clarifyAddressQuestion(locale string) string {
	if locale == localeDE {
		return "Die Website nennt mehrere Geschäftsanschriften. Welche gehört zu Ihrem Unternehmen?"
	}
	return "The website states more than one registered address. Which one belongs to your company?"
}

func clarifyConflictQuestion(locale, key string) string {
	if locale == localeDE {
		return fmt.Sprintf("Ihr gespeicherter Wert für %s unterscheidet sich von der Website. Welchen Wert soll ich verwenden?", key)
	}
	return fmt.Sprintf("Your saved value for %s differs from what the website states. Which value should I use?", key)
}

func clarifyKeepLabel(locale string) string {
	if locale == localeDE {
		return "Meinen Wert behalten"
	}
	return "Keep my value"
}

func clarifyKeepDetail(locale string) string {
	if locale == localeDE {
		return "Von einem Menschen eingetragen; bleibt bei der Bestätigung unverändert (keep_current)."
	}
	return "Entered by a human; confirming keeps it unchanged (keep_current)."
}

func clarifyTakeLabel(locale string) string {
	if locale == localeDE {
		return "Wert der Website übernehmen"
	}
	return "Use the website's value"
}

func clarifyTakeDetail(locale string) string {
	if locale == localeDE {
		return "Von der Website gelesen; die Bestätigung übernimmt diesen Wert (accept_proposal)."
	}
	return "Read from the website; confirming takes this value (accept_proposal)."
}
