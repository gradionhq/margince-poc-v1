// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The five figures the company page reports about how a customer pays
// (finance-ingestion.md FIN-FORM-1..5), as pure functions over mirrored rows.
//
// They are pure so the arithmetic can be proven without a database, because
// what is at stake here is not SQL but honesty: which rows may enter a figure,
// what a figure means when too few rows qualify, and when the answer is "not
// enough to say" rather than a number. One unusually late invoice must not
// become a "bad payer" label.

import (
	"sort"
	"time"
)

// Window parameters (FIN-PARAM-1..3). Named rather than inlined because each
// figure reports the window it used, and a figure whose stated window differs
// from the one it applied is worse than no figure.
const (
	// IssuedWindowDays is FIN-PARAM-1: the trailing window for net invoiced.
	IssuedWindowDays = 365
	// TimelinessWindowDays is FIN-PARAM-2, deliberately shorter than the
	// issued window — how a customer pays NOW matters more than last year.
	TimelinessWindowDays = 180
	// MinTimelinessSample is FIN-PARAM-3. Below it every timeliness figure
	// answers insufficient-sample instead of a number: five settled invoices
	// is the floor at which a pattern is a pattern rather than an anecdote.
	MinTimelinessSample = 5
)

// Invoice is one mirrored invoice, reduced to what the formulas read.
type Invoice struct {
	Status string
	// IssuedOn keys FIN-FORM-1's window — never a service or recognition
	// period, because the issue date is the only field every provider carries.
	IssuedOn time.Time
	DueOn    *time.Time
	// FullyPaidAt is set only when the invoice is completely settled. A
	// partially paid invoice has no settlement date and therefore no
	// punctuality (FIN-FORM-3).
	FullyPaidAt *time.Time
	// NetMinorBase and CreditedMinorBase are converted at the invoice's OWN
	// frozen rate (FIN-PARAM-7).
	NetMinorBase      int64
	CreditedMinorBase int64
	OpenMinorBase     int64
	// CreditsInvoice marks this row as a credit note (FIN-DDL-N-3). It is
	// excluded from FIN-FORM-1's positive term and reaches the figure only
	// through the credited total of the invoice it reduces — counted in both,
	// a credit is subtracted twice; counted in neither, it disappears.
	CreditsInvoice bool
	// Disputed excludes the invoice from timeliness: the delay is a
	// disagreement, not a payment habit. It still counts as open and owed.
	Disputed bool
	// RateMissing marks an invoice whose issue date had no effective
	// conversion rate. One of these refuses the WHOLE figure rather than
	// converting some rows and not others (FIN-AC-6).
	RateMissing bool
}

// NetInvoiced is FIN-FORM-1: what was issued over the trailing window, less
// what was credited back.
//
// Refuses rather than converting part of the set: a total assembled from the
// rows that happened to have a rate is a smaller number presented as the whole
// one, and nothing on the surface would say so.
type NetInvoiced struct {
	AmountMinorBase int64
	// Records counts ISSUED invoices, never credit notes — a credit note is
	// not a fourth invoice, it is a reduction of one of the three.
	Records    int
	WindowDays int
	// RateUnavailable reports the FIN-AC-6 refusal. When true the amount is
	// not a figure and must not be rendered.
	RateUnavailable bool
}

// NetInvoicedOver folds the invoices issued within the window ending at asOf.
func NetInvoicedOver(invoices []Invoice, asOf time.Time) NetInvoiced {
	out := NetInvoiced{WindowDays: IssuedWindowDays}
	from := asOf.AddDate(0, 0, -IssuedWindowDays)
	for _, inv := range invoices {
		if !issuedInWindow(inv, from, asOf) {
			continue
		}
		if inv.RateMissing {
			// One unconvertible row refuses the whole figure. Reporting the
			// rest would be a partial sum wearing the label of a total.
			return NetInvoiced{WindowDays: IssuedWindowDays, RateUnavailable: true}
		}
		if inv.CreditsInvoice {
			// Subtracted exactly once, through the credited total below.
			continue
		}
		out.AmountMinorBase += inv.NetMinorBase - inv.CreditedMinorBase
		out.Records++
	}
	return out
}

// issuedInWindow answers whether one row belongs to FIN-FORM-1's positive
// term. A draft was never issued; a void invoice is excluded entirely rather
// than netted to zero, so the record count stays honest.
func issuedInWindow(inv Invoice, from, to time.Time) bool {
	if inv.Status == "draft" || inv.Status == "void" {
		return false
	}
	return !inv.IssuedOn.Before(from) && !inv.IssuedOn.After(to)
}

// OpenBalance is FIN-FORM-2: what is owed, and how much of it is late.
type OpenBalance struct {
	OpenMinorBase    int64
	OverdueMinorBase int64
	OverdueCount     int
	// RateUnavailable is the same FIN-AC-6 refusal net invoiced makes. An open
	// balance assembled from the invoices that happened to have a rate is a
	// smaller debt presented as the whole one, which is the more dangerous of
	// the two figures to get wrong.
	RateUnavailable bool
	// OldestOverdueDays is the age of the longest-outstanding overdue
	// invoice, which is what tells a rep whether this is a slip or a problem.
	OldestOverdueDays int
}

// OpenBalanceAt folds what is outstanding as of the given instant.
//
// A DISPUTED invoice counts as open, because it is genuinely owed, and is
// reported separately as disputed elsewhere. Presenting it only as overdue
// would describe a disagreement as a payment failure.
func OpenBalanceAt(invoices []Invoice, asOf time.Time) OpenBalance {
	var out OpenBalance
	for _, inv := range invoices {
		if inv.Status == "void" || inv.OpenMinorBase <= 0 {
			continue
		}
		if inv.RateMissing {
			return OpenBalance{RateUnavailable: true}
		}
		out.OpenMinorBase += inv.OpenMinorBase
		if inv.DueOn == nil || !inv.DueOn.Before(asOf) {
			continue
		}
		out.OverdueMinorBase += inv.OpenMinorBase
		out.OverdueCount++
		if age := daysBetween(*inv.DueOn, asOf); age > out.OldestOverdueDays {
			out.OldestOverdueDays = age
		}
	}
	return out
}

// DaysLate is FIN-FORM-3 for one invoice: negative is early, zero is on the
// day, positive is late. The second return is false when the invoice has no
// punctuality to report at all.
//
// The subtraction is between CALENDAR DATES, which is what the formula says
// and what a human means by "eight days late". Measuring elapsed hours instead
// gets it wrong twice: a settlement at 09:00 against a due date at 17:00 is
// the same day and would read as early, and a DST transition inside the span
// shifts the whole answer by one.
func DaysLate(inv Invoice) (int, bool) {
	if inv.Disputed || inv.DueOn == nil || inv.FullyPaidAt == nil {
		return 0, false
	}
	return daysBetween(*inv.DueOn, *inv.FullyPaidAt), true
}

// daysBetween is `date(to) − date(from)`, counted in whole calendar days in
// each timestamp's own location. Both dates are re-anchored to midnight UTC
// first, so the difference is a count of dates rather than of hours, and no
// zone offset or DST shift can move it.
func daysBetween(from, to time.Time) int {
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	fromDay := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	toDay := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(toDay.Sub(fromDay) / (24 * time.Hour))
}

// Timeliness is FIN-FORM-4 and FIN-FORM-5 together: they read the same sample,
// so computing them apart would let the median and the on-time rate disagree
// about which invoices they describe.
type Timeliness struct {
	// InsufficientSample is the answer below FIN-PARAM-3, and it is an answer
	// rather than a missing value: "not enough settled invoices to say" is
	// what stops one late payment becoming a reputation.
	InsufficientSample bool
	MedianDaysLate     int
	// OnTimeRate is a share in [0,1]. Its DENOMINATOR is part of the figure,
	// not a footnote — 100% over two invoices and over sixty are different
	// claims and must never render the same.
	OnTimeRate float64
	SampleSize int
	WindowDays int
}

// TimelinessOver folds the invoices settled inside the timeliness window.
func TimelinessOver(invoices []Invoice, asOf time.Time) Timeliness {
	out := Timeliness{WindowDays: TimelinessWindowDays}
	from := asOf.AddDate(0, 0, -TimelinessWindowDays)
	late := make([]int, 0, len(invoices))
	onTime := 0
	for _, inv := range invoices {
		if inv.FullyPaidAt == nil || inv.FullyPaidAt.Before(from) || inv.FullyPaidAt.After(asOf) {
			continue
		}
		days, ok := DaysLate(inv)
		if !ok {
			continue
		}
		late = append(late, days)
		if days <= 0 {
			onTime++
		}
	}
	out.SampleSize = len(late)
	if out.SampleSize < MinTimelinessSample {
		out.InsufficientSample = true
		return out
	}
	out.MedianDaysLate = medianOf(late)
	out.OnTimeRate = float64(onTime) / float64(out.SampleSize)
	return out
}

// medianOf is the middle value, or the mean of the two middle ones rounded
// half AWAY FROM ZERO. Median rather than mean throughout, deliberately: one
// disputed-then-settled invoice at 180 days would drag a mean into a false
// story about a customer who otherwise pays on time.
func medianOf(values []int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	sum := sorted[mid-1] + sorted[mid]
	if sum < 0 {
		return (sum - 1) / 2
	}
	return (sum + 1) / 2
}
