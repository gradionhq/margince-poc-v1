// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The generator's two obligations, and the hash rule that makes the sync
// idempotent.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func ledgerFor(t *testing.T, customer string) SourceLedger {
	t.Helper()
	provider := NewOfflineProvider("ws-1", []SourceCustomer{{ExternalID: customer}})
	ledger, err := provider.InvoicesFor(context.Background(), customer)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

// The same customer must produce the same ledger everywhere — on a colleague's
// machine, in CI, and on the second run of the same test. A generator that
// drifted would make every finance screenshot and every assertion a moving
// target.
func TestTheSameCustomerAlwaysGeneratesTheSameLedger(t *testing.T) {
	first := ledgerFor(t, "ACME-01")
	second := ledgerFor(t, "ACME-01")
	if len(first.Invoices) != len(second.Invoices) {
		t.Fatalf("invoice counts differ: %d vs %d", len(first.Invoices), len(second.Invoices))
	}
	for i := range first.Invoices {
		if invoiceHash(first.Invoices[i]) != invoiceHash(second.Invoices[i]) {
			t.Fatalf("invoice %d differs between two generations", i)
		}
	}
}

// Two customers must not share a ledger, or every account on the page would
// show the same figures and the card would prove nothing.
func TestTwoCustomersGetDifferentLedgers(t *testing.T) {
	a := ledgerFor(t, "ACME-01")
	b := ledgerFor(t, "BRANDT-02")
	same := 0
	for i := range a.Invoices {
		if i < len(b.Invoices) && a.Invoices[i].GrossMinor == b.Invoices[i].GrossMinor {
			same++
		}
	}
	if same == len(a.Invoices) {
		t.Fatal("two customers generated identical amounts throughout")
	}
}

// FIN-FORM-3 refuses a median below five settled invoices. A generated
// customer that fell short would demonstrate the refusal rather than the
// figure, which is the opposite of what a demo ledger is for.
func TestEveryGeneratedCustomerClearsTheTimelinessSampleFloor(t *testing.T) {
	for _, customer := range []string{"ACME-01", "BRANDT-02", "VOLTAQ-03", "GLAZED-04"} {
		t.Run(customer, func(t *testing.T) {
			ledger := ledgerFor(t, customer)
			window := offlineEpoch.AddDate(0, 0, -TimelinessWindowDays)
			settled := 0
			for _, inv := range ledger.Invoices {
				if inv.FullyPaidAt != nil && inv.FullyPaidAt.After(window) {
					settled++
				}
			}
			if settled < MinTimelinessSample {
				t.Fatalf("%d invoices settled inside the window, want at least %d",
					settled, MinTimelinessSample)
			}
		})
	}
}

// Every account carries an open balance, because "what do they owe us" is the
// reading the card leads with and a ledger of fully settled invoices would
// never exercise it.
func TestEveryGeneratedCustomerCarriesAnOpenBalance(t *testing.T) {
	ledger := ledgerFor(t, "ACME-01")
	open := int64(0)
	for _, inv := range ledger.Invoices {
		open += inv.GrossMinor - inv.PaidMinor
	}
	if open <= 0 {
		t.Fatal("the generated ledger owes nothing; the open-balance reading has nothing to show")
	}
}

// A ledger with no credit note would never exercise FIN-FORM-1's negative
// term, and a figure that has never been reduced is a figure nobody checked.
func TestTheLedgerCarriesACreditNoteAgainstARealInvoice(t *testing.T) {
	ledger := ledgerFor(t, "ACME-01")
	byID := map[string]bool{}
	for _, inv := range ledger.Invoices {
		byID[inv.ExternalID] = true
	}
	notes := 0
	for _, inv := range ledger.Invoices {
		if inv.CreditsExternalID == "" {
			continue
		}
		notes++
		if !byID[inv.CreditsExternalID] {
			t.Fatalf("credit note %s reduces %s, which is not in the ledger",
				inv.ExternalID, inv.CreditsExternalID)
		}
	}
	if notes == 0 {
		t.Fatal("no credit note; the negative term is never exercised")
	}
}

// THE hash rule. An invoice's status depends on today, so a hash that included
// it would change at midnight and make every morning's pass rewrite an
// untouched ledger — an event per invoice, a version bump per row, and a
// mirror reporting change where none happened.
func TestTheHashIgnoresEverythingDerivedFromToday(t *testing.T) {
	due := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	invoice := SourceInvoice{
		ExternalID: "INV-1", Number: "INV-1",
		IssuedOn: due.AddDate(0, 0, -30), DueOn: &due,
		Currency: "EUR", NetMinor: 100000, GrossMinor: 119000,
	}
	before := invoiceHash(invoice)

	// The same invoice, read on a day when it has become overdue. Its DERIVED
	// status differs; the source said nothing new.
	early := deriveStatus(invoice, invoice.GrossMinor, due.AddDate(0, 0, -1))
	late := deriveStatus(invoice, invoice.GrossMinor, due.AddDate(0, 0, 1))
	if early == late {
		t.Fatal("the status did not change across the due date; this test proves nothing")
	}
	if after := invoiceHash(invoice); after != before {
		t.Fatal("the hash moved with the derived status: every pass would rewrite the ledger")
	}
}

// The generator must not key on the clock either, for the same reason: a
// ledger regenerated tomorrow must hash the same as today's, or the sync
// rewrites everything every day.
func TestTheGeneratorDoesNotMoveWithTheClock(t *testing.T) {
	ledger := ledgerFor(t, "ACME-01")
	for _, inv := range ledger.Invoices {
		if inv.IssuedOn.After(offlineEpoch) {
			t.Fatalf("invoice %s is issued after the fixed epoch, so the ledger tracks the clock",
				inv.ExternalID)
		}
	}
}

// FIN-FORM-1 reads `credited_minor` off the invoice being REDUCED — its term
// is `net - credited`. A credit note that carried its own amount there would
// subtract nothing from anything, and the credit would never reach the figure.
func TestACreditLandsOnTheInvoiceItReducesRatherThanOnTheNote(t *testing.T) {
	ledger := ledgerFor(t, "ACME-01")
	credited, orphans := applyCredits(ledger)
	if len(orphans) != 0 {
		t.Fatalf("generated ledger has %d orphan credit notes", len(orphans))
	}
	if len(credited) == 0 {
		t.Fatal("no credit landed on any invoice")
	}
	for _, inv := range ledger.Invoices {
		if inv.CreditsExternalID == "" {
			continue
		}
		if credited[inv.ExternalID] != 0 {
			t.Fatalf("the credit landed on the note %s rather than on its target",
				inv.ExternalID)
		}
		if credited[inv.CreditsExternalID] != inv.GrossMinor {
			t.Fatalf("target %s carries %d credited, want the note's %d",
				inv.CreditsExternalID, credited[inv.CreditsExternalID], inv.GrossMinor)
		}
	}
}

// The source may list a note before the invoice it reduces. Resolving credits
// over the whole ledger is what makes the outcome independent of that order —
// a per-row pass would leave the note pointing at nothing, and the
// unchanged-hash short circuit would stop a later pass repairing it.
func TestCreditsResolveWhateverOrderTheSourceListsThemIn(t *testing.T) {
	target := SourceInvoice{ExternalID: "INV-1", GrossMinor: 100000}
	note := SourceInvoice{ExternalID: "CN-1", GrossMinor: 25000, CreditsExternalID: "INV-1"}

	noteFirst, _ := applyCredits(SourceLedger{Invoices: []SourceInvoice{note, target}})
	targetFirst, _ := applyCredits(SourceLedger{Invoices: []SourceInvoice{target, note}})
	if noteFirst["INV-1"] != 25000 || targetFirst["INV-1"] != 25000 {
		t.Fatalf("order changed the result: %d vs %d", noteFirst["INV-1"], targetFirst["INV-1"])
	}
}

// A note whose target the source did not send is reported rather than dropped:
// a credit that vanishes overstates what the customer owes.
func TestAnOrphanCreditIsReportedRatherThanDropped(t *testing.T) {
	note := SourceInvoice{ExternalID: "CN-9", GrossMinor: 5000, CreditsExternalID: "INV-GONE"}
	credited, orphans := applyCredits(SourceLedger{Invoices: []SourceInvoice{note}})
	if len(orphans) != 1 || orphans[0] != "CN-9" {
		t.Fatalf("orphans = %v, want [CN-9]", orphans)
	}
	if len(credited) != 0 {
		t.Fatalf("credited = %v, want nothing applied", credited)
	}
}

// A credit note is money going the other way. Left with its gross as an open
// balance it would inflate receivables by the amount it was meant to reduce.
func TestACreditNoteOwesNothing(t *testing.T) {
	note := SourceInvoice{
		ExternalID: "CN-1", GrossMinor: 25000, CreditsExternalID: "INV-1",
	}
	values := deriveValues(note, offlineEpoch, map[string]ids.UUID{}, 0)
	if values.openMinor != 0 {
		t.Fatalf("the credit note owes %d, want 0", values.openMinor)
	}
	if values.status != statusCredited {
		t.Fatalf("status = %q, want %q", values.status, statusCredited)
	}
}
