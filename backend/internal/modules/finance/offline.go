// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The offline provider: plausible accounting data, generated rather than
// fetched, so the finance surfaces can be built and demonstrated before any
// real accounting system is connected (A128 pins an offline fake first).
//
// It is labelled honestly at every level — the connection's provider is
// `offline_demo`, and the card shows that string — because a figure a reader
// cannot tell from a real one is worse than no figure.
//
// **Deterministic, and deterministic in the right way.** The seed is
// sha256(workspace | external customer id), so the same customer produces the
// same ledger on every machine, in every test, forever. What it does NOT
// depend on is the clock: a second sync on a later day must produce the same
// records, or every morning's pass would rewrite every row and the mirror
// would report change where none happened. Dates are generated backwards from
// a fixed epoch rather than from `now` for exactly that reason.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"time"
)

// OfflineProviderName is what the connection and the card call it. A reader
// who sees this string knows the figures are demonstration data.
const OfflineProviderName = "offline_demo"

// offlineEpoch is the fixed point every generated date is measured back from.
//
// NOT time.Now(). A generator keyed on today produces a different ledger every
// day for a customer nobody touched, so every sync would rewrite every row —
// and the sync's whole job is to notice what actually changed.
//
// The cost of that choice is that the ledger ages: the figures are computed
// over windows measured back from NOW, so as the calendar leaves the epoch
// behind, fewer and fewer generated invoices fall inside them. Anything that
// asserts a FIGURE over this ledger therefore reads it at a clock pinned to
// this epoch, or it is measuring how long ago the epoch was.
var offlineEpoch = time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

// OfflineProvider generates one workspace's demonstration ledger.
type OfflineProvider struct {
	workspace string
	// customers is the directory this provider answers. The sync maps them
	// onto organizations by an explicit link, so the names here are the
	// source's own and need not match any company in the CRM.
	customers []SourceCustomer
}

// NewOfflineProvider builds the generator for one workspace's customers.
func NewOfflineProvider(workspace string, customers []SourceCustomer) *OfflineProvider {
	return &OfflineProvider{workspace: workspace, customers: customers}
}

// Name is the string stored on the connection and shown beside the figures,
// so a reader always knows these are demonstration data.
func (p *OfflineProvider) Name() string { return OfflineProviderName }

// Customers answers the directory this generator was built with. A human maps
// an organization onto one of these; nothing here is matched automatically.
func (p *OfflineProvider) Customers(context.Context) ([]SourceCustomer, error) {
	return p.customers, nil
}

// archetype is how a customer pays. Three of them, because the card's whole
// purpose is telling them apart — a generator that produced one behaviour
// would make every account look identical and prove nothing.
type archetype struct {
	name string
	// lateDays is the range of days past due this customer settles in. A
	// negative low end means they sometimes pay early.
	lateLow, lateHigh int
	// disputeRate is how often an invoice is disputed, per thousand.
	disputeRate int
	// openTail is how many of the most recent invoices are still unpaid — the
	// open balance a real account always carries, and the reading the card
	// leads with. It grows with the archetype because a slower payer sitting on
	// more open money is one of the differences the card exists to show.
	openTail int
}

var archetypes = []archetype{
	{name: "prompt", lateLow: -6, lateHigh: 2, disputeRate: 0, openTail: 1},
	{name: "ordinary", lateLow: -2, lateHigh: 12, disputeRate: 30, openTail: 2},
	{name: "slow", lateLow: 5, lateHigh: 38, disputeRate: 40, openTail: 3},
}

// InvoicesFor generates one customer's ledger.
//
// Eighteen months of fortnightly invoices, which leaves about a dozen settled
// inside the timeliness window — comfortably clear of the five FIN-FORM-3
// refuses below, so a generated customer demonstrates the figure rather than
// the refusal.
func (p *OfflineProvider) InvoicesFor(
	_ context.Context, externalCustomerID string,
) (SourceLedger, error) {
	if externalCustomerID == "" {
		return SourceLedger{}, fmt.Errorf("finance: offline provider needs a customer id")
	}
	// #nosec G404 -- a seeded PCG is the requirement, not a shortcut. This
	// generator must produce the same ledger for the same customer on every
	// machine and every run; a cryptographic source would produce a different
	// one each time, and every sync would rewrite the whole mirror. Nothing
	// here is a secret, a token or a key.
	rng := rand.New(rand.NewPCG(seedFor(p.workspace, externalCustomerID)))
	kind := archetypes[rng.IntN(len(archetypes))]
	currency := "EUR"

	var out SourceLedger
	// Oldest first, so the settled ones the timeliness window reads sit at the
	// front and the open tail is genuinely the recent end.
	for period := offlineInvoices - 1; period >= 0; period-- {
		issued := offlineEpoch.AddDate(0, 0, -period*offlineCadenceDays)
		due := issued.AddDate(0, 0, paymentTermDays)
		invoice := SourceInvoice{
			ExternalID: fmt.Sprintf("%s-INV-%04d", externalCustomerID, offlineInvoices-period),
			Number:     fmt.Sprintf("INV-%s-%03d", issued.Format("2006"), offlineInvoices-period),
			IssuedOn:   issued,
			DueOn:      &due,
			Currency:   currency,
		}
		net := int64(rng.IntN(offlineAmountSpread)+offlineAmountFloor) * 100
		invoice.NetMinor = net
		invoice.TaxMinor = net * vatPercent / 100
		invoice.GrossMinor = invoice.NetMinor + invoice.TaxMinor

		if period < kind.openTail {
			// The recent end is still open. This is what gives every account a
			// non-zero open balance, which is the reading the card leads with.
			out.Invoices = append(out.Invoices, invoice)
			continue
		}
		settle(&invoice, kind, rng, due)
		out.Invoices = append(out.Invoices, invoice)
		if invoice.FullyPaidAt != nil {
			out.Payments = append(out.Payments, SourcePayment{
				ExternalID:        invoice.ExternalID + "-PMT",
				InvoiceExternalID: invoice.ExternalID,
				PaidAt:            *invoice.FullyPaidAt,
				Currency:          currency,
				AmountMinor:       invoice.GrossMinor,
			})
		}
	}
	out.Invoices = append(out.Invoices, creditNoteFor(out.Invoices, externalCustomerID, rng))
	return out, nil
}

const (
	// offlineInvoices and offlineCadenceDays are the ledger's depth and its
	// billing rhythm: thirty-nine fortnights is eighteen months of invoices.
	//
	// The CADENCE is what puts a real sample inside the timeliness window, and
	// it is fortnightly rather than monthly for that reason alone. FIN-FORM-3
	// refuses a median below five settled invoices (FIN-PARAM-3) inside the
	// 180-day window (FIN-PARAM-2); monthly billing offers barely six
	// candidates there, so an open tail plus one dispute leaves a generated
	// customer demonstrating the refusal rather than the figure. A fortnight
	// offers about a dozen, which clears the floor with room for both.
	//
	// The DEPTH is what the trailing 365-day figure reads, and eighteen months
	// covers it with a year of history to spare.
	offlineInvoices    = 39
	offlineCadenceDays = 14
	// creditNoteDelayDays is how long after an invoice its credit note is
	// issued. The note is a generated date like any other, so it may not land
	// after the epoch — see creditNoteFor.
	creditNoteDelayDays = 14
	// paymentTermDays is the terms every generated invoice carries. One value
	// rather than a range: the card measures lateness against the due date, so
	// varying the terms would vary the reading without varying the behaviour.
	paymentTermDays     = 30
	vatPercent          = 19
	offlineAmountFloor  = 800
	offlineAmountSpread = 9000
)

// settle marks an invoice paid, the way this customer pays.
func settle(invoice *SourceInvoice, kind archetype, rng *rand.Rand, due time.Time) {
	if rng.IntN(1000) < kind.disputeRate {
		// A dispute is open money with a reason, and it is deliberately
		// excluded from the timeliness reading: the delay is a disagreement,
		// not a payment habit.
		invoice.Disputed = true
		return
	}
	late := kind.lateLow + rng.IntN(kind.lateHigh-kind.lateLow+1)
	paid := due.AddDate(0, 0, late)
	invoice.FullyPaidAt = &paid
	invoice.PaidMinor = invoice.GrossMinor
}

// creditNoteFor issues one credit note against an older invoice, because a
// ledger with none would never exercise FIN-FORM-1's negative term — and a
// figure that has never been reduced is a figure nobody has checked.
//
// The target must be old enough that the note still lands on or before the
// epoch. A note dated after it would be a generated row that moved past the
// fixed point the whole ledger is measured back from — which is the one thing
// this generator promises not to do.
func creditNoteFor(
	invoices []SourceInvoice, customerID string, rng *rand.Rand,
) SourceInvoice {
	// Oldest first (InvoicesFor builds them that way), so the eligible targets
	// are a PREFIX of the ledger — and the oldest invoice, eighteen months
	// back, always qualifies, which is what makes this count at least one.
	eligible := 0
	for _, inv := range invoices {
		if inv.IssuedOn.AddDate(0, 0, creditNoteDelayDays).After(offlineEpoch) {
			break
		}
		eligible++
	}
	target := invoices[rng.IntN(eligible)]
	issued := target.IssuedOn.AddDate(0, 0, creditNoteDelayDays)
	// A partial credit, not a full reversal: a full one is indistinguishable
	// from a void, and the card should show a reduced total rather than a
	// disappeared invoice.
	amount := target.NetMinor / 4
	return SourceInvoice{
		ExternalID:        customerID + "-CN-1",
		Number:            "CN-" + target.Number,
		IssuedOn:          issued,
		Currency:          target.Currency,
		NetMinor:          amount,
		GrossMinor:        amount,
		CreditsExternalID: target.ExternalID,
	}
}

// seedFor derives the generator's two seed words from the workspace and the
// customer, so a customer's ledger is the same on every machine and in every
// test run — and two customers in one workspace never share one.
func seedFor(workspace, customerID string) (uint64, uint64) {
	sum := sha256.Sum256([]byte(workspace + "|" + customerID))
	return binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])
}
