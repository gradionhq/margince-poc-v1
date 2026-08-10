// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// Mirroring one received payment, on the same hash rule the invoices use: a
// payment the source has already told us about writes nothing on a second
// pass.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
