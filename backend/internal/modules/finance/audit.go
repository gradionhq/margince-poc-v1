// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The images the mirror's audit rows carry, and the connection's transition
// rule. The `storekit.Audit` calls themselves sit at the six write statements,
// so each one is ratified on its own key in `auditOnlyWrites` — a seventh
// finance write would arrive unratified rather than inheriting a waiver
// somebody wrote about four tables.
//
// Why the audit row at all: the mirror holds MONEY, and an audit row cannot be
// written after the fact. An invoice mirrored without one stays permanently
// unaccounted for, and the erasure and retention reasoning that reads
// audit_log is blind to it. So it commits in the same transaction as the
// domain row, from the same connector principal that stamped `captured_by`.
//
// AUDIT-ONLY, deliberately, and this is the whole rationale — read it before
// adding an emit. The event catalog is CLOSED: an event type exists because a
// contract declares it, and a build may not mint one to satisfy a rule. The
// catalog carries no finance verb at all, and neither of the two types that
// look adjacent fits. `mirror.*` belongs to the overlay write-back stream, so
// staging a mirrored invoice under it would route an accounting fact to
// subscribers watching for something else entirely. `organization.updated`
// would tell every subscriber that a company record changed when none did.
// Publishing under either is worse than publishing nothing: a wrong envelope
// is acted on, an absent one is not.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The mirror's own tables, spelled once. Each is both the row lock's table
// name and the audit row's entity type, and a reader following one to the
// other has to find the same word in both places.
const (
	entityInvoice          = "finance_invoice"
	entityPayment          = "finance_payment"
	entityExternalCustomer = "finance_external_customer"
	entityConnection       = "finance_connection"
)

// invoiceImage is one mirrored invoice as the audit trail carries it.
//
// It covers EVERY column the sync writes, and that is not thoroughness for its
// own sake: the change key is `invoiceHash`, so anything inside the hash can be
// the sole reason a row was rewritten. An image narrower than the hash produces
// audit rows whose before and after differ only in `sync_hash` — a trail saying
// money moved without saying what moved, which is the gap this exists to close.
// A source restating net 1000 → 1200 as a tax correction, gross unchanged, is
// the case that finds it.
//
// `fx_rate_to_base` is here although the hash does not cover it, because the
// repair path rewrites a row on an UNCHANGED hash precisely to fill it in;
// without it that write would record before == after.
//
// One shape for both sides. The before image is read off the row and the after
// is derived for the write, and a reader diffing them has to be comparing like
// with like — two shapes would make an absent field indistinguishable from a
// field that went away.
//
// There is no `paid_minor`: the mirror does not store it. What the source said
// it received arrives as `open_minor`, which is gross less paid, clamped at
// nothing-owed.
type invoiceImage struct {
	OrganizationID ids.OrganizationID `json:"organization_id"`
	Number         *string            `json:"number"`
	IssuedAt       time.Time          `json:"issued_at"`
	DueAt          *time.Time         `json:"due_at"`
	Status         string             `json:"status"`
	Currency       string             `json:"currency"`
	NetMinor       int64              `json:"net_minor"`
	TaxMinor       int64              `json:"tax_minor"`
	GrossMinor     int64              `json:"gross_minor"`
	OpenMinor      int64              `json:"open_minor"`
	CreditedMinor  int64              `json:"credited_minor"`
	FullyPaidAt    *time.Time         `json:"fully_paid_at"`
	DisputedAt     *time.Time         `json:"disputed_at"`
	VoidAt         *time.Time         `json:"void_at"`
	FxRateToBase   *float64           `json:"fx_rate_to_base"`
	SyncHash       string             `json:"sync_hash"`
}

// paymentImage is one mirrored payment as the audit trail carries it,
// including the invoice it settles: a payment reassigned to another invoice is
// money moving between accounts, and the before image is where that shows.
type paymentImage struct {
	OrganizationID ids.OrganizationID `json:"organization_id"`
	InvoiceID      *ids.UUID          `json:"invoice_id"`
	Currency       string             `json:"currency"`
	AmountMinor    int64              `json:"amount_minor"`
	PaidAt         time.Time          `json:"paid_at"`
	SyncHash       string             `json:"sync_hash"`
}

// externalCustomerImage is one mirrored directory entry. It carries no money;
// what it records is which name in the accounting source a link points at.
type externalCustomerImage struct {
	DisplayName string `json:"display_name"`
	SyncHash    string `json:"sync_hash"`
}

// connectionImage is the connection's reportable state — what the card says
// about whether the figures beside it can be trusted.
//
// `last_error_code` is a plain string and not a pointer, because this struct is
// compared BY VALUE to decide whether anything changed. Two pointers holding
// the same code are not equal, so a pointer field would report a transition on
// every failed pass — the exact noise the comparison exists to suppress.
//
// It carries no `omitempty`, deliberately. On recovery the after image's job is
// to say the error code was CLEARED, and a projection that walks the after
// image's keys cannot record a change to a key that is not there. The empty
// string is the cleared state, written rather than implied.
type connectionImage struct {
	Status    string `json:"status"`
	ErrorCode string `json:"last_error_code"`
}

// auditConnectionTransition records a connection that changed STATE, and
// deliberately records nothing when it did not.
//
// The sweep runs every six hours and rewrites `last_attempt_at` on every pass
// whatever happened. Auditing that would file four rows a day per connection
// saying nothing changed, and the transitions this exists to record — the
// source went down, the source came back — would be buried in them. The
// timestamps are a heartbeat; the status and the error code are the facts.
func auditConnectionTransition(
	ctx context.Context, tx pgx.Tx, id ids.UUID, before, after connectionImage,
) error {
	if before == after {
		return nil
	}
	if _, err := storekit.Audit(ctx, tx, "update", entityConnection, id, before, after); err != nil {
		return fmt.Errorf("record the connection's change of state: %w", err)
	}
	return nil
}

// readConnectionState reads the state a status write is about to change, under
// the row lock the caller already holds. Read rather than derived from what
// the sweep believed at the start of the pass: another writer may have moved
// the row since, and a before image that was never true is worse than none.
func readConnectionState(
	ctx context.Context, tx pgx.Tx, id ids.UUID,
) (connectionImage, error) {
	var (
		out  connectionImage
		code *string
	)
	if err := tx.QueryRow(ctx,
		`SELECT status, last_error_code FROM finance_connection WHERE id = $1`,
		id).Scan(&out.Status, &code); err != nil {
		return connectionImage{}, fmt.Errorf("read the connection's state: %w", err)
	}
	if code != nil {
		out.ErrorCode = *code
	}
	return out, nil
}
