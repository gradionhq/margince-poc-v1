// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The spec's own worked examples, run as tests (finance-ingestion.md
// FIN-FORM-1..5). Using the chapter's numbers rather than invented ones is
// deliberate: it makes a disagreement between this code and the specification
// fail here, where a reader can see both.

import (
	"testing"
	"time"
)

func on(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("parsing %q: %v", value, err)
	}
	return parsed
}

func ptr(t time.Time) *time.Time { return &t }

// FIN-FORM-1's worked example: three issued invoices of 8.750, 9.820 and
// 12.430 euro, plus a credit note of 1.000 pointing at the first, whose
// credited total therefore carries that same 1.000. Expected: 30.000 over
// THREE records — the credit note is not the fourth.
func TestNetInvoicedSubtractsACreditNoteExactlyOnce(t *testing.T) {
	asOf := on(t, "2026-08-09")
	issued := on(t, "2026-06-01")
	out := NetInvoicedOver([]Invoice{
		{Status: "paid", IssuedOn: issued, NetMinorBase: 875000, CreditedMinorBase: 100000},
		{Status: "open", IssuedOn: issued, NetMinorBase: 982000},
		{Status: "open", IssuedOn: issued, NetMinorBase: 1243000},
		// The credit note itself: excluded from the positive term, and already
		// subtracted through the first invoice's credited total above.
		{Status: "credited", IssuedOn: issued, NetMinorBase: 100000, CreditsInvoice: true},
	}, asOf)

	if out.AmountMinorBase != 3000000 {
		t.Fatalf("net invoiced = %d, want 3000000 (31.000 issued less 1.000 credited)", out.AmountMinorBase)
	}
	if out.Records != 3 {
		t.Fatalf("records = %d, want 3 — the credit note is not a fourth invoice", out.Records)
	}
}

// Counting the credit note in the positive term as well would subtract it
// twice. This is the failure FIN-AC-N-1 exists to pin, so it gets its own case.
func TestACreditNoteIsNotCountedInThePositiveTerm(t *testing.T) {
	asOf := on(t, "2026-08-09")
	issued := on(t, "2026-06-01")
	withCredit := NetInvoicedOver([]Invoice{
		{Status: "paid", IssuedOn: issued, NetMinorBase: 500000, CreditedMinorBase: 100000},
		{Status: "credited", IssuedOn: issued, NetMinorBase: 100000, CreditsInvoice: true},
	}, asOf)

	// 5.000 issued less 1.000 credited is 4.000. Counting the note in both
	// places gives 3.000; counting it in neither gives 5.000.
	if withCredit.AmountMinorBase != 400000 {
		t.Fatalf("net invoiced = %d, want 400000 — subtracted once, not twice or never",
			withCredit.AmountMinorBase)
	}
}

// A draft was never issued, and a void invoice is excluded entirely rather
// than netted to zero — so the RECORD COUNT stays honest as well as the total.
func TestNetInvoicedExcludesDraftsAndVoidedInvoices(t *testing.T) {
	asOf := on(t, "2026-08-09")
	issued := on(t, "2026-06-01")
	out := NetInvoicedOver([]Invoice{
		{Status: "open", IssuedOn: issued, NetMinorBase: 100000},
		{Status: "draft", IssuedOn: issued, NetMinorBase: 999900},
		{Status: "void", IssuedOn: issued, NetMinorBase: 999900},
	}, asOf)

	if out.AmountMinorBase != 100000 {
		t.Fatalf("net invoiced = %d, want 100000", out.AmountMinorBase)
	}
	if out.Records != 1 {
		t.Fatalf("records = %d, want 1 — a void invoice is not a record worth zero", out.Records)
	}
}

// FIN-AC-6: one invoice with no conversion rate on its issue date refuses the
// WHOLE figure. Converting the rest would report a smaller number under the
// label of a complete one.
func TestOneUnconvertibleInvoiceRefusesTheWholeFigure(t *testing.T) {
	asOf := on(t, "2026-08-09")
	issued := on(t, "2026-06-01")
	out := NetInvoicedOver([]Invoice{
		{Status: "open", IssuedOn: issued, NetMinorBase: 100000},
		{Status: "open", IssuedOn: issued, NetMinorBase: 200000, RateMissing: true},
	}, asOf)

	if !out.RateUnavailable {
		t.Fatal("figure was served despite an invoice with no rate; a partial sum must refuse")
	}
	if out.AmountMinorBase != 0 {
		t.Fatalf("amount = %d, want 0 — a refused figure carries no number", out.AmountMinorBase)
	}
}

func TestNetInvoicedIgnoresInvoicesOutsideTheWindow(t *testing.T) {
	asOf := on(t, "2026-08-09")
	out := NetInvoicedOver([]Invoice{
		{Status: "open", IssuedOn: on(t, "2026-06-01"), NetMinorBase: 100000},
		{Status: "open", IssuedOn: on(t, "2024-01-01"), NetMinorBase: 999900},
	}, asOf)

	if out.AmountMinorBase != 100000 || out.Records != 1 {
		t.Fatalf("net invoiced = %d over %d records, want 100000 over 1",
			out.AmountMinorBase, out.Records)
	}
	if out.WindowDays != IssuedWindowDays {
		t.Fatalf("window = %d days, want %d — the figure reports the window it used",
			out.WindowDays, IssuedWindowDays)
	}
}

// The lifetime figure's whole point: the row the trailing window drops is the
// row it must keep. Same invoices as the case above, so the two readings can
// be compared directly.
func TestNetInvoicedLifetimeKeepsWhatTheTrailingWindowDrops(t *testing.T) {
	asOf := on(t, "2026-08-09")
	invoices := []Invoice{
		{Status: "open", IssuedOn: on(t, "2026-06-01"), NetMinorBase: 100000},
		{Status: "open", IssuedOn: on(t, "2024-01-01"), NetMinorBase: 999900},
	}

	out := NetInvoicedLifetime(invoices, asOf)
	if out.AmountMinorBase != 1099900 || out.Records != 2 {
		t.Fatalf("lifetime = %d over %d records, want 1099900 over 2",
			out.AmountMinorBase, out.Records)
	}
	// 0 means "no lower bound", which is what lets a surface label this
	// figure lifetime rather than as some number of days.
	if out.WindowDays != 0 {
		t.Fatalf("window = %d, want 0 — lifetime is not a trailing window", out.WindowDays)
	}
}

// Lifetime is a wider window over the SAME fold, so the three behaviours that
// make the trailing figure honest have to survive the widening: a credit note
// is subtracted once and never counted as a record, a draft and a void are
// excluded entirely, and one unconvertible row refuses the whole total.
func TestNetInvoicedLifetimeKeepsTheFoldsHonestyRules(t *testing.T) {
	asOf := on(t, "2026-08-09")
	old := on(t, "2019-03-04")

	credited := NetInvoicedLifetime([]Invoice{
		{Status: "paid", IssuedOn: old, NetMinorBase: 500000, CreditedMinorBase: 100000},
		{Status: "credited", IssuedOn: old, NetMinorBase: 100000, CreditsInvoice: true},
	}, asOf)
	if credited.AmountMinorBase != 400000 || credited.Records != 1 {
		t.Fatalf("credited lifetime = %d over %d records, want 400000 over 1",
			credited.AmountMinorBase, credited.Records)
	}

	skipped := NetInvoicedLifetime([]Invoice{
		{Status: "open", IssuedOn: old, NetMinorBase: 100000},
		{Status: "draft", IssuedOn: old, NetMinorBase: 700000},
		{Status: "void", IssuedOn: old, NetMinorBase: 700000},
	}, asOf)
	if skipped.AmountMinorBase != 100000 || skipped.Records != 1 {
		t.Fatalf("lifetime = %d over %d records, want 100000 over 1 — a draft was never issued and a void is excluded, not netted to zero",
			skipped.AmountMinorBase, skipped.Records)
	}

	refused := NetInvoicedLifetime([]Invoice{
		{Status: "open", IssuedOn: old, NetMinorBase: 100000},
		{Status: "open", IssuedOn: old, RateMissing: true},
	}, asOf)
	if !refused.RateUnavailable {
		t.Fatal("lifetime was served despite an invoice with no rate; a partial sum must refuse (FIN-AC-6)")
	}
	if refused.AmountMinorBase != 0 {
		t.Fatalf("amount = %d, want 0 — a refused figure carries no number", refused.AmountMinorBase)
	}
}

// FIN-FORM-2's worked example: open invoices of 19.750, 12.430 and 2.000, the
// second due 19 days ago and the others not yet due.
func TestOpenBalanceSeparatesWhatIsOverdueFromWhatIsMerelyOwed(t *testing.T) {
	asOf := on(t, "2026-08-09")
	out := OpenBalanceAt([]Invoice{
		{Status: "open", OpenMinorBase: 1975000, DueOn: ptr(on(t, "2026-09-01"))},
		{Status: "open", OpenMinorBase: 1243000, DueOn: ptr(on(t, "2026-07-21"))},
		{Status: "open", OpenMinorBase: 200000, DueOn: ptr(on(t, "2026-08-20"))},
	}, asOf)

	if out.OpenMinorBase != 3418000 {
		t.Fatalf("open = %d, want 3418000", out.OpenMinorBase)
	}
	if out.OverdueMinorBase != 1243000 || out.OverdueCount != 1 {
		t.Fatalf("overdue = %d across %d, want 1243000 across 1",
			out.OverdueMinorBase, out.OverdueCount)
	}
	if out.OldestOverdueDays != 19 {
		t.Fatalf("oldest overdue = %d days, want 19", out.OldestOverdueDays)
	}
}

// A disputed invoice is genuinely owed and genuinely contested. Counting it as
// open is right; describing it only as overdue would call a disagreement a
// payment failure.
func TestADisputedInvoiceStillCountsAsOwed(t *testing.T) {
	asOf := on(t, "2026-08-09")
	out := OpenBalanceAt([]Invoice{
		{Status: "disputed", OpenMinorBase: 500000, DueOn: ptr(on(t, "2026-07-01")), Disputed: true},
	}, asOf)

	if out.OpenMinorBase != 500000 {
		t.Fatalf("open = %d, want 500000 — a disputed invoice is still owed", out.OpenMinorBase)
	}
}

// FIN-FORM-3's worked examples: due 15 May settled 23 May is +8; due and
// settled on 13 April is 0, not early.
func TestDaysLateCountsFromTheDueDate(t *testing.T) {
	late, ok := DaysLate(Invoice{
		DueOn: ptr(on(t, "2026-05-15")), FullyPaidAt: ptr(on(t, "2026-05-23")),
	})
	if !ok || late != 8 {
		t.Fatalf("days late = %d (ok=%v), want +8", late, ok)
	}

	onDay, ok := DaysLate(Invoice{
		DueOn: ptr(on(t, "2026-04-13")), FullyPaidAt: ptr(on(t, "2026-04-13")),
	})
	if !ok || onDay != 0 {
		t.Fatalf("days late = %d (ok=%v), want 0 — settled on the due date is not early", onDay, ok)
	}
}

// Punctuality is defined only for a fully settled, undisputed invoice. A
// partially paid one has no settlement date; a disputed one's delay is an
// argument rather than a habit.
func TestAnUnsettledOrDisputedInvoiceHasNoPunctuality(t *testing.T) {
	if _, ok := DaysLate(Invoice{DueOn: ptr(on(t, "2026-05-15"))}); ok {
		t.Fatal("an unsettled invoice reported a punctuality it cannot have")
	}
	if _, ok := DaysLate(Invoice{
		DueOn: ptr(on(t, "2026-05-15")), FullyPaidAt: ptr(on(t, "2026-06-15")), Disputed: true,
	}); ok {
		t.Fatal("a disputed invoice reported punctuality; the delay is a disagreement")
	}
}

// FIN-FORM-4's worked example: settled invoices at −2, 0, 3, 8, 12 give a
// median of 3 over a sample of 5. The on-time rate over that same sample is
// 2 of 5 — see the note on the assertion below.
func TestMedianAndOnTimeRateReadTheSameSample(t *testing.T) {
	asOf := on(t, "2026-08-09")
	settle := func(daysLate int) Invoice {
		due := on(t, "2026-06-01")
		return Invoice{DueOn: ptr(due), FullyPaidAt: ptr(due.AddDate(0, 0, daysLate))}
	}
	out := TimelinessOver([]Invoice{
		settle(-2), settle(0), settle(3), settle(8), settle(12),
	}, asOf)

	if out.InsufficientSample {
		t.Fatal("five settled invoices is the floor, not below it")
	}
	if out.MedianDaysLate != 3 {
		t.Fatalf("median = %d, want 3", out.MedianDaysLate)
	}
	if out.SampleSize != 5 {
		t.Fatalf("sample = %d, want 5 — the denominator is part of the figure", out.SampleSize)
	}
	// Of −2, 0, 3, 8 and 12, exactly two are at or before due. FIN-FORM-5's
	// prose says "4 of 5", which does not describe FIN-FORM-4's own sample —
	// the two examples were written against different sets. The rule is what
	// is implemented: count(days_late <= 0) over the sample.
	if out.OnTimeRate != 0.4 {
		t.Fatalf("on-time rate = %v, want 0.4 (2 of 5 at or before due)", out.OnTimeRate)
	}
}

// The same customer with only three settled invoices: below FIN-PARAM-3, so
// the answer is "not enough to say" rather than a number. This is what stops
// one late payment becoming a reputation.
func TestTooFewSettledInvoicesAnswerInsufficientSample(t *testing.T) {
	asOf := on(t, "2026-08-09")
	settle := func(daysLate int) Invoice {
		due := on(t, "2026-06-01")
		return Invoice{DueOn: ptr(due), FullyPaidAt: ptr(due.AddDate(0, 0, daysLate))}
	}
	out := TimelinessOver([]Invoice{settle(-2), settle(0), settle(8)}, asOf)

	if !out.InsufficientSample {
		t.Fatal("three settled invoices produced a timeliness figure; the floor is five")
	}
	if out.MedianDaysLate != 0 || out.OnTimeRate != 0 {
		t.Fatalf("a refused figure carried values (median %d, rate %v)",
			out.MedianDaysLate, out.OnTimeRate)
	}
	if out.SampleSize != 3 {
		t.Fatalf("sample = %d, want 3 — the refusal still says how many it had", out.SampleSize)
	}
}

// Median, not mean, is the whole point: one disputed-then-settled invoice at
// 180 days would drag a mean into a false story about a customer who otherwise
// pays on time.
func TestOneVeryLateInvoiceDoesNotMoveTheMedian(t *testing.T) {
	// The four prompt settlements land on the due date and just after it; the
	// fifth lands 180 days later. asOf sits just past that last one, and the
	// due date is chosen so all five settlements fall inside the trailing
	// window — this case is about the median, not about the window.
	asOf := on(t, "2026-10-30")
	settle := func(daysLate int) Invoice {
		due := on(t, "2026-05-03")
		return Invoice{DueOn: ptr(due), FullyPaidAt: ptr(due.AddDate(0, 0, daysLate))}
	}
	out := TimelinessOver([]Invoice{
		settle(0), settle(1), settle(2), settle(3), settle(180),
	}, asOf)

	// The mean here is 37 days — a story about a bad payer. The median is 2.
	if out.MedianDaysLate != 2 {
		t.Fatalf("median = %d, want 2 — a mean would report 37 and misdescribe them", out.MedianDaysLate)
	}
}

// A settlement and a due date on the SAME calendar day is zero days late,
// whatever time of day either carries and whatever zone they were recorded in.
// Measuring elapsed hours instead reports −1 or +1 here.
func TestSameDaySettlementIsZeroWhateverTheTimeOfDay(t *testing.T) {
	due := time.Date(2026, 5, 15, 17, 0, 0, 0, time.UTC)
	paid := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)

	days, ok := DaysLate(Invoice{DueOn: &due, FullyPaidAt: &paid})

	if !ok || days != 0 {
		t.Fatalf("days late = %d (ok=%v), want 0 — settled the same day it was due", days, ok)
	}
}

// A span crossing a daylight-saving transition is still a count of dates. An
// hours-based subtraction loses or gains one here.
func TestDaysLateSurvivesADaylightSavingTransition(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading the zone: %v", err)
	}
	// 29 March 2026 is the spring-forward night in Europe/Berlin.
	due := time.Date(2026, 3, 27, 12, 0, 0, 0, berlin)
	paid := time.Date(2026, 3, 31, 12, 0, 0, 0, berlin)

	days, ok := DaysLate(Invoice{DueOn: &due, FullyPaidAt: &paid})

	if !ok || days != 4 {
		t.Fatalf("days late = %d (ok=%v), want 4 calendar days across the DST change", days, ok)
	}
}

// FIN-AC-6 governs the open balance exactly as it governs net invoiced: a
// total assembled from the convertible rows is a smaller debt wearing the
// label of the whole one.
func TestOneUnconvertibleInvoiceRefusesTheOpenBalance(t *testing.T) {
	asOf := on(t, "2026-08-09")
	out := OpenBalanceAt([]Invoice{
		{Status: "open", OpenMinorBase: 100000, DueOn: ptr(on(t, "2026-07-01"))},
		{Status: "open", OpenMinorBase: 900000, DueOn: ptr(on(t, "2026-07-01")), RateMissing: true},
	}, asOf)

	if !out.RateUnavailable {
		t.Fatal("open balance was served despite an invoice with no rate")
	}
	if out.OpenMinorBase != 0 {
		t.Fatalf("open = %d, want 0 — a refused figure carries no number", out.OpenMinorBase)
	}
}

// The oldest overdue age is a MAXIMUM over the overdue set, so it needs more
// than one overdue invoice to be proven at all.
func TestTheOldestOverdueAgeIsTheLongestOutstandingOne(t *testing.T) {
	asOf := on(t, "2026-08-09")
	out := OpenBalanceAt([]Invoice{
		{Status: "open", OpenMinorBase: 100000, DueOn: ptr(on(t, "2026-07-21"))},
		{Status: "open", OpenMinorBase: 100000, DueOn: ptr(on(t, "2026-03-01"))},
		{Status: "open", OpenMinorBase: 100000, DueOn: ptr(on(t, "2026-08-01"))},
	}, asOf)

	if out.OverdueCount != 3 {
		t.Fatalf("overdue count = %d, want 3", out.OverdueCount)
	}
	if out.OldestOverdueDays != 161 {
		t.Fatalf("oldest overdue = %d days, want 161 (the March invoice, not the July one)",
			out.OldestOverdueDays)
	}
}

// The half-away-from-zero rule has two directions, and a naive integer divide
// rounds the negative one the wrong way.
func TestAnEvenSampleOfEarlyPaymentsRoundsAwayFromZero(t *testing.T) {
	asOf := on(t, "2026-08-09")
	settle := func(daysLate int) Invoice {
		due := on(t, "2026-06-01")
		return Invoice{DueOn: ptr(due), FullyPaidAt: ptr(due.AddDate(0, 0, daysLate))}
	}
	out := TimelinessOver([]Invoice{
		settle(-9), settle(-6), settle(-4), settle(-3), settle(-2), settle(-1),
	}, asOf)

	// Middle two are −4 and −3; their mean is −3.5, which rounds to −4.
	if out.MedianDaysLate != -4 {
		t.Fatalf("median = %d, want -4 (−3.5 rounds away from zero)", out.MedianDaysLate)
	}
}

func TestAnEvenSampleTakesTheMeanOfTheTwoMiddleValues(t *testing.T) {
	asOf := on(t, "2026-08-09")
	settle := func(daysLate int) Invoice {
		due := on(t, "2026-05-01")
		return Invoice{DueOn: ptr(due), FullyPaidAt: ptr(due.AddDate(0, 0, daysLate))}
	}
	out := TimelinessOver([]Invoice{
		settle(0), settle(2), settle(3), settle(4), settle(10), settle(20),
	}, asOf)

	// Middle two are 3 and 4; rounded half away from zero that is 4.
	if out.MedianDaysLate != 4 {
		t.Fatalf("median = %d, want 4", out.MedianDaysLate)
	}
}
