// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// factsOfField builds n distinct facts of one field, in merge order.
func factsOfField(field string, n int) []people.DeepReadFact {
	out := make([]people.DeepReadFact, 0, n)
	for i := range n {
		value := fmt.Sprintf("%s %02d", field, i)
		out = append(out, people.DeepReadFact{
			Field: field, Value: value, ValueKey: value,
			Category: factCategoryByField[field],
		})
	}
	return out
}

func countByField(facts []people.DeepReadFact) map[string]int {
	byField := map[string]int{}
	for _, f := range facts {
		byField[f.Field]++
	}
	return byField
}

// The bands exist to spend a fixed budget, so their shares must add up to
// exactly that budget — a sum below it silently under-fills every read, a
// sum above it lets the bands promise slots the cap will not honour.
func TestFactBandQuotasSpendExactlyTheSelectionBudget(t *testing.T) {
	total := 0
	for _, band := range factBands {
		total += band.quota
	}
	if total != identity.MaxSelectedFacts {
		t.Fatalf("band quotas sum to %d, want identity.MaxSelectedFacts (%d)", total, identity.MaxSelectedFacts)
	}
}

// Every fact field the vocabulary defines must be claimed by a band, or a
// new field would compete only in the leftovers of the last one.
func TestEveryFactFieldIsBanded(t *testing.T) {
	banded := map[string]bool{}
	for _, band := range factBands {
		for _, field := range band.fields {
			banded[field] = true
		}
	}
	for field := range factCategoryByField {
		if !banded[field] {
			t.Errorf("fact field %q belongs to no curation band — add it to factBands", field)
		}
	}
}

func TestCapFactsKeepsWhatTheCompanySellsOverItsPartnerWall(t *testing.T) {
	// The shape that made a head-of-list cut wrong: the partner wall and
	// the office list commit early, the offering pages commit last.
	var facts []people.DeepReadFact
	facts = append(facts, factsOfField("technology", 70)...)
	facts = append(facts, factsOfField("location", 22)...)
	facts = append(facts, factsOfField("service", 60)...)
	facts = append(facts, factsOfField("product", 27)...)
	facts = append(facts, factsOfField("named_customer", 10)...)

	got := capFacts(facts)
	if len(got) != identity.MaxSelectedFacts {
		t.Fatalf("curated to %d facts, want the full budget (%d)", len(got), identity.MaxSelectedFacts)
	}
	byField := countByField(got)
	if offerings := byField["service"] + byField["product"]; offerings != 40 {
		t.Errorf("named offerings kept = %d, want the band's 40 (a head-of-list cut kept 8)", offerings)
	}
	if byField["named_customer"] != 10 {
		t.Errorf("named customers kept = %d, want all 10 — proof is the second-richest band", byField["named_customer"])
	}
	// The stack legitimately absorbs what the bands above it could not
	// spend — the budget is for spending — but never at their expense.
	if byField["technology"] >= len(got) {
		t.Errorf("technology kept = %d of %d: the stack crowded out richer bands", byField["technology"], len(got))
	}
	if byField["location"] > 5 {
		t.Errorf("locations kept = %d, want at most the band's 5: offices are the thinnest context", byField["location"])
	}
}

func TestCapFactsLendsUnfilledQuotaToLaterBands(t *testing.T) {
	// A company with almost nothing to sell still gets a full page: the
	// offering band lends its unspent share down the priority order
	// rather than returning a short read.
	var facts []people.DeepReadFact
	facts = append(facts, factsOfField("service", 3)...)
	facts = append(facts, factsOfField("technology", 200)...)

	got := capFacts(facts)
	if len(got) != identity.MaxSelectedFacts {
		t.Fatalf("curated to %d facts, want the budget filled by the lending band (%d)", len(got), identity.MaxSelectedFacts)
	}
	byField := countByField(got)
	if byField["service"] != 3 {
		t.Errorf("all 3 services must survive, got %d", byField["service"])
	}
}

func TestCapFactsLeavesASmallReadAlone(t *testing.T) {
	facts := factsOfField("service", 12)
	got := capFacts(facts)
	if len(got) != 12 {
		t.Fatalf("a read under the budget is not curated: got %d of 12", len(got))
	}
}

// The merge's order is the read's order: curation selects, it never sorts.
func TestCapFactsPreservesMergeOrder(t *testing.T) {
	var facts []people.DeepReadFact
	facts = append(facts, factsOfField("technology", 40)...)
	facts = append(facts, factsOfField("service", 80)...)

	got := capFacts(facts)
	seen := map[string]int{}
	for i, fact := range got {
		seen[fact.Value] = i
	}
	last := -1
	for _, fact := range facts {
		at, kept := seen[fact.Value]
		if !kept {
			continue
		}
		if at < last {
			t.Fatalf("curation reordered the read at %q", fact.Value)
		}
		last = at
	}
}
