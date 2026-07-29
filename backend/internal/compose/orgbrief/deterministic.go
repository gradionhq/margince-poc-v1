// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The deterministic brief: the floor every deployment gets, and the shape
// the model lane is asked to rewrite rather than exceed.
//
// It states facts already on the page, in the order a rep reads them:
// what this account is, what is open, what is stuck, what happened last.
// It never infers — no "they seem interested", no "worth a call" — because
// a sentence nobody can check is worth less than the number it paraphrases.

import (
	"fmt"
	"strings"
)

// Deterministic writes the brief without a model. Every sentence cites the
// record it came from, exactly as the model path's do, so the card renders
// and behaves identically whichever wrote it.
func Deterministic(orgID string, in Input) []Sentence {
	account := []Evidence{{EntityType: citeOrganization, EntityID: orgID}}
	sentences := make([]Sentence, 0, 4)

	sentences = append(sentences, Sentence{Text: identityLine(in), Evidence: account})

	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{
			Text:     pipelineLine(in),
			Evidence: dealEvidence(in),
		})
	}
	if stalled := stalledNames(in); len(stalled) > 0 {
		sentences = append(sentences, Sentence{
			Text:     fmt.Sprintf("Stalled with no recent activity: %s.", strings.Join(stalled, ", ")),
			Evidence: stalledEvidence(in),
		})
	}
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		sentences = append(sentences, Sentence{
			Text:     lastTouchLine(last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	if len(in.OpenTasks) > 0 {
		sentences = append(sentences, Sentence{
			Text: fmt.Sprintf("%d open task(s), starting with %q.",
				len(in.OpenTasks), in.OpenTasks[0].Name),
			// Cites the task itself, so the reader can open the one named.
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.OpenTasks[0].ID}},
		})
	}
	return sentences
}

func identityLine(in Input) string {
	parts := []string{in.Name}
	if in.Industry != "" {
		parts = append(parts, in.Industry)
	}
	if in.SizeBand != "" {
		parts = append(parts, in.SizeBand+" people")
	}
	line := strings.Join(parts, ", ") + "."
	if in.ContactCount > 0 {
		// The score is reported with the contact count it was taken over, so
		// a strong number from one contact never reads like a broad
		// relationship.
		line += fmt.Sprintf(" Relationship strength %d across %d known contact(s).",
			in.Strength, in.ContactCount)
	}
	return line
}

func pipelineLine(in Input) string {
	line := fmt.Sprintf("%d open deal(s)", len(in.OpenDeals))
	total, currency, ok := oneCurrencyTotal(in.OpenDeals)
	if ok && total > 0 {
		// Minor units are rendered as a plain major-unit figure; the card
		// formats money properly, and this text is the fallback.
		line += fmt.Sprintf(" worth about %d %s", total/100, currency)
	}
	if in.WonLifetime > 0 && currency != "" {
		line += fmt.Sprintf("; %d %s won to date", in.WonLifetime/100, currency)
	}
	return line + "."
}

// oneCurrencyTotal sums the open deals only when they all agree on a
// currency.
//
// Adding minor units across currencies produces a number that is not money
// in any of them, and labelling the result with whichever deal happened to
// come first states it as a fact. A mixed-currency account gets the deal
// COUNT and no total: the card converts and totals properly, and this text
// is the floor, so under-reporting is the only honest option here.
func oneCurrencyTotal(deals []DealIn) (total int64, currency string, ok bool) {
	for _, deal := range deals {
		if deal.AmountMinor == 0 {
			continue // an amountless deal contributes nothing, and no currency
		}
		if deal.Currency == "" {
			// An amount whose currency nobody recorded cannot be added to
			// anything: folded into a later deal's total it would be reported
			// as that currency, which is a figure this account never had.
			// (The deal_amount_currency_pair CHECK makes this unreachable
			// from the database; Input is a plain struct, and the total is
			// money.)
			return 0, "", false
		}
		if currency == "" {
			currency = deal.Currency
		}
		if deal.Currency != currency {
			return 0, "", false
		}
		total += deal.AmountMinor
	}
	return total, currency, currency != ""
}

func stalledNames(in Input) []string {
	var names []string
	for _, deal := range in.OpenDeals {
		if deal.Stalled {
			names = append(names, deal.Name)
		}
	}
	return names
}

func dealEvidence(in Input) []Evidence {
	out := make([]Evidence, 0, len(in.OpenDeals))
	for _, deal := range in.OpenDeals {
		out = append(out, Evidence{EntityType: citeDeal, EntityID: deal.ID})
	}
	return out
}

func stalledEvidence(in Input) []Evidence {
	out := make([]Evidence, 0)
	for _, deal := range in.OpenDeals {
		if deal.Stalled {
			out = append(out, Evidence{EntityType: citeDeal, EntityID: deal.ID})
		}
	}
	return out
}

func lastTouchLine(last ActIn) string {
	if last.Subject == "" {
		return fmt.Sprintf("Last contact was a %s on %s.", last.Kind, last.At)
	}
	// The subject is quoted rather than woven into the sentence: it is text
	// from outside the workspace, and it must read as theirs, not ours.
	return fmt.Sprintf("Last contact was a %s on %s: %q.", last.Kind, last.At, last.Subject)
}
