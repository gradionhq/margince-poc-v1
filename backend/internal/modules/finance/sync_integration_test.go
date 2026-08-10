// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package finance

// The sync pass against a real database, and the one claim the whole design
// rests on: a second pass over an unchanged source writes NOTHING.
//
// It cannot be proved anywhere else. The hash discipline, the derived status,
// the credit-note placement and the row locking all meet in the SQL, and a
// unit test over the formulas would happily pass while every pass rewrote
// every row.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// financeEnv is one workspace with a connected offline source and one linked
// organization — the smallest install the sync has anything to do on.
type financeEnv struct {
	store    *Store
	ctx      context.Context
	ws       ids.UUID
	org      ids.OrganizationID
	external string
}

func setupFinance(t *testing.T) *financeEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &financeEnv{
		ws:       ids.NewV7(),
		org:      ids.New[ids.OrganizationKind](),
		external: "ACME-01",
	}
	connID := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Finance', $2, 'EUR')`,
		e.ws, "fin-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO organization (id, workspace_id, display_name, lifecycle, source, captured_by)
		 VALUES ($1, $2, 'Ledger GmbH', 'customer', 'manual', 'human:test')`,
		e.org, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO finance_connection
		       (id, workspace_id, provider, status, credential_ref, source, captured_by)
		VALUES ($1, $2, $3, 'active', 'offline://test', 'system', 'system:test')`,
		connID, e.ws, OfflineProviderName); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO finance_customer_link
		       (workspace_id, connection_id, organization_id, external_customer_id,
		        sync_hash, source, captured_by)
		VALUES ($1, $2, $3, $4, 'seed', 'system', 'system:test')`,
		e.ws, connID, e.org, e.external); err != nil {
		t.Fatal(err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// The mirror's base currency is a fixed input here, not a thing under
	// test: this suite is about the sync pass — the hash discipline, the
	// derived status, the credit-note placement. Injecting the literal keeps
	// it that way, and keeps the suite indifferent to where the installation
	// stores its currency.
	e.store = NewStore(pool, func(context.Context, pgx.Tx) (string, error) {
		return "EUR", nil
	})

	opCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	// The sweep's own principal: a connector, on a schedule, with no human
	// behind it — which is what makes every mirrored row say so.
	e.ctx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "connector:finance",
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"finance":      {Read: true},
				"organization": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return e
}

// ledgerSeed is the workspace the GENERATOR is seeded from, and it is a
// constant rather than this env's workspace on purpose.
//
// The generator's seed is sha256(workspace | customer), so passing a freshly
// minted workspace would draw a different archetype — a different ledger, a
// different sample, a different open balance — on every run. That is a lottery,
// not a fixture: the assertions below would be about which archetype came up
// rather than about what the sync and the formulas do.
//
// The workspace the ROWS live in is still e.ws, minted per run so parallel runs
// do not share a tenant. Nothing but the seed reads this string.
const ledgerSeed = "finance-integration-ledger"

func (e *financeEnv) provider() Provider {
	return NewOfflineProvider(ledgerSeed, []SourceCustomer{{ExternalID: e.external}})
}

// summaryAtEpoch reads the card at a clock pinned to the ledger's own epoch.
//
// Every FIGURE on the card is folded over a window measured back from the
// store's clock, and the generated ledger is anchored to a fixed date rather
// than to today (see offlineEpoch). Read at wall-clock time, the assertions
// over those figures measure how far the calendar has travelled since that
// epoch: they thin out as the window slides off the ledger and eventually
// report an empty card. Pinned, they measure the formulas, which is what they
// are for.
//
// The STATE is deliberately not read through here. Staleness is a real
// comparison between the connection's last_success_at — stamped by the
// database's clock — and now; pinning one side of it would make the assertion
// pass for the wrong reason.
func (e *financeEnv) summaryAtEpoch(
	t *testing.T, orgID ids.OrganizationID,
) crmcontracts.OrganizationFinanceSummary {
	t.Helper()
	at := NewStore(e.store.pool, e.store.baseCurrency).
		WithClock(func() time.Time { return offlineEpoch })
	out, err := at.SummaryFor(e.ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// THE claim. An invoice's status depends on today, so a hash that covered it
// would make every pass rewrite every row — an event per invoice, a version
// bump per row, and a mirror reporting change where none happened.
func TestASecondSyncOverAnUnchangedSourceWritesNothing(t *testing.T) {
	e := setupFinance(t)
	ctx, provider, store := e.ctx, e.provider(), e.store

	first, err := store.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.InvoicesInsert == 0 {
		t.Fatal("the first sync mirrored no invoices; the rest of this test proves nothing")
	}

	second, err := store.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.InvoicesInsert != 0 || second.InvoicesUpdate != 0 {
		t.Fatalf("the second pass wrote %d new and %d updated invoices, want none",
			second.InvoicesInsert, second.InvoicesUpdate)
	}
	if second.PaymentsWrite != 0 {
		t.Fatalf("the second pass wrote %d payments, want none", second.PaymentsWrite)
	}
	if second.Unchanged != first.InvoicesInsert+first.PaymentsWrite {
		t.Fatalf("the second pass reported %d unchanged over %d rows the first wrote",
			second.Unchanged, first.InvoicesInsert+first.PaymentsWrite)
	}
}

// A row nobody touched keeps its version. The version bump is what an audit
// trail and a concurrency guard both read, so an idle pass that moved it would
// make every invoice look edited.
func TestAnUnchangedInvoiceKeepsItsVersion(t *testing.T) {
	e := setupFinance(t)
	ctx, orgID, provider := e.ctx, e.org, e.provider()
	store := e.store

	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	before := invoiceVersions(ctx, t, e, orgID)
	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	after := invoiceVersions(ctx, t, e, orgID)
	for id, version := range before {
		if after[id] != version {
			t.Fatalf("invoice %s went from version %d to %d without the source changing",
				id, version, after[id])
		}
	}
}

func invoiceVersions(
	ctx context.Context, t *testing.T, e *financeEnv, orgID ids.OrganizationID,
) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT external_id, version FROM finance_invoice WHERE organization_id = $1`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id      string
				version int64
			)
			if err := rows.Scan(&id, &version); err != nil {
				return err
			}
			out[id] = version
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read invoice versions: %v", err)
	}
	return out
}

// The whole point of the arc: after a sync, the card answers with figures
// rather than with a state that says it cannot.
func TestAfterASyncTheCardHasFiguresToShow(t *testing.T) {
	e := setupFinance(t)
	ctx, orgID, provider := e.ctx, e.org, e.provider()
	store := e.store

	// Before the pass: connected and mapped, but nothing synced. The card says
	// so rather than showing zeroes.
	before, err := store.SummaryFor(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != crmcontracts.FinanceSummaryStateSyncing {
		t.Fatalf("state before the first sync = %q, want syncing", before.State)
	}
	if before.NetInvoiced != nil || before.OpenBalance != nil {
		t.Fatal("a never-synced connection reported figures")
	}

	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	after, err := store.SummaryFor(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != crmcontracts.FinanceSummaryStateConnected {
		t.Fatalf("state after the sync = %q, want connected", after.State)
	}

	// The figures, as of the ledger's epoch — see summaryAtEpoch for why they
	// are not read off the summary above.
	figures := e.summaryAtEpoch(t, orgID)
	if figures.NetInvoiced == nil || figures.NetInvoiced.AmountMinor == nil {
		t.Fatal("no net invoiced after a sync that mirrored a ledger")
	}
	if *figures.NetInvoiced.AmountMinor <= 0 {
		t.Fatalf("net invoiced = %d, want a positive figure", *figures.NetInvoiced.AmountMinor)
	}
	// The generator leaves an open tail on purpose, because "what do they owe
	// us" is the reading the card leads with.
	if figures.OpenBalance == nil || *figures.OpenBalance.AmountMinor <= 0 {
		t.Fatal("no open balance after a sync; the card's lead reading is empty")
	}
	if figures.RecentInvoices == nil || len(*figures.RecentInvoices) == 0 {
		t.Fatal("no recent invoices after a sync")
	}
	// The generator clears the timeliness sample floor, so the payment reading
	// is a figure rather than a refusal.
	if figures.MedianDaysAfterDue == nil {
		t.Fatal("no payment-behaviour median after a sync over eighteen months")
	}
}

// A credit note reduces the invoice it names. FIN-FORM-1's term is
// `net - credited`, read off the reduced invoice, so this is the write that
// proves the amount landed on the right row.
func TestTheCreditNoteReducesItsTargetInTheMirror(t *testing.T) {
	e := setupFinance(t)
	ctx, orgID, provider := e.ctx, e.org, e.provider()
	if _, err := e.store.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}

	var (
		creditedRows int
		noteOwes     int64
	)
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM finance_invoice
			 WHERE organization_id = $1 AND credited_minor > 0`, orgID).Scan(&creditedRows); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT coalesce(sum(open_minor), 0) FROM finance_invoice
			 WHERE organization_id = $1 AND credits_invoice_id IS NOT NULL`,
			orgID).Scan(&noteOwes)
	}); err != nil {
		t.Fatal(err)
	}
	if creditedRows == 0 {
		t.Fatal("no invoice carries a credited amount; the credit landed nowhere")
	}
	// A credit note is money going the other way. An open balance on it would
	// inflate receivables by the amount it was meant to reduce them by.
	if noteOwes != 0 {
		t.Fatalf("the credit notes owe %d, want 0", noteOwes)
	}
}

// The clock moves; the source does not. An invoice that crosses its due date
// between two passes becomes overdue on READ, and the pass must not rewrite
// the row to say so — that is the whole reason status is derived.
func TestCrossingADueDateDoesNotRewriteTheLedger(t *testing.T) {
	e := setupFinance(t)
	ctx, provider := e.ctx, e.provider()

	atEpoch := NewStore(e.store.pool, e.store.baseCurrency).WithClock(func() time.Time { return offlineEpoch })
	if _, err := atEpoch.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	// A year later, with the SAME source. Every open invoice is now long past
	// due, so a status-in-the-hash implementation would rewrite them all.
	later := NewStore(e.store.pool, e.store.baseCurrency).WithClock(func() time.Time { return offlineEpoch.AddDate(1, 0, 0) })
	second, err := later.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if second.InvoicesUpdate != 0 {
		t.Fatalf("a year passing rewrote %d invoices; status must not ride the hash",
			second.InvoicesUpdate)
	}
}

// The repair Codex named: an invoice mirrored BEFORE the rate was recorded is
// unchanged on the source, so the hash skip would keep it unconvertible for
// good — and one unconvertible row refuses the whole total (FIN-AC-6). The
// source has no reason to change again, so nothing would ever fix it.
func TestASyncRepairsAnInvoiceThatIsMissingItsRate(t *testing.T) {
	e := setupFinance(t)
	ctx, orgID, provider := e.ctx, e.org, e.provider()

	if _, err := e.store.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	// Put the ledger back into the state the old writer left: rates cleared,
	// hashes untouched. This is what a database that synced before the fix
	// actually holds.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE finance_invoice
			   SET fx_rate_to_base = NULL, fx_rate_date = NULL
			 WHERE organization_id = $1`, orgID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := e.summaryAtEpoch(t, orgID)
	if before.NetInvoiced != nil {
		t.Fatal("a ledger with no rates reported a total; the rest of this test proves nothing")
	}

	repair, err := e.store.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if repair.InvoicesUpdate == 0 {
		t.Fatal("the pass skipped every invoice, so the rates were never repaired")
	}
	if after := e.summaryAtEpoch(t, orgID); after.NetInvoiced == nil ||
		after.NetInvoiced.AmountMinor == nil {
		t.Fatal("still no total after a repair pass")
	}

	// And it settles. A repair that re-fired every pass would be the same bug
	// wearing the opposite sign: an event and a version bump per invoice, four
	// times a day, forever.
	settled, err := e.store.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if settled.InvoicesUpdate != 0 {
		t.Fatalf("the pass after the repair rewrote %d invoices, want none",
			settled.InvoicesUpdate)
	}
}
