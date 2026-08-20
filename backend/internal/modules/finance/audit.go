// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The ledger row every mirror write commits beside the row it wrote.
//
// The mirror holds MONEY, and an audit row cannot be written after the fact:
// an invoice mirrored without one stays permanently unaccounted for, and the
// erasure and retention reasoning that reads audit_log is blind to it. So the
// audit row is committed in the same transaction as the domain row, from the
// same connector principal that stamped `captured_by`.
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
//
// The audit row is the half that was missing and the half that cannot wait,
// because it cannot be reconstructed. The event is a product decision about
// which finance types the contract should carry, tracked as its own issue.

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

// auditFinanceCreate records a row this pass brought into the mirror.
//
// The verb is a literal here rather than a parameter because the audit-verb
// gate reads the SOURCE for it: a call that built the verb from a variable
// would leave the DDL's CHECK and the contract in perfect agreement and still
// fail at INSERT. `create` and not `import`: `import` is the bulk-import run's
// verb, and one mirrored row is not an import run.
func auditFinanceCreate[T mirrorImage](
	ctx context.Context, tx pgx.Tx, entityType string, id ids.UUID, after T,
) error {
	if _, err := storekit.Audit(ctx, tx, "create", entityType, id, nil, after); err != nil {
		return fmt.Errorf("record that the mirror took in a %s: %w", entityType, err)
	}
	return nil
}

// auditFinanceUpdate records a mirrored row the source restated, with both
// images so a reader can see what the money was before it moved.
func auditFinanceUpdate[T mirrorImage](
	ctx context.Context, tx pgx.Tx, entityType string, id ids.UUID, before, after T,
) error {
	if _, err := storekit.Audit(ctx, tx, "update", entityType, id, before, after); err != nil {
		return fmt.Errorf("record that the mirror restated a %s: %w", entityType, err)
	}
	return nil
}

// mirrorImage is the closed set of audit images this module writes — one shape
// per table it owns. A constraint rather than a bare `any`, because the set
// really is closed: a fifth shape means a fifth table, which is a schema change
// nobody makes by accident. It also makes auditFinanceUpdate take its two
// images as the SAME type, so a before read off one table can never be recorded
// against an after derived for another.
type mirrorImage interface {
	invoiceImage | paymentImage | externalCustomerImage | connectionImage
}

// invoiceImage is one mirrored invoice as the audit trail carries it: the
// money, the state it is in, and the source hash that decided it changed.
//
// One shape for both sides on purpose. The before image is read off the row
// and the after image is derived for the write, and a reader diffing them has
// to be comparing like with like — two shapes would make an absent field
// indistinguishable from a field that went away.
type invoiceImage struct {
	Number        *string `json:"number"`
	Currency      string  `json:"currency"`
	GrossMinor    int64   `json:"gross_minor"`
	OpenMinor     int64   `json:"open_minor"`
	CreditedMinor int64   `json:"credited_minor"`
	Status        string  `json:"status"`
	SyncHash      string  `json:"sync_hash"`
}

// paymentImage is one mirrored payment as the audit trail carries it,
// including the invoice it settles: a payment reassigned to another invoice is
// money moving between accounts, and the before image is where that shows.
type paymentImage struct {
	InvoiceID   *ids.UUID `json:"invoice_id"`
	Currency    string    `json:"currency"`
	AmountMinor int64     `json:"amount_minor"`
	PaidAt      time.Time `json:"paid_at"`
	SyncHash    string    `json:"sync_hash"`
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
// every failed pass — the exact noise the comparison exists to suppress. The
// column is nullable and "no error" is its empty string here, which `omitempty`
// keeps out of the recorded image rather than writing it as a value.
type connectionImage struct {
	Status    string `json:"status"`
	ErrorCode string `json:"last_error_code,omitempty"`
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
	return auditFinanceUpdate(ctx, tx, entityConnection, id, before, after)
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
