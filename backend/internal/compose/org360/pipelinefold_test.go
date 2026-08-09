// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The money rules behind the company page's open-pipeline figure (plan §4.2).
// They are proven here rather than through the database because what is at
// stake is arithmetic and honesty, not SQL: which deals may enter a total, what
// the total means when some of them cannot, and what a converted figure has to
// carry beside it.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func day(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("parsing %q: %v", value, err)
	}
	return parsed
}

// deal builds one scanned row the way the DEAL WRITER leaves it: an amount in
// the deal's own currency, and — for an open deal — no frozen conversion rate,
// because the rate freezes on close. A foreign-currency open deal therefore
// carries a nil valueBase, which is exactly what the database returns.
func deal(amount *int64, currency string, rateDate *time.Time) openRow {
	row := openRow{
		id: ids.NewV7(), name: "Deal", baseCcy: "EUR",
		amountMinor: amount, currency: &currency, rateDate: rateDate,
	}
	// A closed-and-reopened deal is the only open row that carries a frozen
	// rate, and the generated column follows the rate.
	if amount != nil && currency != "EUR" && rateDate != nil {
		converted := *amount
		row.valueBase = &converted
	}
	return row
}

func amount(minor int64) *int64 { return &minor }

// The case the whole figure rests on, and the one a column-only read gets
// wrong: an ordinary open deal in the workspace's own currency. It has no
// frozen FX rate — the rate freezes on close — so amount_minor_base is null,
// and a total built from that column alone would price every open pipeline at
// nothing on every installation.
func TestAnOpenDealInTheBaseCurrencyIsPricedWithoutAnyFxRate(t *testing.T) {
	out := foldPipeline([]openRow{
		deal(amount(250000), "EUR", nil),
		deal(amount(150000), "EUR", nil),
	})

	if out.Priced != 2 {
		t.Fatalf("Priced = %d, want 2 — an open deal in the base currency needs no rate", out.Priced)
	}
	if out.ValueMinorBase != 400000 {
		t.Fatalf("ValueMinorBase = %d, want 400000", out.ValueMinorBase)
	}
	if out.Converted != 0 || out.FXAsOf != nil {
		t.Fatalf("Converted/FXAsOf = %d/%v, want 0/nil — nothing was converted",
			out.Converted, out.FXAsOf)
	}
}

// A foreign-currency deal with no frozen rate cannot be converted, so it must
// not enter the total — and must still count as open, so the page reports a
// partial figure rather than a silently short one.
func TestAForeignOpenDealWithNoFrozenRateStaysOutOfTheTotal(t *testing.T) {
	out := foldPipeline([]openRow{
		deal(amount(100000), "EUR", nil),
		deal(amount(999999), "USD", nil),
	})

	if out.ValueMinorBase != 100000 {
		t.Fatalf("ValueMinorBase = %d, want 100000 — the unconvertible deal contributes nothing", out.ValueMinorBase)
	}
	if out.Priced != 1 || out.OpenCount != 2 {
		t.Fatalf("Priced/OpenCount = %d/%d, want 1/2 — the gap is what the page discloses",
			out.Priced, out.OpenCount)
	}
	if out.FXAsOf != nil {
		t.Fatalf("FXAsOf = %v, want nil — no conversion happened", out.FXAsOf)
	}
}

func TestAPipelineWhoseDealsCarryNoAmountIsPricedAtNothing(t *testing.T) {
	out := foldPipeline([]openRow{
		deal(nil, "EUR", nil),
		deal(nil, "EUR", nil),
	})

	if out.OpenCount != 2 {
		t.Fatalf("OpenCount = %d, want 2 — the deals are open whether or not they carry a figure", out.OpenCount)
	}
	// Priced == 0 is what tells the page to show no money. A zero TOTAL with a
	// non-zero Priced would claim a pipeline that exists and is worth nothing.
	if out.Priced != 0 {
		t.Fatalf("Priced = %d, want 0 — nothing here could be added up", out.Priced)
	}
	if out.ValueMinorBase != 0 {
		t.Fatalf("ValueMinorBase = %d, want 0", out.ValueMinorBase)
	}
}

// A total that covers some of the pipeline must report how much, or a reader
// takes a partial figure for the whole one.
func TestAPartialTotalReportsHowManyDealsItCovers(t *testing.T) {
	out := foldPipeline([]openRow{
		deal(amount(150000), "EUR", nil),
		deal(nil, "EUR", nil),
		deal(amount(50000), "EUR", nil),
	})

	if out.ValueMinorBase != 200000 {
		t.Fatalf("ValueMinorBase = %d, want 200000 (the two priced deals)", out.ValueMinorBase)
	}
	if out.Priced != 2 || out.OpenCount != 3 {
		t.Fatalf("Priced/OpenCount = %d/%d, want 2/3 — the gap is what the page discloses",
			out.Priced, out.OpenCount)
	}
}

// §4.2 forbids a cross-currency sum without an explicit conversion source and
// as-of date. The as-of is the OLDEST rate standing behind the figure: each
// deal freezes its own, so that is how far back any part of the total reaches.
func TestAConvertedTotalCarriesTheOldestRateDateBehindIt(t *testing.T) {
	recent, older := day(t, "2026-07-01"), day(t, "2026-02-14")
	out := foldPipeline([]openRow{
		deal(amount(100000), "USD", &recent),
		deal(amount(100000), "CHF", &older),
		deal(amount(100000), "EUR", nil),
	})

	if out.Converted != 2 {
		t.Fatalf("Converted = %d, want 2 — the euro deal needed no rate", out.Converted)
	}
	if out.FXAsOf == nil || !out.FXAsOf.Equal(older) {
		t.Fatalf("FXAsOf = %v, want %v (the furthest back the figure reaches)", out.FXAsOf, older)
	}
}

// A same-currency total has no rate behind it, and must not claim one.
func TestASameCurrencyTotalNamesNoConversion(t *testing.T) {
	out := foldPipeline([]openRow{
		deal(amount(100000), "EUR", nil),
		deal(amount(25000), "EUR", nil),
	})

	if out.Converted != 0 {
		t.Fatalf("Converted = %d, want 0 — nothing was converted", out.Converted)
	}
	if out.FXAsOf != nil {
		t.Fatalf("FXAsOf = %v, want nil — an as-of date here would name a rate that never applied", out.FXAsOf)
	}
	if out.BaseCurrency != "EUR" {
		t.Fatalf("BaseCurrency = %q, want EUR", out.BaseCurrency)
	}
}

// The nearest expected close is what a prospect's page reports, so it is the
// minimum over the deals that name one — not the first the scan happened to
// return, and not disturbed by deals that name none.
func TestTheNextCloseIsTheNearestDateAnyOpenDealNames(t *testing.T) {
	later, sooner := day(t, "2026-12-01"), day(t, "2026-09-30")
	rows := []openRow{deal(nil, "EUR", nil), deal(nil, "EUR", nil), deal(nil, "EUR", nil)}
	rows[0].closeOn = &later
	rows[2].closeOn = &sooner

	out := foldPipeline(rows)

	if out.NextCloseOn == nil || !out.NextCloseOn.Equal(sooner) {
		t.Fatalf("NextCloseOn = %v, want %v", out.NextCloseOn, sooner)
	}
}

func TestAPipelineWithNoExpectedCloseNamesNoDate(t *testing.T) {
	out := foldPipeline([]openRow{deal(amount(1000), "EUR", nil)})

	if out.NextCloseOn != nil {
		t.Fatalf("NextCloseOn = %v, want nil — no deal here names a close date", out.NextCloseOn)
	}
}
