// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The sync pass: read the source, write what changed, and nothing else.
//
// **The hash is over the SOURCE RECORD, never over a derived value.** An
// invoice's status depends on today — an open invoice becomes overdue at
// midnight with nobody touching it — so a hash that included status would make
// every morning's pass rewrite every row, emit an event per invoice, and
// report change where none happened. Status is recomputed on read instead;
// what is hashed is the dates and the amounts, which do not move unless the
// source moved them.
//
// The write shape here is the mirrored row and its audit row in ONE
// transaction, `captured_by` stamped from the connector principal the sweep
// runs as — which is what makes every mirrored row say a connector wrote it
// rather than a person. There is no outbox event, and audit.go carries the
// whole reason: the event catalog is closed and holds no finance type, so the
// mirror publishes nothing until the contract ratifies one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SyncResult reports what one pass did, for the job's log line and the tests.
type SyncResult struct {
	CustomersSeen  int
	InvoicesSeen   int
	InvoicesInsert int
	InvoicesUpdate int
	PaymentsSeen   int
	PaymentsWrite  int
	// Unchanged is the number the whole hash discipline exists to keep high: a
	// second pass over an unchanged source must write nothing.
	Unchanged int
	// OrphanCredits counts credit notes whose target invoice the source did
	// not send. Reported rather than dropped in silence: a credit that
	// vanishes overstates what the customer owes.
	OrphanCredits int
}

// SyncConnection runs one pass of one connection.
//
// No auth gate, and ratified as such in `ungatedEntryPoints`: this is the
// scheduled sweep's own path, run under the worker's connector principal with
// no request and no human actor. The accounting source is the authority for
// what it says, and there is no object grant a schedule could hold.
//
// It resolves the organizations from the LINK table rather than from anything
// the provider says. A provider names its own customers; which company one of
// those is remains a human's decision, and a sync that inferred it would put
// money on the wrong account.
func (s *Store) SyncConnection(
	ctx context.Context, provider Provider,
) (SyncResult, error) {
	var out SyncResult
	customers, err := provider.Customers(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("read the source's customers: %w", err)
	}
	out.CustomersSeen = len(customers)

	err = s.tx(ctx, func(tx pgx.Tx) error {
		conn, connected, err := readConnection(ctx, tx)
		if err != nil {
			return err
		}
		if !connected {
			// Nothing configured. Not an error: a sweep that runs on a
			// workspace with no accounting source has simply nothing to do.
			return nil
		}
		// The provider names itself on every row it produced. Stamping a
		// constant here instead would label a real source's invoices as the
		// offline generator's, and `source` is what a reader asks when the
		// figures disagree with the accounting system.
		source := provider.Name()
		if err := mirrorCustomers(ctx, tx, conn.id, customers, source); err != nil {
			return err
		}
		links, err := readLinks(ctx, tx, conn.id)
		if err != nil {
			return err
		}
		for _, link := range links {
			ledger, err := provider.InvoicesFor(ctx, link.externalCustomerID)
			if err != nil {
				return fmt.Errorf("read the ledger for %s: %w", link.externalCustomerID, err)
			}
			if err := s.mirrorLedger(ctx, tx, conn.id, link, ledger, source, &out); err != nil {
				return err
			}
		}
		return markSynced(ctx, tx, conn.id)
	})
	if err != nil {
		return SyncResult{}, err
	}
	return out, nil
}

// link is one accounting customer's mapping onto an organization.
type link struct {
	organizationID     ids.OrganizationID
	externalCustomerID string
}

func readLinks(ctx context.Context, tx pgx.Tx, connectionID ids.UUID) ([]link, error) {
	rows, err := tx.Query(ctx, `
		SELECT organization_id, external_customer_id
		  FROM finance_customer_link
		 WHERE connection_id = $1 AND archived_at IS NULL
		 ORDER BY external_customer_id`, connectionID)
	if err != nil {
		return nil, fmt.Errorf("read the customer links: %w", err)
	}
	defer rows.Close()
	var out []link
	for rows.Next() {
		var each link
		if err := rows.Scan(&each.organizationID, &each.externalCustomerID); err != nil {
			return nil, fmt.Errorf("scan a customer link: %w", err)
		}
		out = append(out, each)
	}
	return out, rows.Err()
}

// mirrorCustomers keeps the source's own directory current. It is what the
// unmapped state is drawn from: a candidate list cannot be built from a table
// of decisions already made.
func mirrorCustomers(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID,
	customers []SourceCustomer, source string,
) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	for _, customer := range customers {
		if err := mirrorCustomer(ctx, tx, connectionID, customer, source, by); err != nil {
			return err
		}
	}
	return nil
}

// mirrorCustomer writes one directory entry, on the same read-modify-write the
// invoice and payment writers use.
//
// It reads first rather than leaving the outcome to an upsert's own guard,
// because the pass has to KNOW which of the three things it did: an unchanged
// entry writes no row, and a pass that audited it anyway would file history for
// a write that did not happen.
//
// That trades an ON CONFLICT the previous version had, and the trade is
// deliberate. Two sweeps racing on one entry now collide on the unique index,
// which rolls the transaction back and lets the retry write correct history.
// The upsert would not have collided — it would have taken the DO UPDATE arm
// while this pass still believed it was inserting, and filed `create` for an
// update. audit_log is APPEND-ONLY: a rollback is recoverable and a verb is
// not.
func mirrorCustomer(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID,
	customer SourceCustomer, source, by string,
) error {
	hash := hashOf(customer.ExternalID, customer.DisplayName)
	before, id, found, err := findExternalCustomer(ctx, tx, connectionID, customer.ExternalID)
	if err != nil {
		return err
	}
	after := externalCustomerImage{DisplayName: customer.DisplayName, SyncHash: hash}
	switch {
	case found && before == after:
		return nil
	case found:
		return updateExternalCustomer(ctx, tx, id, customer, hash, before, after)
	}
	id = ids.NewV7()
	if _, err := tx.Exec(ctx, `
		INSERT INTO finance_external_customer
		       (id, connection_id, external_customer_id, display_name,
		        source_updated_at, sync_hash, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, connectionID, customer.ExternalID, customer.DisplayName,
		customer.UpdatedAt, hash, source, by); err != nil {
		return fmt.Errorf("mirror the source's customer: %w", err)
	}
	return auditFinanceCreate(ctx, tx, entityExternalCustomer, id, after)
}

// updateExternalCustomer rewrites one directory entry the source restated.
func updateExternalCustomer(
	ctx context.Context, tx pgx.Tx, id ids.UUID, customer SourceCustomer,
	hash string, before, after externalCustomerImage,
) error {
	// The row is already held by findExternalCustomer's FOR UPDATE, which is
	// where this read-modify-write begins. Taken again here so the guard
	// travels with the statement it protects rather than depending on a caller
	// remembering to take it — the same reason updateInvoice takes it twice.
	if _, err := storekit.LockRow(ctx, tx, entityExternalCustomer, id, storekit.IncludeArchived); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE finance_external_customer
		   SET display_name = $2, source_updated_at = $3, sync_hash = $4
		 WHERE id = $1`,
		id, customer.DisplayName, customer.UpdatedAt, hash); err != nil {
		return fmt.Errorf("update the mirrored customer: %w", err)
	}
	return auditFinanceUpdate(ctx, tx, entityExternalCustomer, id, before, after)
}

// findExternalCustomer reads what the mirror already holds for one directory
// entry, holding the row for the write that follows.
func findExternalCustomer(
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, externalID string,
) (image externalCustomerImage, id ids.UUID, found bool, err error) {
	// FOR UPDATE for the reason findInvoice takes it: this read is the first
	// half of a read-modify-write, and two sweeps must not both write.
	err = tx.QueryRow(ctx, `
		SELECT id, display_name, sync_hash FROM finance_external_customer
		 WHERE connection_id = $1 AND external_customer_id = $2
		   FOR UPDATE`,
		connectionID, externalID).Scan(&id, &image.DisplayName, &image.SyncHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return externalCustomerImage{}, ids.UUID{}, false, nil
	}
	if err != nil {
		return externalCustomerImage{}, ids.UUID{}, false,
			fmt.Errorf("read the mirrored customer: %w", err)
	}
	return image, id, true, nil
}

// markSynced records that the pass finished. `last_success_at` is what the
// card's staleness is measured from, so it moves only when the pass actually
// completed — a failed attempt leaves it where it was and the reader keeps the
// date of the last figure they can trust.
func markSynced(ctx context.Context, tx pgx.Tx, connectionID ids.UUID) error {
	// The connection row is held for the same reason the invoice rows are: two
	// sweeps finishing at once must not interleave their status writes and
	// leave the row saying `error` after the later one succeeded.
	if _, err := storekit.LockRow(ctx, tx, entityConnection, connectionID, storekit.LiveOnly); err != nil {
		return err
	}
	before, err := readConnectionState(ctx, tx, connectionID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE finance_connection
		   SET status = $2, last_attempt_at = now(), last_success_at = now(),
		       last_error_code = NULL
		 WHERE id = $1`, connectionID, statusConnectionActive); err != nil {
		return fmt.Errorf("record the finished sync: %w", err)
	}
	return auditConnectionTransition(ctx, tx, connectionID,
		before, connectionImage{Status: statusConnectionActive})
}

// The connection states this file writes, matching the column's CHECK. Named
// rather than repeated: the SQL and the audit image have to agree about which
// word the row now carries, and two spellings of one state is how they stop.
const (
	statusConnectionActive = "active"
	statusConnectionError  = "error"
	// errorCodeSyncFailed is the one code this path writes. The card reads it
	// to say "the last refresh failed" beside figures it still shows.
	errorCodeSyncFailed = "sync_failed"
)

// hashOf is the change key, over the SOURCE's own values only.
//
// Every caller passes the fields the source stated and none that this system
// derived. That is the whole discipline: a hash including a derived status
// would change at midnight and rewrite an untouched ledger every morning.
func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// invoiceHash covers exactly what the source stated about an invoice.
//
// Deliberately absent: status, open_minor, and anything else this system
// computes. Present: the dates and amounts, which move only when the source
// moves them.
func invoiceHash(inv SourceInvoice) string {
	return hashOf(
		inv.ExternalID, inv.Number,
		inv.IssuedOn.UTC().Format(time.RFC3339Nano),
		stampOf(inv.DueOn), inv.Currency,
		strconv.FormatInt(inv.NetMinor, 10),
		strconv.FormatInt(inv.TaxMinor, 10),
		strconv.FormatInt(inv.GrossMinor, 10),
		strconv.FormatInt(inv.PaidMinor, 10),
		stampOf(inv.FullyPaidAt),
		strconv.FormatBool(inv.Disputed), strconv.FormatBool(inv.Void),
		inv.CreditsExternalID,
	)
}

func paymentHash(pay SourcePayment) string {
	return hashOf(pay.ExternalID, pay.InvoiceExternalID,
		pay.PaidAt.UTC().Format(time.RFC3339Nano), pay.Currency,
		strconv.FormatInt(pay.AmountMinor, 10))
}

// RFC3339Nano, not RFC3339: a source that restates a settlement time within
// the same second would otherwise hash identically and the change would be
// missed. The extra precision costs nothing and closes the window.
func stampOf(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// RecordSyncFailure marks the connection as failed, so the card can say "the
// last refresh failed" beside the figures it still shows.
//
// `last_success_at` is deliberately untouched: the reader keeps the date of
// the last figures they can trust, which is the difference between a stale
// card and an empty one.
//
// The original error is returned whatever happens here. A bookkeeping write
// that fails must not replace the failure it was trying to record — the caller
// would then report the wrong reason for the wrong thing.
func (s *Store) RecordSyncFailure(ctx context.Context, cause error) error {
	writeErr := s.tx(ctx, func(tx pgx.Tx) error {
		// The SAME connection the pass ran against, resolved the same way the
		// pass resolved it. The statement used to carry no id predicate at all
		// and marked every live connection failed — harmless only while an
		// installation holds one, and wrong on the day it holds two: a source
		// answering perfectly well would be reported broken, and the audit row
		// would say so on that source's own history.
		conn, connected, err := readConnection(ctx, tx)
		if err != nil {
			return err
		}
		if !connected {
			// Nothing configured. The sync failed for a reason that is not
			// this connection's, and there is no row to record it on.
			return nil
		}
		return markSyncFailed(ctx, tx, conn.id)
	})
	if writeErr != nil {
		return fmt.Errorf("%w (and recording the failure also failed: %w)", cause, writeErr)
	}
	return cause
}

// markSyncFailed puts one connection into the error state.
//
// `last_success_at` is untouched here, which is the whole point of the state:
// the reader keeps the date of the last figures they can trust.
func markSyncFailed(ctx context.Context, tx pgx.Tx, connectionID ids.UUID) error {
	if _, err := storekit.LockRow(ctx, tx, entityConnection, connectionID, storekit.LiveOnly); err != nil {
		return err
	}
	before, err := readConnectionState(ctx, tx, connectionID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE finance_connection
		   SET status = $2, last_attempt_at = now(), last_error_code = $3
		 WHERE id = $1`, connectionID, statusConnectionError, errorCodeSyncFailed); err != nil {
		return fmt.Errorf("record the failed sync: %w", err)
	}
	return auditConnectionTransition(ctx, tx, connectionID, before,
		connectionImage{Status: statusConnectionError, ErrorCode: errorCodeSyncFailed})
}
