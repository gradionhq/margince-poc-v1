// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The seam an accounting source is read through.
//
// It exists so the mirror has exactly one shape to ingest, whatever produced
// the rows: the offline generator this repo ships, and whatever real provider
// a fork adds. A provider ANSWERS records; it never writes them, never decides
// what an invoice's status is today, and never sees the database.
//
// The one rule that shapes the whole interface: a provider returns what the
// SOURCE said, and the mirror derives everything else. Status is the sharpest
// case — "overdue" is a function of today, so a provider that returned it
// would produce a different record every morning for an invoice nobody
// touched, and every sync would rewrite rows that did not change.

import (
	"context"
	"time"
)

// Provider reads one accounting source.
type Provider interface {
	// Name is the provider's own identifier, stored on the connection and
	// shown beside the figures so a reader knows what they are looking at.
	Name() string
	// Customers lists the source's own customer directory — what a human maps
	// an organization onto. Never matched automatically: guessing which
	// customer is which company is how money lands on the wrong account.
	Customers(ctx context.Context) ([]SourceCustomer, error)
	// InvoicesFor answers one customer's invoices and the payments against
	// them, as the source holds them.
	InvoicesFor(ctx context.Context, externalCustomerID string) (SourceLedger, error)
}

// SourceCustomer is one entry in the source's customer directory.
type SourceCustomer struct {
	ExternalID  string
	DisplayName string
	// UpdatedAt is the source's own modification stamp when it keeps one. Used
	// for reporting, never for change detection — a source that touches every
	// row nightly would otherwise look like a source that changed every row.
	UpdatedAt *time.Time
}

// SourceLedger is one customer's invoices and payments.
type SourceLedger struct {
	Invoices []SourceInvoice
	Payments []SourcePayment
}

// SourceInvoice is an invoice as the SOURCE states it.
//
// Deliberately absent: `status`. An invoice is overdue relative to today, and
// a provider that computed it would hand the mirror a value that changes
// without the source changing. The mirror derives status on read from the
// fields below, which do not move once the invoice is settled.
type SourceInvoice struct {
	ExternalID string
	Number     string
	IssuedOn   time.Time
	DueOn      *time.Time
	Currency   string
	NetMinor   int64
	TaxMinor   int64
	GrossMinor int64
	// PaidMinor is what the source says has been received against it. The
	// mirror's open balance is derived from this, not stored twice.
	PaidMinor int64
	// FullyPaidAt is set only when the source considers it settled. A
	// partially paid invoice has no settlement date and therefore no
	// punctuality.
	FullyPaidAt *time.Time
	Disputed    bool
	Void        bool
	// CreditsExternalID names the invoice this one reduces, for a credit note.
	// Empty on an ordinary invoice.
	CreditsExternalID string
	UpdatedAt         *time.Time
}

// SourcePayment is one received payment.
type SourcePayment struct {
	ExternalID string
	// InvoiceExternalID is empty for a payment the source has not applied to a
	// specific invoice — an on-account credit. Mirrored as such rather than
	// guessed onto the oldest open invoice.
	InvoiceExternalID string
	PaidAt            time.Time
	Currency          string
	AmountMinor       int64
	UpdatedAt         *time.Time
}
