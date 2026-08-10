// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The company page's finance read: does this customer pay us, and on time?
//
// The whole file turns on one distinction. Six things can be true of an
// account's money — no source connected, a source connected but this customer
// unmapped, a first sync still running, current figures, figures from a while
// ago, and a failed refresh — and five of them render as identical blank
// numbers if the state is not carried alongside them. So the state is resolved
// first and the figures are computed only where they mean something.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// recentInvoiceLimit is how many invoices the card carries (FIN-LIM-4). Past a
// handful the reader is scanning a ledger, which is the accounting system's
// job; `truncated` says there are more.
const recentInvoiceLimit = 5

// SummaryFor answers the finance card for one organization.
func (s *Store) SummaryFor(
	ctx context.Context, orgID ids.OrganizationID,
) (crmcontracts.OrganizationFinanceSummary, error) {
	if err := auth.Require(ctx, "finance", principal.ActionRead); err != nil {
		return crmcontracts.OrganizationFinanceSummary{}, err
	}
	out := crmcontracts.OrganizationFinanceSummary{
		OrganizationId: openapi_types.UUID(orgID.UUID),
		State:          crmcontracts.FinanceSummaryStateNoConnection,
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The account itself is row-scoped, and a finance grant is not a
		// licence to learn that an organization exists: an account the caller
		// cannot see answers 404 before any figure is read.
		//
		// EnsureVisible alone is not enough. Under row_scope=all it skips the
		// query entirely — correctly, since everything is visible — so an id
		// naming no organization would come back as a summary of the
		// workspace's provider rather than a 404. The existence probe is what
		// makes a made-up id answer the same way a hidden one does.
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		if err := organizationExists(ctx, tx, orgID); err != nil {
			return err
		}
		conn, connected, err := readConnection(ctx, tx)
		if err != nil {
			return err
		}
		if !connected {
			// No accounting source at all. Not an error and not an empty
			// account — the installation has simply not connected one.
			return nil
		}
		out.Provider = &conn.provider
		out.LastSyncedAt = conn.lastSuccessAt
		linked, err := organizationIsLinked(ctx, tx, orgID, conn.id)
		if err != nil {
			return err
		}
		if !linked {
			// A source is connected but nobody has said which of its customers
			// this is. The fix is a mapping, not a sync, and the card must say
			// so rather than showing an account that owes nothing.
			out.State = crmcontracts.FinanceSummaryStateUnmapped
			return nil
		}
		out.State = connectionState(conn, s.now())
		if out.State == crmcontracts.FinanceSummaryStateSyncing {
			// The first pass has not finished, so any total would be the sum of
			// however much has landed so far — a smaller number wearing the
			// label of the whole one. The state is the answer until it does.
			return nil
		}
		return s.fillFigures(ctx, tx, orgID, conn, &out)
	})
	if err != nil {
		return crmcontracts.OrganizationFinanceSummary{}, err
	}
	return out, nil
}

// connection is the installation's accounting source, reduced to what the
// summary reads.
type connection struct {
	id            ids.UUID
	provider      string
	status        string
	lastSuccessAt *time.Time
}

// readConnection resolves the workspace's live connection, or nil when there
// is none. One installation, one source (A107/ADR-0061), so the newest live
// row is the answer rather than a set.
// `found` is false when the installation has connected no accounting source.
// That is not an error and not a sentinel worth minting — it is the ordinary
// state of an installation that has not been set up yet, and the card's
// `no_connection` renders it.
func readConnection(ctx context.Context, tx pgx.Tx) (conn connection, found bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT id, provider, status, last_success_at
		  FROM finance_connection
		 WHERE archived_at IS NULL AND status <> 'disconnected'
		 ORDER BY created_at DESC
		 LIMIT 1`).Scan(&conn.id, &conn.provider, &conn.status, &conn.lastSuccessAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return connection{}, false, nil
	}
	if err != nil {
		return connection{}, false, fmt.Errorf("read the finance connection: %w", err)
	}
	return conn, true, nil
}

// organizationIsLinked answers whether anyone has mapped an accounting
// customer onto this organization. A link is a deliberate act and never
// inferred from a name match — FIN-AC-7's unmapped state exists because
// guessing which customer is which company is how money lands on the wrong
// account.
// Scoped to the LIVE connection: rows from a source that was disconnected and
// replaced stay in the mirror, and mixing them into the current source's
// totals would report money the connected system has never heard of.
func organizationIsLinked(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, connectionID ids.UUID,
) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM finance_customer_link
			 WHERE organization_id = $1 AND connection_id = $2
			   AND archived_at IS NULL)`, orgID, connectionID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read the customer link: %w", err)
	}
	return exists, nil
}

// organizationExists answers the id that names nobody with the same 404 a
// hidden account gets. Existence-hiding cuts both ways: a caller must not be
// able to tell "no such account" from "not yours".
func organizationExists(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization WHERE id = $1 AND archived_at IS NULL)`,
		orgID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("read the account: %w", err)
	}
	if !exists {
		return apperrors.ErrNotFound
	}
	return nil
}

// connectionState reads the connection's own health into the card's
// vocabulary.
//
// A failed attempt does NOT hide the figures: the last good answer is more
// useful than a blank card, as long as the reader is told it is not current.
// That is the difference between `error` and `no_connection`.
func connectionState(conn connection, now time.Time) crmcontracts.FinanceSummaryState {
	if conn.status == "error" {
		return crmcontracts.FinanceSummaryStateError
	}
	if conn.lastSuccessAt == nil {
		// Connected, never synced: the first pass has not finished, so the
		// figures below are partial rather than final.
		return crmcontracts.FinanceSummaryStateSyncing
	}
	if now.Sub(*conn.lastSuccessAt) > staleAfter {
		return crmcontracts.FinanceSummaryStateStale
	}
	return crmcontracts.FinanceSummaryStateConnected
}

// fillFigures computes the card's readings from the account's own invoices.
//
// Every figure is left ABSENT rather than zero when it cannot be computed
// (FIN-AC-2), and the formulas' own refusals are honoured: one invoice with no
// conversion rate withholds the whole total rather than reporting the sum of
// the rows that happened to convert.
func (s *Store) fillFigures(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, conn connection,
	out *crmcontracts.OrganizationFinanceSummary,
) error {
	invoices, currency, err := readInvoices(ctx, tx, orgID, conn.id)
	if err != nil {
		return err
	}
	if len(invoices) == 0 {
		// A mapped customer with no invoices is a real answer, and it is not
		// "€0 open" — we have simply never billed them. The figures stay
		// absent and the card says the state.
		return nil
	}
	now := s.now()
	if net := NetInvoicedOver(invoices, now); !net.RateUnavailable {
		out.NetInvoiced = money(net.AmountMinorBase, currency)
	}
	if open := OpenBalanceAt(invoices, now); !open.RateUnavailable {
		out.OpenBalance = money(open.OpenMinorBase, currency)
		out.Overdue = money(open.OverdueMinorBase, currency)
	}
	// Below the sample floor the answer is "not enough settled invoices to
	// say" — which is why it is a flag on the formula rather than a zero, and
	// why the figure is left absent here rather than reported as 0 days.
	// Both readings ride the SAME sample floor. Below it the answer is "not
	// enough settled invoices to say", and a sparkline drawn from one invoice
	// is a payment-behaviour claim made from an anecdote — the picture would
	// state what the number refuses to.
	timeliness := TimelinessOver(invoices, now)
	if !timeliness.InsufficientSample {
		median := timeliness.MedianDaysLate
		out.MedianDaysAfterDue = &median
		out.PaymentBehaviour = paymentBehaviour(invoices, now)
	}
	return s.fillRecent(ctx, tx, orgID, conn.id, out)
}

// paymentBehaviour is the sparkline's series: days late per settled invoice,
// oldest first. It rides the same window the median does, so the picture and
// the number cannot describe different months.
//
// Never padded. A zero here reads as "paid exactly on time", so an invoice
// with no due date contributes nothing rather than a false on-time mark.
func paymentBehaviour(invoices []Invoice, asOf time.Time) *[]int {
	from := asOf.AddDate(0, 0, -TimelinessWindowDays)
	series := make([]int, 0, len(invoices))
	for _, inv := range invoices {
		if inv.FullyPaidAt == nil || inv.FullyPaidAt.Before(from) {
			continue
		}
		if late, ok := DaysLate(inv); ok {
			series = append(series, late)
		}
	}
	return &series
}

func money(minor int64, currency string) *crmcontracts.Money {
	amount := minor
	code := currency
	return &crmcontracts.Money{AmountMinor: &amount, Currency: &code}
}

// fillRecent carries the handful of invoices the card lists, and says whether
// there are more. A capped list that stays silent about the cap reads as the
// whole ledger.
func (s *Store) fillRecent(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, connectionID ids.UUID,
	out *crmcontracts.OrganizationFinanceSummary,
) error {
	rows, err := tx.Query(ctx, `
		SELECT id, number, issued_at, due_at, fully_paid_at, status, currency,
		       gross_minor, open_minor
		  FROM finance_invoice
		 WHERE organization_id = $1 AND connection_id = $2 AND archived_at IS NULL
		 ORDER BY issued_at DESC, id DESC
		 LIMIT $3`, orgID, connectionID, recentInvoiceLimit+1)
	if err != nil {
		return fmt.Errorf("read recent invoices: %w", err)
	}
	defer rows.Close()
	recent := make([]crmcontracts.FinanceInvoice, 0, recentInvoiceLimit)
	truncated := false
	for rows.Next() {
		if len(recent) == recentInvoiceLimit {
			// The extra row exists only to answer "are there more", and is
			// never rendered.
			truncated = true
			break
		}
		invoice, scanErr := scanRecentInvoice(rows, s.now())
		if scanErr != nil {
			return scanErr
		}
		recent = append(recent, invoice)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read recent invoices: %w", err)
	}
	out.RecentInvoices = &recent
	out.Truncated = &truncated
	return nil
}

func scanRecentInvoice(rows pgx.Rows, now time.Time) (crmcontracts.FinanceInvoice, error) {
	var (
		id       ids.UUID
		number   *string
		issued   time.Time
		due      *time.Time
		paid     *time.Time
		status   string
		currency string
		gross    int64
		open     int64
	)
	if err := rows.Scan(&id, &number, &issued, &due, &paid, &status,
		&currency, &gross, &open); err != nil {
		return crmcontracts.FinanceInvoice{}, fmt.Errorf("scan invoice: %w", err)
	}
	out := crmcontracts.FinanceInvoice{
		Id:         openapi_types.UUID(id),
		Number:     number,
		IssuedAt:   openapi_types.Date{Time: issued},
		Status:     crmcontracts.FinanceInvoiceStatus(status),
		Currency:   currency,
		GrossMinor: gross,
		OpenMinor:  open,
	}
	if due != nil {
		out.DueAt = &openapi_types.Date{Time: *due}
	}
	out.PaidAt = paid
	if late, ok := DaysLate(Invoice{
		Status: status, IssuedOn: issued, DueOn: due, FullyPaidAt: paid,
		OpenMinorBase: open,
	}); ok {
		out.DaysLate = &late
	} else if due != nil && open > 0 && now.After(*due) {
		// Still open and past due: lateness is measured against today rather
		// than against a settlement that has not happened.
		running := int(now.Sub(*due).Hours() / 24)
		out.DaysLate = &running
	}
	return out, nil
}

// readInvoices loads the account's mirrored invoices in the shape the formulas
// read, and reports the currency they are stated in.
//
// The base-currency conversion is deliberately NOT done here. Every invoice
// carries the rate frozen at its own issue date (DM-FX-4), and an invoice
// whose issue date had no effective rate is marked rather than skipped — the
// formulas refuse the whole figure on one of those (FIN-AC-6), which they can
// only do if the row reaches them.
//
// `currency` is the issued currency when the account bills in exactly one, and
// empty otherwise. A mixed-currency account has no single label for a total,
// and the formulas' own conversion is what produces one — until the rate sheet
// is wired through, a summary with no single currency reports no money figure
// rather than a sum wearing the wrong symbol.
func readInvoices(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, connectionID ids.UUID,
) ([]Invoice, string, error) {
	// The amounts are converted HERE, at each invoice's own frozen rate
	// (FIN-PARAM-7/DM-FX-4), because the formulas' fields are base-currency by
	// contract. An invoice with no rate keeps its issued amounts and is marked
	// — the formulas refuse the whole figure on one of those (FIN-AC-6), which
	// they can only do if the row reaches them.
	//
	// Rounded to the nearest minor unit rather than truncated: consistently
	// rounding down turns a hundred invoices into a total that is quietly
	// short.
	rows, err := tx.Query(ctx, `
		SELECT status, issued_at, due_at, fully_paid_at, currency,
		       round(net_minor * coalesce(fx_rate_to_base, 1))::bigint,
		       round(credited_minor * coalesce(fx_rate_to_base, 1))::bigint,
		       round(open_minor * coalesce(fx_rate_to_base, 1))::bigint,
		       credits_invoice_id IS NOT NULL, disputed_at IS NOT NULL,
		       fx_rate_to_base IS NULL
		  FROM finance_invoice
		 WHERE organization_id = $1 AND connection_id = $2 AND archived_at IS NULL
		 ORDER BY issued_at ASC, id ASC`, orgID, connectionID)
	if err != nil {
		return nil, "", fmt.Errorf("read invoices: %w", err)
	}
	defer rows.Close()
	var out []Invoice
	currencies := map[string]bool{}
	for rows.Next() {
		var (
			inv      Invoice
			currency string
		)
		if err := rows.Scan(&inv.Status, &inv.IssuedOn, &inv.DueOn, &inv.FullyPaidAt,
			&currency, &inv.NetMinorBase, &inv.CreditedMinorBase, &inv.OpenMinorBase,
			&inv.CreditsInvoice, &inv.Disputed, &inv.RateMissing); err != nil {
			return nil, "", fmt.Errorf("scan invoice: %w", err)
		}
		currencies[currency] = true
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read invoices: %w", err)
	}
	if len(currencies) != 1 {
		// Zero currencies means no invoices; more than one means no single
		// label. Both leave the money figures absent, which is the honest
		// answer either way.
		return out, "", nil
	}
	for currency := range currencies {
		return out, currency, nil
	}
	return out, "", nil
}
