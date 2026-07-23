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
	"log/slog"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
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

// verifySelectedOption re-derives the current clarifications and checks
// the echoed selection against them, keeping the grant version-bound and
// server-authored: an unknown or stale clarify id, a field that does not
// belong to it, or — for a closed option list — a value the server never
// offered is refused before it can authorize anything. A free-text
// clarification accepts any non-empty value for its field; option values
// are locale-invariant, so the check holds whatever language the options
// were rendered in.
func verifySelectedOption(selection crmcontracts.OnboardingClarifySelection, read *people.SiteRead, comparisons []people.SiteReadComparison, locale string) error {
	var clarifies []crmcontracts.OnboardingClarify
	if read != nil {
		clarifies = onboardingClarifies(*read, comparisons, locale)
	}
	clarifyID := strings.TrimSpace(selection.ClarifyId)
	for _, clarify := range clarifies {
		if clarify.Id != clarifyID {
			continue
		}
		if clarify.Field != strings.TrimSpace(selection.Field) {
			return httperr.Validation("selected_option.field", "invalid", "the selection names a different field than its clarification")
		}
		value := strings.TrimSpace(selection.Value)
		for _, option := range clarify.Options {
			if option.Value == value {
				return nil
			}
		}
		if clarify.AllowFreeText != nil && *clarify.AllowFreeText {
			return nil
		}
		return httperr.Validation("selected_option.value", "invalid", "the value is not one of this clarification's options")
	}
	return httperr.Validation("selected_option.clarify_id", "stale", "this clarification is no longer open; re-read the current questions and ask again")
}

// onboardingClarifies runs every detector over the read and its
// comparisons, in a stable order: entity identity first (it decides who
// the company IS), then per-field conflicts. This is the FULL set —
// selection verification checks against it so an already-answered
// question's option can still be re-picked; presentation paths use
// openOnboardingClarifies to stop re-asking what the draft resolved.
func onboardingClarifies(read people.SiteRead, comparisons []people.SiteReadComparison, locale string) []crmcontracts.OnboardingClarify {
	out := entityClarifies(read, locale)
	return append(out, conflictClarifies(read.DraftVersion, comparisons, locale)...)
}

// openOnboardingClarifies drops every question the persisted draft has
// already answered: a question is resolved ONLY when the draft's value
// for its field exactly equals one of the question's option values
// (post-trim) — that match is provably an earlier authorized selection
// or an accepted value, so re-deriving the question from site evidence
// alone must not forget it on restore. A hand-typed value that matches
// no option deliberately leaves the question open: the server cannot
// distinguish a human's overriding edit from a read-prefilled draft, so
// only the provable case resolves.
func openOnboardingClarifies(read people.SiteRead, comparisons []people.SiteReadComparison, locale string, draft identity.OnboardingCompanyDraft) []crmcontracts.OnboardingClarify {
	values := onboardingDraftValues(draft)
	all := onboardingClarifies(read, comparisons, locale)
	open := make([]crmcontracts.OnboardingClarify, 0, len(all))
	for _, clarify := range all {
		if clarifyAnsweredByDraft(clarify, values) {
			continue
		}
		open = append(open, clarify)
	}
	return open
}

func clarifyAnsweredByDraft(clarify crmcontracts.OnboardingClarify, values map[string]*string) bool {
	value := values[clarify.Field]
	if value == nil {
		return false
	}
	answer := strings.TrimSpace(*value)
	if answer == "" {
		return false
	}
	for _, option := range clarify.Options {
		if option.Value == answer {
			return true
		}
	}
	return false
}

// onboardingDraftValues maps each clarifiable profile field to its draft
// value. Fact-conflict questions carry composite keys with no draft
// counterpart, so they never resolve through the draft — their answers
// live in the confirm call's resolutions.
func onboardingDraftValues(draft identity.OnboardingCompanyDraft) map[string]*string {
	return map[string]*string{
		fieldDisplayName: draft.DisplayName, fieldOfferSummary: draft.OfferSummary,
		fieldICP: draft.ICP, fieldValueProposition: draft.ValueProposition,
		fieldUSP: draft.USP, fieldCustomerPains: draft.CustomerPains,
		fieldDesiredOutcomes: draft.DesiredOutcomes, fieldBuyingCenter: draft.BuyingCenter,
		fieldBuyingIntents: draft.BuyingIntents, fieldCommonObjections: draft.CommonObjections,
		fieldSalesMotion: draft.SalesMotion, fieldLegalName: draft.LegalName,
		fieldRegisteredAddress: draft.RegisteredAddress, fieldRegisterVat: draft.RegisterVAT,
		fieldIndustry: draft.Industry, fieldHistory: draft.History,
	}
}

// onboardingClarifyID is stable per read draft version, so a re-poll of
// the same draft re-identifies the same question and a new draft version
// retires stale answers.
func onboardingClarifyID(field string, draftVersion int) string {
	return fmt.Sprintf("clarify:%s:%d", field, draftVersion)
}

// The clarify plausibility bounds: a legal name or address a human is
// asked to pick must LOOK like one. Values beyond these caps, spanning
// lines, or shaped like navigation chrome are extraction debris — an
// unanswerable option must never gate the save.
const (
	clarifyNameMaxRunes    = 120
	clarifyAddressMaxRunes = 160
	// A token printed this often inside ONE candidate is menu chrome, not
	// a printed identity.
	clarifyTokenRepeatLimit = 3
	// An "address" running past this many words with neither a comma nor
	// a digit is a scraped link trail, not a postal address.
	clarifyAddressMaxPlainWords = 6
)

// entityClarifies asks which printed legal entity (and which printed
// registered address) the installation belongs to when the legal notice
// names more than one. Option values are the plausible printed strings
// (whitespace-collapsed); free text is off — the census question is a
// pick among what the site states, and the manual edit path stays
// available elsewhere. A question exists ONLY when at least two
// plausible options survive the filter: one survivor means there is no
// ambiguity worth blocking a save on. Filtered candidates are logged at
// debug for operator diagnosability, never shown.
func entityClarifies(read people.SiteRead, locale string) []crmcontracts.OnboardingClarify {
	if len(read.LegalEntities) < 2 {
		return nil
	}
	var out []crmcontracts.OnboardingClarify
	names := make([]crmcontracts.OnboardingClarifyOption, 0, len(read.LegalEntities))
	seenName := map[string]bool{}
	addresses := make([]crmcontracts.OnboardingClarifyOption, 0, len(read.LegalEntities))
	seenAddress := map[string]bool{}
	var dropped []string
	for _, entity := range read.LegalEntities {
		// An implausible half yields "" — the other half's option then
		// simply carries no detail rather than showing the debris.
		name, nameOK := plausibleClarifyValue(entity.Name, clarifyNameMaxRunes, false)
		address, addressOK := plausibleClarifyValue(entity.RegisteredAddress, clarifyAddressMaxRunes, true)
		if !nameOK && strings.TrimSpace(entity.Name) != "" {
			dropped = append(dropped, boundedRunes(entity.Name, clarifyNameMaxRunes))
		}
		if !addressOK && strings.TrimSpace(entity.RegisteredAddress) != "" {
			dropped = append(dropped, boundedRunes(entity.RegisteredAddress, clarifyAddressMaxRunes))
		}
		if nameOK && !seenName[name] {
			seenName[name] = true
			names = append(names, entityOption(name, name, entity, address))
		}
		if addressOK && !seenAddress[address] {
			seenAddress[address] = true
			addresses = append(addresses, entityOption(address, address, entity, name))
		}
	}
	if len(dropped) > 0 {
		slog.Debug("onboarding clarify candidates filtered as implausible",
			"read_id", read.ID, "dropped", dropped)
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

// plausibleClarifyValue normalizes one candidate and decides whether a
// human could answer with it: trimmed, single-line, whitespace-collapsed,
// inside the length cap, no token repeated to the chrome threshold, and
// (for addresses) not a long word trail with neither comma nor digit.
// Every presentation and verification path shares this one builder, so
// what can be picked is exactly what was shown.
func plausibleClarifyValue(raw string, maxRunes int, addressShaped bool) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "\n\r") {
		return "", false
	}
	value := strings.Join(strings.Fields(trimmed), " ")
	if len([]rune(value)) > maxRunes {
		return "", false
	}
	words := strings.Fields(strings.ToLower(value))
	counts := make(map[string]int, len(words))
	for _, word := range words {
		counts[word]++
		if counts[word] >= clarifyTokenRepeatLimit {
			return "", false
		}
	}
	if addressShaped && len(words) > clarifyAddressMaxPlainWords && !strings.ContainsAny(value, ",0123456789") {
		return "", false
	}
	return value, true
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
