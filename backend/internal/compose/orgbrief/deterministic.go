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
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// Deterministic writes the brief without a model. Every sentence cites the
// record it came from, exactly as the model path's do, so the card renders
// and behaves identically whichever wrote it.
func Deterministic(orgID string, in Input) []Sentence {
	account := accountEvidence(orgID)
	sentences := make([]Sentence, 0, 4)

	sentences = append(sentences, Sentence{Text: identityLine(in), Evidence: account})

	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{
			Text:     pipelineLine(in),
			Evidence: leadDealEvidence(in),
		})
	}
	// One sentence per stalled deal, so the reader opens the one they mean
	// instead of picking between chips hanging off a joined list.
	sentences = append(sentences,
		perRecordSentences(stalledDeals(in), citeDeal, dealID, stalledLine)...)
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		sentences = append(sentences, Sentence{
			Text:     lastTouchLine(last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	if len(in.OpenTasks) > 0 {
		sentences = append(sentences, Sentence{
			Text: fmt.Sprintf("%s, starting with %q.",
				plural(len(in.OpenTasks), "open task"), in.OpenTasks[0].Name),
			// Cites the task itself, so the reader can open the one named.
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.OpenTasks[0].ID}},
		})
	}
	// Then what the company IS. Same two-part shape the model lane is asked
	// for, so the card reads the same whichever wrote it.
	sentences = append(sentences, profileLines(in, account)...)
	return claims.Dedupe(sentences)
}

// profileLabels turn a stored field name into the question it answers.
//
// Label and value are joined with a colon, never grammatically. These values
// are whatever a human accepted off a site read: some are noun phrases and
// some are whole sentences in the company's own language, and a lead-in that
// reads as a sentence stem produced "They sell Als unabhängige Beratung helfen
// wir…" on a real account. A colon is true of both shapes.
//
// The floor states the statement verbatim behind that label — it paraphrases
// nothing, because a paraphrase nobody can check is worth less than the
// sentence a human already accepted.
var profileLabels = map[string]string{
	"offer_summary":     "What they sell",
	"icp":               "Who they sell to",
	"value_proposition": "What they promise",
	"usp":               "How they differentiate",
	"customer_pains":    "What they solve",
	"desired_outcomes":  "What their customers want",
	"buying_center":     "Who decides there",
	"sales_motion":      "How they sell",
}

// deterministicProfileLines bounds the company half of the floor. Two
// statements say what a company does; eight is the profile card, which the
// reader can open underneath.
const deterministicProfileLines = 2

func profileLines(in Input, account []Evidence) []Sentence {
	out := make([]Sentence, 0, deterministicProfileLines)
	for _, entry := range in.Profile {
		if len(out) == deterministicProfileLines {
			break
		}
		label, ok := profileLabels[entry.Field]
		if !ok {
			continue
		}
		// A stored value of nothing but punctuation reduces to nothing, and a
		// line reading "What they sell: ." is worse than no line.
		statement := trimSentence(entry.Value)
		if statement == "" {
			continue
		}
		out = append(out, Sentence{
			Text:     fmt.Sprintf("%s: %s", label, statement),
			Evidence: account,
		})
	}
	return out
}

// trimSentence renders a stored statement as one sentence: a closing full
// stop when the value ends without one, and the author's own terminator kept
// when it has one. Rewriting a "?" into a "." would be an edit to approved
// text, which is the one thing this half of the brief promises not to do —
// so only a dangling list separator is stripped.
func trimSentence(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), ";:, ")
	if trimmed == "" {
		return trimmed
	}
	// "…" included: a statement that trails off already ends, and appending a
	// full stop to it renders "….".
	for _, terminator := range []string{".", "!", "?", "…"} {
		if strings.HasSuffix(trimmed, terminator) {
			return trimmed
		}
	}
	return trimmed + "."
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
	line := plural(len(in.OpenDeals), "open deal")
	total, currency, ok := oneCurrencyTotal(in.OpenDeals)
	if ok && total > 0 {
		// Minor units are rendered as a plain major-unit figure; the card
		// formats money properly, and this text is the fallback.
		line += " worth about " + values.MajorUnits(total, currency) + " " + currency
	}
	// The won total carries its OWN currency: the 360 converts it to the
	// workspace base at each deal's frozen close-time rate, which has no
	// relation to whatever the open deals are priced in. Labelling it with
	// the open currency reported a real figure under the wrong unit.
	if in.WonLifetime > 0 && in.WonCurrency != "" {
		line += "; " + values.MajorUnits(in.WonLifetime, in.WonCurrency) + " " + in.WonCurrency + " won to date"
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

func stalledDeals(in Input) []DealIn {
	stalled := make([]DealIn, 0, len(in.OpenDeals))
	for _, deal := range in.OpenDeals {
		if deal.Stalled {
			stalled = append(stalled, deal)
		}
	}
	return stalled
}

func dealID(deal DealIn) string { return deal.ID }

// leadDealEvidence cites the one deal a pipeline COUNT is anchored on: the
// first the account listed. The sentence names no deal, so it needs somewhere
// for the reader to start, and citing all of them was what produced a row of
// chips nobody could tell apart.
func leadDealEvidence(in Input) []Evidence {
	return []Evidence{{EntityType: citeDeal, EntityID: in.OpenDeals[0].ID}}
}

func stalledLine(deal DealIn) string {
	return fmt.Sprintf("%s is stalled with no recent activity.", deal.Name)
}

// article is "an" before a vowel sound and "a" otherwise. The activity kinds
// this reaches are a closed, ASCII, lower-case set (email, call, meeting,
// note, task…), so first-letter agreement is exact for all of them rather
// than approximately right — and "a email" in a sentence written for a
// salesperson is the register this whole surface is leaving behind.
func article(noun string) string {
	if noun == "" {
		return "a"
	}
	if strings.ContainsRune("aeiou", rune(noun[0])) {
		return "an"
	}
	return "a"
}

func lastTouchLine(last ActIn) string {
	line := "Last contact was " + article(last.Kind) + " " + last.Kind
	if when := shortDate(last.At); when != "" {
		line += " on " + when
	}
	if last.Subject == "" {
		return line + "."
	}
	// The subject is quoted rather than woven into the sentence: it is text
	// from outside the workspace, and it must read as theirs, not ours.
	return fmt.Sprintf("%s: %q.", line, last.Subject)
}

// plural renders a count with the noun it counts. "3 open task(s)" is a
// developer's shorthand printed at a salesperson, which is the register this
// whole surface is trying to leave behind.
func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// shortDate renders an RFC3339 instant the way a reader writes a date, and
// returns empty for one it cannot read.
//
// Empty means "say nothing about when": the caller drops the clause instead of
// printing a machine timestamp at the reader. The instants are formatted by
// this package's own folds, so an unreadable one is a defect upstream of here
// rather than a fact about the account — and the sentence around it is still
// true without the date. The year is always named because these writers hold
// no clock, and "21 Jul" on a task from last year reads as this year.
func shortDate(at string) string {
	if at == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format("2 Jan 2006")
}

// DeterministicSections is the floor in the shape the card renders: the same
// sentences, sorted into the questions they answer.
//
// It writes no `fit` section, and that absence is the honest one. Fit is a
// judgment about what this account is worth to US, and a judgment is exactly
// what a floor with no model cannot make. A heading over a restated fact would
// claim an assessment nobody performed.
func DeterministicSections(orgID string, in Input) []Section {
	account := accountEvidence(orgID)
	sections := make([]Section, 0, 4)

	// What the company IS: its identity, then the curated statements a human
	// already accepted, quoted rather than paraphrased.
	snapshot := append([]Sentence{{Text: identityLine(in), Evidence: account}},
		profileLines(in, account)...)
	sections = append(sections, Section{Kind: sectionSnapshot, Sentences: snapshot})

	var health []Sentence
	if len(in.OpenDeals) > 0 {
		health = append(health, Sentence{Text: pipelineLine(in), Evidence: leadDealEvidence(in)})
	}
	health = append(health, perRecordSentences(stalledDeals(in), citeDeal, dealID, stalledLine)...)
	if len(health) > 0 {
		sections = append(sections, Section{Kind: sectionHealth, Sentences: claims.Dedupe(health)})
	}

	var activity []Sentence
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		activity = append(activity, Sentence{
			Text:     lastTouchLine(last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	if len(activity) > 0 {
		sections = append(sections, Section{Kind: sectionActivity, Sentences: activity})
	}

	if len(in.OpenTasks) > 0 {
		sections = append(sections, Section{Kind: sectionNextStep, Sentences: []Sentence{{
			Text: fmt.Sprintf("%s, starting with %q.",
				plural(len(in.OpenTasks), "open task"), in.OpenTasks[0].Name),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.OpenTasks[0].ID}},
		}}})
	}
	return sections
}
