// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// Writing one customer's ledger into the mirror.
//
// Status is DERIVED here rather than taken from the provider, and derived from
// fields that do not move: an invoice with an open balance past its due date
// is overdue, and it becomes overdue at midnight without the source touching
// it. Deriving it on write and re-deriving on the next pass is what lets the
// hash cover only the source's own values, so an unchanged ledger writes
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// mirrorLedger writes one customer's invoices and payments.
func (s *Store) mirrorLedger(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, mapped link,
	ledger SourceLedger, out *SyncResult,
) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	// Credits are resolved over the WHOLE ledger before anything is written, so
	// a note that arrives before its target still reduces it. Resolving them
	// per row made the outcome depend on the source's ordering, and the
	// unchanged-hash short circuit would have stopped a later pass repairing
	// it.
	credited, orphans := applyCredits(ledger)
	out.OrphanCredits += len(orphans)

	// External id → row id, so a payment can name the invoice it settles and a
	// credit note the invoice it reduces. Both are source-side references,
	// resolved here rather than stored as strings.
	//
	// Two passes: ids first, then the writes. A single pass could not point a
	// note at a target it had not reached yet.
	rowIDs, err := existingRowIDs(ctx, tx, connectionID, ledger)
	if err != nil {
		return err
	}
	for _, invoice := range ledger.Invoices {
		out.InvoicesSeen++
		id, outcome, err := s.mirrorInvoice(ctx, tx, mirrorArgs{
			connectionID: connectionID, organizationID: mapped.organizationID,
			invoice: invoice, capturedBy: by, rowIDs: rowIDs,
			creditedAgainst: credited[invoice.ExternalID],
		})
		if err != nil {
			return err
		}
		rowIDs[invoice.ExternalID] = id
		countOutcome(outcome, &out.InvoicesInsert, &out.InvoicesUpdate, &out.Unchanged)
	}
	for _, payment := range ledger.Payments {
		out.PaymentsSeen++
		outcome, err := s.mirrorPayment(ctx, tx, paymentArgs{
			connectionID: connectionID, organizationID: mapped.organizationID,
			payment: payment, capturedBy: by, rowIDs: rowIDs,
		})
		if err != nil {
			return err
		}
		countOutcome(outcome, &out.PaymentsWrite, &out.PaymentsWrite, &out.Unchanged)
	}
	return nil
}

func countOutcome(outcome writeOutcome, inserted, updated, unchanged *int) {
	switch outcome {
	case wroteInsert:
		*inserted++
	case wroteUpdate:
		*updated++
	case wroteNothing:
		*unchanged++
	}
}

// existingRowIDs pre-resolves the ids of every invoice in this ledger that the
// mirror already holds, so a credit note or a payment can point at its target
// whatever order the source listed them in. An invoice not yet mirrored is
// absent and gets its id when it is written.
func existingRowIDs(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, ledger SourceLedger,
) (map[string]ids.UUID, error) {
	externals := make([]string, 0, len(ledger.Invoices))
	for _, inv := range ledger.Invoices {
		externals = append(externals, inv.ExternalID)
	}
	rows, err := tx.Query(ctx, `
		SELECT external_id, id FROM finance_invoice
		 WHERE connection_id = $1 AND external_id = ANY($2)`, connectionID, externals)
	if err != nil {
		return nil, fmt.Errorf("read the mirrored invoice ids: %w", err)
	}
	defer rows.Close()
	out := map[string]ids.UUID{}
	for rows.Next() {
		var (
			external string
			id       ids.UUID
		)
		if err := rows.Scan(&external, &id); err != nil {
			return nil, fmt.Errorf("scan a mirrored invoice id: %w", err)
		}
		out[external] = id
	}
	return out, rows.Err()
}

// writeOutcome says what one row's upsert did, so a pass can report whether an
// unchanged source really wrote nothing.
type writeOutcome int

const (
	wroteNothing writeOutcome = iota
	wroteInsert
	wroteUpdate
)

type mirrorArgs struct {
	connectionID   ids.UUID
	organizationID ids.OrganizationID
	invoice        SourceInvoice
	capturedBy     string
	rowIDs         map[string]ids.UUID
	// creditedAgainst is what credit notes elsewhere in this ledger reduce
	// THIS invoice by. Resolved over the whole ledger before any write, so a
	// note that arrives before its target still lands.
	creditedAgainst int64
}

// mirrorInvoice upserts one invoice, writing only when the SOURCE's own values
// changed.
func (s *Store) mirrorInvoice(
	ctx context.Context, tx pgx.Tx, args mirrorArgs,
) (ids.UUID, writeOutcome, error) {
	inv := args.invoice
	hash := invoiceHash(inv)
	existingID, existingHash, found, err := findInvoice(ctx, tx, args.connectionID, inv.ExternalID)
	if err != nil {
		return ids.UUID{}, wroteNothing, err
	}
	if found && existingHash == hash {
		// The source says exactly what it said last time. Rewriting the row
		// would bump its version, write an audit row and emit an event for a
		// change that did not happen.
		return existingID, wroteNothing, nil
	}
	values := deriveValues(inv, s.now(), args.rowIDs, args.creditedAgainst)
	if found {
		return existingID, wroteUpdate, updateInvoice(ctx, tx, existingID, args, values, hash)
	}
	id := ids.NewV7()
	return id, wroteInsert, insertInvoice(ctx, tx, id, args, values, hash)
}

func findInvoice(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, externalID string,
) (id ids.UUID, hash string, found bool, err error) {
	// FOR UPDATE, because this read starts a read-modify-write: two sweeps
	// racing on the same invoice would otherwise both see the old hash, both
	// decide it changed, and both write. The row is held to commit.
	err = tx.QueryRow(ctx, `
		SELECT id, sync_hash FROM finance_invoice
		 WHERE connection_id = $1 AND external_id = $2
		   FOR UPDATE`,
		connectionID, externalID).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, "", false, nil
	}
	if err != nil {
		return ids.UUID{}, "", false, fmt.Errorf("read the mirrored invoice: %w", err)
	}
	return id, hash, true, nil
}

// invoiceValues are the columns derived from one source invoice — everything
// the mirror computes rather than mirrors.
type invoiceValues struct {
	status     string
	openMinor  int64
	credited   int64
	creditsID  *ids.UUID
	disputedAt *time.Time
	voidAt     *time.Time
}

func deriveValues(
	inv SourceInvoice, now time.Time, rowIDs map[string]ids.UUID, creditedAgainst int64,
) invoiceValues {
	open := inv.GrossMinor - inv.PaidMinor
	if open < 0 {
		// Overpaid. Nothing owed rather than a negative balance, which would
		// read as us owing them.
		open = 0
	}
	if inv.CreditsExternalID != "" {
		// A credit note is money going the other way. Left with its gross as
		// an open balance it would inflate receivables by the amount it was
		// supposed to reduce them by.
		open = 0
	}
	out := invoiceValues{openMinor: open, credited: creditedAgainst}
	out.status = deriveStatus(inv, open, now)
	if inv.CreditsExternalID != "" {
		// A credit note whose target is not in this pass keeps its amount and
		// loses only the pointer: dropping the row would lose real money from
		// the total, which is worse than losing a link between two rows.
		if target, ok := rowIDs[inv.CreditsExternalID]; ok {
			out.creditsID = &target
		}
	}
	if inv.Disputed {
		out.disputedAt = &inv.IssuedOn
	}
	if inv.Void {
		out.voidAt = &inv.IssuedOn
	}
	return out
}

func insertInvoice(
	ctx context.Context, tx pgx.Tx, id ids.UUID, args mirrorArgs,
	values invoiceValues, hash string,
) error {
	inv := args.invoice
	_, err := tx.Exec(ctx, `
		INSERT INTO finance_invoice
		       (id, workspace_id, connection_id, organization_id, external_id, number,
		        issued_at, due_at, status, currency, net_minor, tax_minor, gross_minor,
		        open_minor, credited_minor, fully_paid_at, disputed_at, void_at,
		        credits_invoice_id, source_updated_at, sync_hash, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
		        $16, $17, $18, $19, $20, $21, $22, $23)`,
		id, storekit.MustWorkspace(ctx), args.connectionID, args.organizationID,
		inv.ExternalID, nullable(inv.Number), inv.IssuedOn, inv.DueOn, values.status,
		inv.Currency, inv.NetMinor, inv.TaxMinor, inv.GrossMinor, values.openMinor,
		values.credited, inv.FullyPaidAt, values.disputedAt, values.voidAt,
		values.creditsID, inv.UpdatedAt, hash, OfflineProviderName, args.capturedBy)
	if err != nil {
		return fmt.Errorf("mirror the invoice: %w", err)
	}
	return nil
}

// updateInvoice rewrites EVERY hashed field, not only the derived ones.
//
// The hash is what decided this row changed, so anything inside it must be
// written — an update that refreshed `open_minor` from a corrected gross while
// leaving the old gross stored would leave the mirror internally inconsistent
// and the hash claiming it was current.
//
// `organization_id` is written too: remapping an accounting customer onto a
// different company must move its invoices, or the money stays on the account
// the live link no longer names.
func updateInvoice(
	ctx context.Context, tx pgx.Tx, id ids.UUID, args mirrorArgs,
	values invoiceValues, hash string,
) error {
	// The row is already held by findInvoice's FOR UPDATE, which is where this
	// read-modify-write begins. Taken again here so the guard travels with the
	// statement it protects rather than depending on a caller three frames up
	// remembering to take it.
	if _, err := storekit.LockRow(ctx, tx, "finance_invoice", id, storekit.IncludeArchived); err != nil {
		return err
	}
	inv := args.invoice
	_, err := tx.Exec(ctx, `
		UPDATE finance_invoice
		   SET organization_id = $2, number = $3, issued_at = $4, due_at = $5,
		       status = $6, currency = $7, net_minor = $8, tax_minor = $9,
		       gross_minor = $10, open_minor = $11, credited_minor = $12,
		       fully_paid_at = $13, disputed_at = $14, void_at = $15,
		       credits_invoice_id = $16, source_updated_at = $17, sync_hash = $18
		 WHERE id = $1`,
		id, args.organizationID, nullable(inv.Number), inv.IssuedOn, inv.DueOn,
		values.status, inv.Currency, inv.NetMinor, inv.TaxMinor, inv.GrossMinor,
		values.openMinor, values.credited, inv.FullyPaidAt, values.disputedAt,
		values.voidAt, values.creditsID, inv.UpdatedAt, hash)
	if err != nil {
		return fmt.Errorf("update the mirrored invoice: %w", err)
	}
	return nil
}

// applyCredits folds every credit note in this ledger onto the invoice it
// reduces.
//
// FIN-FORM-1's term is `net - credited`, read off the invoice being REDUCED.
// So the amount belongs on the target, never on the note: a note that carried
// its own amount there would subtract nothing from anything and the credit
// would never reach the figure.
//
// It runs over the WHOLE ledger before anything is written, which fixes the
// ordering problem a per-row pass has: a note that arrives before its target
// has no row to point at yet, and the unchanged-hash short circuit would stop
// a later pass from repairing it. Resolving the pairs up front means order
// never matters.
//
// A note whose target is not in the ledger keeps its amount and finds no home:
// it is reported so the caller can decide, rather than silently dropped, since
// a credit that vanishes overstates what the customer owes.
func applyCredits(ledger SourceLedger) (map[string]int64, []string) {
	credited := map[string]int64{}
	var orphans []string
	known := map[string]bool{}
	for _, inv := range ledger.Invoices {
		known[inv.ExternalID] = true
	}
	for _, inv := range ledger.Invoices {
		if inv.CreditsExternalID == "" {
			continue
		}
		if !known[inv.CreditsExternalID] {
			orphans = append(orphans, inv.ExternalID)
			continue
		}
		credited[inv.CreditsExternalID] += inv.GrossMinor
	}
	return credited, orphans
}

// The mirrored invoice's status vocabulary, matching the column's CHECK and
// the contract's enum. Named rather than repeated: the derivation below and
// the CHECK have to agree, and two spellings of one word is how they stop.
const (
	statusVoid          = "void"
	statusCredited      = "credited"
	statusDisputed      = "disputed"
	statusPaid          = "paid"
	statusOverdue       = "overdue"
	statusPartiallyPaid = "partially_paid"
	statusOpen          = "open"
)

// deriveStatus computes what the invoice IS today, from values that do not
// move. Never hashed and never taken from the provider: "overdue" changes at
// midnight, and a stored-and-hashed status would rewrite the ledger every
// morning.
func deriveStatus(inv SourceInvoice, open int64, now time.Time) string {
	switch {
	case inv.Void:
		return statusVoid
	case inv.CreditsExternalID != "":
		return statusCredited
	case inv.Disputed:
		return statusDisputed
	case open == 0 && inv.FullyPaidAt != nil:
		return statusPaid
	case overdue(inv, now):
		return statusOverdue
	case inv.PaidMinor > 0:
		return statusPartiallyPaid
	default:
		return statusOpen
	}
}

func overdue(inv SourceInvoice, now time.Time) bool {
	return inv.DueOn != nil && now.After(*inv.DueOn)
}

type paymentArgs struct {
	connectionID   ids.UUID
	organizationID ids.OrganizationID
	payment        SourcePayment
	capturedBy     string
	rowIDs         map[string]ids.UUID
}

// mirrorPayment upserts one received payment, on the same hash rule.
func (s *Store) mirrorPayment(
	ctx context.Context, tx pgx.Tx, args paymentArgs,
) (writeOutcome, error) {
	pay := args.payment
	hash := paymentHash(pay)
	var (
		existingID   ids.UUID
		existingHash string
	)
	// FOR UPDATE for the reason findInvoice takes it: this read is the first
	// half of a read-modify-write, and two sweeps must not both write.
	err := tx.QueryRow(ctx, `
		SELECT id, sync_hash FROM finance_payment
		 WHERE connection_id = $1 AND external_id = $2
		   FOR UPDATE`,
		args.connectionID, pay.ExternalID).Scan(&existingID, &existingHash)
	switch {
	case err == nil && existingHash == hash:
		return wroteNothing, nil
	case err == nil:
		// Every hashed field, for the reason updateInvoice writes them all: a
		// payment reassigned to a different invoice, or restated in another
		// currency, changed the hash and must change the row.
		if _, err := tx.Exec(ctx, `
			UPDATE finance_payment
			   SET organization_id = $2, invoice_id = $3, paid_at = $4,
			       currency = $5, amount_minor = $6, source_updated_at = $7,
			       sync_hash = $8
			 WHERE id = $1`,
			existingID, args.organizationID, resolveInvoice(pay, args.rowIDs),
			pay.PaidAt, pay.Currency, pay.AmountMinor, pay.UpdatedAt, hash); err != nil {
			return wroteNothing, fmt.Errorf("update the mirrored payment: %w", err)
		}
		return wroteUpdate, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return wroteNothing, fmt.Errorf("read the mirrored payment: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO finance_payment
		       (workspace_id, connection_id, organization_id, external_id, invoice_id,
		        paid_at, currency, amount_minor, source_updated_at, sync_hash,
		        source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		storekit.MustWorkspace(ctx), args.connectionID, args.organizationID,
		pay.ExternalID, resolveInvoice(pay, args.rowIDs), pay.PaidAt, pay.Currency, pay.AmountMinor,
		pay.UpdatedAt, hash, OfflineProviderName, args.capturedBy)
	if err != nil {
		return wroteNothing, fmt.Errorf("mirror the payment: %w", err)
	}
	return wroteInsert, nil
}

// resolveInvoice answers the mirrored row a payment settles.
//
// A payment the source has not applied to a specific invoice stays unapplied
// rather than being guessed onto the oldest open one — an on-account credit is
// a real state, and attributing it would move money onto an invoice the source
// never named.
func resolveInvoice(pay SourcePayment, rowIDs map[string]ids.UUID) *ids.UUID {
	if pay.InvoiceExternalID == "" {
		return nil
	}
	if target, ok := rowIDs[pay.InvoiceExternalID]; ok {
		return &target
	}
	return nil
}

// nullable turns an empty source string into a NULL column: a blank invoice
// number is the absence of one, not a number that is the empty string.
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
