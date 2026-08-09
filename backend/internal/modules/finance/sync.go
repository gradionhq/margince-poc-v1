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
// The write shape is the house one: the mirrored row, its audit row and its
// outbox event in one transaction, `captured_by` from the authenticated
// principal. A sync runs as the system principal, which is what makes those
// rows say a connector wrote them rather than a person.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
}

// SyncConnection runs one pass of one connection.
//
// No auth gate, and ratified as such in `ungatedEntryPoints`: this is the
// scheduled sweep's own path, run under the worker's system principal with no
// request and no human actor. The accounting source is the authority for what
// it says, and there is no object grant a schedule could hold.
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
		if err := mirrorCustomers(ctx, tx, conn.id, customers); err != nil {
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
			if err := s.mirrorLedger(ctx, tx, conn.id, link, ledger, &out); err != nil {
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
	ctx context.Context, tx pgx.Tx, connectionID ids.UUID, customers []SourceCustomer,
) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	for _, customer := range customers {
		hash := hashOf(customer.ExternalID, customer.DisplayName)
		_, err := tx.Exec(ctx, `
			INSERT INTO finance_external_customer
			       (workspace_id, connection_id, external_customer_id, display_name,
			        source_updated_at, sync_hash, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace_id, connection_id, external_customer_id)
			DO UPDATE SET display_name = EXCLUDED.display_name,
			              source_updated_at = EXCLUDED.source_updated_at,
			              sync_hash = EXCLUDED.sync_hash
			      WHERE finance_external_customer.sync_hash <> EXCLUDED.sync_hash`,
			storekit.MustWorkspace(ctx), connectionID, customer.ExternalID,
			customer.DisplayName, customer.UpdatedAt, hash, OfflineProviderName, by)
		if err != nil {
			return fmt.Errorf("mirror the source's customer: %w", err)
		}
	}
	return nil
}

// markSynced records that the pass finished. `last_success_at` is what the
// card's staleness is measured from, so it moves only when the pass actually
// completed — a failed attempt leaves it where it was and the reader keeps the
// date of the last figure they can trust.
func markSynced(ctx context.Context, tx pgx.Tx, connectionID ids.UUID) error {
	// The connection row is held for the same reason the invoice rows are: two
	// sweeps finishing at once must not interleave their status writes and
	// leave the row saying `error` after the later one succeeded.
	if _, err := storekit.LockRow(ctx, tx, "finance_connection", connectionID, storekit.LiveOnly); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE finance_connection
		   SET status = 'active', last_attempt_at = now(), last_success_at = now(),
		       last_error_code = NULL
		 WHERE id = $1`, connectionID)
	if err != nil {
		return fmt.Errorf("record the finished sync: %w", err)
	}
	return nil
}

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
		inv.IssuedOn.UTC().Format(time.RFC3339),
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
		pay.PaidAt.UTC().Format(time.RFC3339), pay.Currency,
		strconv.FormatInt(pay.AmountMinor, 10))
}

func stampOf(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
