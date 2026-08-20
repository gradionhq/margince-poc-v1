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
	// source is the provider's own name, stamped on every row it produced.
	source string
	rowIDs map[string]ids.UUID
}

// mirrorPayment upserts one received payment, on the same hash rule.
func (s *Store) mirrorPayment(
	ctx context.Context, tx pgx.Tx, args paymentArgs,
) (writeOutcome, error) {
	hash := paymentHash(args.payment)
	existingID, before, found, err := findPayment(ctx, tx, args.connectionID, args.payment.ExternalID)
	if err != nil {
		return wroteNothing, err
	}
	if found && before.SyncHash == hash {
		return wroteNothing, nil
	}
	if found {
		return wroteUpdate, updatePayment(ctx, tx, existingID, args, hash, before)
	}
	return wroteInsert, insertPayment(ctx, tx, args, hash)
}

// findPayment reads what the mirror already holds for one source payment, and
// holds the row for the write that follows.
func findPayment(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, externalID string,
) (id ids.UUID, image paymentImage, found bool, err error) {
	// FOR UPDATE for the reason findInvoice takes it: this read is the first
	// half of a read-modify-write, and two sweeps must not both write.
	err = tx.QueryRow(ctx, `
		SELECT id, sync_hash, invoice_id, currency, amount_minor, paid_at
		  FROM finance_payment
		 WHERE connection_id = $1 AND external_id = $2
		   FOR UPDATE`,
		connectionID, externalID).Scan(&id, &image.SyncHash, &image.InvoiceID,
		&image.Currency, &image.AmountMinor, &image.PaidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, paymentImage{}, false, nil
	}
	if err != nil {
		return ids.UUID{}, paymentImage{}, false, fmt.Errorf("read the mirrored payment: %w", err)
	}
	return id, image, true, nil
}

// updatePayment rewrites every hashed field, for the reason updateInvoice
// writes them all: a payment reassigned to a different invoice, or restated in
// another currency, changed the hash and must change the row.
func updatePayment(
	ctx context.Context, tx pgx.Tx, id ids.UUID, args paymentArgs,
	hash string, before paymentImage,
) error {
	// The row is already held by findPayment's FOR UPDATE, which is where this
	// read-modify-write begins. Taken again here so the guard travels with the
	// statement it protects rather than depending on a caller remembering to
	// take it — the same reason updateInvoice takes it twice.
	if _, err := storekit.LockRow(ctx, tx, entityPayment, id, storekit.IncludeArchived); err != nil {
		return err
	}
	after := paymentImageOf(args, hash)
	if _, err := tx.Exec(ctx, `
		UPDATE finance_payment
		   SET organization_id = $2, invoice_id = $3, paid_at = $4,
		       currency = $5, amount_minor = $6, source_updated_at = $7,
		       sync_hash = $8
		 WHERE id = $1`,
		id, args.organizationID, after.InvoiceID,
		after.PaidAt, after.Currency, after.AmountMinor, args.payment.UpdatedAt, hash); err != nil {
		return fmt.Errorf("update the mirrored payment: %w", err)
	}
	return auditFinanceUpdate(ctx, tx, entityPayment, id, before, after)
}

func insertPayment(ctx context.Context, tx pgx.Tx, args paymentArgs, hash string) error {
	id := ids.NewV7()
	after := paymentImageOf(args, hash)
	if _, err := tx.Exec(ctx, `
		INSERT INTO finance_payment
		       (id, connection_id, organization_id, external_id, invoice_id,
		        paid_at, currency, amount_minor, source_updated_at, sync_hash,
		        source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, args.connectionID, args.organizationID, args.payment.ExternalID,
		after.InvoiceID, after.PaidAt, after.Currency, after.AmountMinor,
		args.payment.UpdatedAt, hash, args.source, args.capturedBy); err != nil {
		return fmt.Errorf("mirror the payment: %w", err)
	}
	return auditFinanceCreate(ctx, tx, entityPayment, id, after)
}

// paymentImageOf renders what this pass is about to write, in the shape
// findPayment read the previous one in.
func paymentImageOf(args paymentArgs, hash string) paymentImage {
	pay := args.payment
	return paymentImage{
		InvoiceID: resolveInvoice(pay, args.rowIDs), Currency: pay.Currency,
		AmountMinor: pay.AmountMinor, PaidAt: pay.PaidAt, SyncHash: hash,
	}
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
