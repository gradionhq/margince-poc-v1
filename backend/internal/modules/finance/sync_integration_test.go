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
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
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
	// To head before anything else touches this database: testdb.Pool refuses
	// until EnsureSchema has run, and EnsureSchema still REBUILDS whenever it
	// cannot prove the database is a fresh lane clone — so a seed written
	// before it would be dropped rather than reset.
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &financeEnv{
		ws:       ids.NewV7(),
		org:      ids.New[ids.OrganizationKind](),
		external: "ACME-01",
	}
	connID := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		e.ws, "fin-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO organization (id, display_name, lifecycle, source, captured_by)
		 VALUES ($1, 'Ledger GmbH', 'customer', 'manual', 'human:test')`,
		e.org); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO finance_connection
		       (id, provider, status, credential_ref, source, captured_by)
		VALUES ($1, $2, 'active', 'offline://test', 'system', 'system:test')`,
		connID, OfflineProviderName); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO finance_customer_link
		       (connection_id, organization_id, external_customer_id,
		        sync_hash, source, captured_by)
		VALUES ($1, $2, $3, 'seed', 'system', 'system:test')`,
		connID, e.org, e.external); err != nil {
		t.Fatal(err)
	}

	pool, err := testdb.Pool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Registered where the pool is handed out, before the test adds any cleanup
	// of its own, so it runs last and sees a package that has genuinely stopped.
	// The pool outlives the test now, so a goroutine still holding a connection
	// would go on writing into the database the NEXT test just reset.
	t.Cleanup(func() { testdb.AssertPoolsQuiesced(t) })
	// The mirror's base currency is a fixed input here, not a thing under
	// test: this suite is about the sync pass — the hash discipline, the
	// derived status, the credit-note placement. Injecting the literal keeps
	// it that way, and keeps the suite indifferent to where the installation
	// stores its currency.
	e.store = NewStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](e.ws)), func(context.Context, pgx.Tx) (string, error) {
		return "EUR", nil
	})

	opCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	// The sweep's own principal: a connector, on a schedule, with no human
	// behind it — which is what makes every mirrored row say so.
	e.ctx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:finance",
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
	at := NewStore(e.store.db, e.store.baseCurrency).
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

	atEpoch := NewStore(e.store.db, e.store.baseCurrency).WithClock(func() time.Time { return offlineEpoch })
	if _, err := atEpoch.SyncConnection(ctx, provider); err != nil {
		t.Fatal(err)
	}
	// A year later, with the SAME source. Every open invoice is now long past
	// due, so a status-in-the-hash implementation would rewrite them all.
	later := NewStore(e.store.db, e.store.baseCurrency).WithClock(func() time.Time { return offlineEpoch.AddDate(1, 0, 0) })
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

// The write shape on the mirror path: every row the sync writes commits its
// audit_log row in the SAME transaction, and a pass that writes no row writes
// no history either.
//
// Both halves matter, and neither can be proved anywhere but here. Without the
// first, money arrives in the mirror with nothing recording that it did — and
// an audit row cannot be backfilled, so every invoice mirrored before the fix
// is permanently unaccounted for. Without the second, a sweep that runs every
// six hours over an unchanged ledger would mint history forever, which is the
// failure the whole hash discipline exists to avoid.
func TestEveryMirroredRowWritesItsAuditRowAndASecondPassWritesNone(t *testing.T) {
	e := setupFinance(t)
	ctx, provider, store := e.ctx, e.provider(), e.store

	// Taken BEFORE the sync: the assertions below are about what THIS sync
	// wrote, and the fixture has already written history of its own.
	mark := auditWatermark(ctx, t, e)

	first, err := store.SyncConnection(ctx, provider)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.InvoicesInsert == 0 || first.PaymentsWrite == 0 {
		t.Fatalf("the first sync mirrored %d invoices and %d payments; the rest of this test proves nothing",
			first.InvoicesInsert, first.PaymentsWrite)
	}

	audits := auditRowsByEntity(ctx, t, e, mark)
	for _, want := range []struct {
		entity string
		count  int
	}{
		{"finance_invoice", first.InvoicesInsert},
		{"finance_payment", first.PaymentsWrite},
		{"finance_external_customer", first.CustomersSeen},
	} {
		if audits[want.entity] != want.count {
			t.Errorf("%d audit_log rows for %s, want %d — mirrored money nobody can account for",
				audits[want.entity], want.entity, want.count)
		}
	}

	// The delta, not the total. A second pass over an unchanged source writes
	// no domain row, so it must write no audit row: asserting the movement is
	// what makes this about the no-op rather than about the first pass.
	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	again := auditRowsByEntity(ctx, t, e, mark)
	for entity, before := range audits {
		if again[entity] != before {
			t.Errorf("a second pass over an unchanged source wrote history for %s: %d→%d",
				entity, before, again[entity])
		}
	}
	if events := outboxRowsForFinance(ctx, t, e); events != 0 {
		t.Errorf("%d event_outbox rows name a finance entity, want 0 — the mirror is audit-only until the contract carries a finance event type",
			events)
	}
}

// auditRowsByEntity counts what the ledger holds per finance entity type. The
// workspace is minted per run, so every row it sees belongs to this fixture.
// auditWatermark is the highest audit_log id before the work under test runs.
// audit_log ids are UUIDv7 and therefore time-ordered, so a later row always
// compares greater — which is what makes this a watermark rather than a guess.
func auditWatermark(ctx context.Context, t *testing.T, e *financeEnv) ids.UUID {
	t.Helper()
	var high ids.UUID
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		// ORDER BY ... LIMIT 1 rather than max(): Postgres has no max(uuid), and
		// this is the same ordering the > comparison below relies on.
		return tx.QueryRow(ctx, `
			SELECT coalesce(
				(SELECT id FROM audit_log ORDER BY id DESC LIMIT 1),
				'00000000-0000-0000-0000-000000000000'::uuid)`).Scan(&high)
	}); err != nil {
		t.Fatalf("reading the audit watermark: %v", err)
	}
	return high
}

// auditRowsByEntity counts the finance audit rows written SINCE the watermark.
//
// It counted this run's workspace until ADR-0091 §8 phase D took the tenant
// column off audit_log. The predicate was never about tenancy here — this
// package's tests share one database, so it was keeping one test's rows out of
// another's count, and a bare count would pass or fail on test order. The
// watermark says the same thing more directly: these are the rows the work
// under test wrote.
func auditRowsByEntity(ctx context.Context, t *testing.T, e *financeEnv, since ids.UUID) map[string]int {
	t.Helper()
	out := map[string]int{}
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT entity_type, count(*) FROM audit_log
			 WHERE entity_type LIKE 'finance\_%' AND id > $1
			 GROUP BY entity_type`, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				entity string
				count  int
			)
			if err := rows.Scan(&entity, &count); err != nil {
				return err
			}
			out[entity] = count
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("counting the mirror's audit rows: %v", err)
	}
	return out
}

// outboxRowsForFinance counts what the mirror staged on the bus.
//
// The match is on the envelope's ENTITY TYPE, the same prefix the audit query
// above groups by, because the outbox carries no workspace member to scope on —
// so a predicate written against one would match nothing whatever the mirror
// did, and the assertion would pass over a mirror that published on every row.
// Nothing else in the tree emits a finance entity, so an unscoped count is
// stricter here rather than looser.
func outboxRowsForFinance(ctx context.Context, t *testing.T, e *financeEnv) int {
	t.Helper()
	var count int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM event_outbox
			 WHERE envelope->'entity'->>'type' LIKE 'finance\_%'`).Scan(&count)
	}); err != nil {
		t.Fatalf("counting the outbox: %v", err)
	}
	return count
}

// The connection's STATE is a fact about whether the figures beside it can be
// trusted, so every transition is on its history — and its heartbeat is not.
//
// The sweep rewrites `last_attempt_at` every six hours whatever happened. An
// audit row for that would file four rows a day per connection saying nothing
// changed, and the two transitions anybody reads this history for — the source
// went down, the source came back — would be buried in them.
func TestAConnectionAuditsItsStateChangesAndNotItsHeartbeat(t *testing.T) {
	e := setupFinance(t)
	ctx, provider, store := e.ctx, e.provider(), e.store
	mark := auditWatermark(ctx, t, e)
	connectionAudits := func() int { return auditRowsByEntity(ctx, t, e, mark)[entityConnection] }

	// The fixture seeds the connection active, so a pass that succeeds leaves
	// it exactly as it was: a heartbeat, and nothing to record.
	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := connectionAudits(); got != 0 {
		t.Fatalf("%d audit rows for a pass that changed no state, want 0 — the heartbeat is filing history", got)
	}

	cause := errors.New("the accounting source refused the read")
	if err := store.RecordSyncFailure(ctx, cause); !errors.Is(err, cause) {
		t.Fatalf("RecordSyncFailure answered %v, want the original cause back", err)
	}
	if got := connectionAudits(); got != 1 {
		t.Fatalf("%d audit rows after the source went down, want 1", got)
	}
	// The same failure again is the same state. Still no transition.
	if err := store.RecordSyncFailure(ctx, cause); !errors.Is(err, cause) {
		t.Fatalf("second RecordSyncFailure answered %v, want the original cause back", err)
	}
	if got := connectionAudits(); got != 1 {
		t.Fatalf("%d audit rows after a repeat of the same failure, want 1", got)
	}
	// Recovery is a transition, and the one a reader most wants dated.
	if _, err := store.SyncConnection(ctx, provider); err != nil {
		t.Fatalf("recovery sync: %v", err)
	}
	if got := connectionAudits(); got != 2 {
		t.Fatalf("%d audit rows after the source came back, want 2", got)
	}
}

// A failed sync marks THE connection it ran against, not every live one.
//
// The statement carried no id predicate at all, so one source failing reported
// every other source broken. That was invisible while an installation held one
// connection and wrong the moment it held two — and now that the write files an
// audit row, it would put a failure on the history of a source that was
// answering perfectly well.
func TestAFailedSyncMarksOnlyTheConnectionItRanAgainst(t *testing.T) {
	e := setupFinance(t)
	other := e.plantSecondConnection(t)
	mark := auditWatermark(e.ctx, t, e)

	cause := errors.New("the accounting source refused the read")
	if err := e.store.RecordSyncFailure(e.ctx, cause); !errors.Is(err, cause) {
		t.Fatalf("RecordSyncFailure answered %v, want the original cause back", err)
	}
	if status := e.connectionStatus(t, other); status != "active" {
		t.Errorf("the bystander connection reads %q after another source failed, want \"active\"", status)
	}
	if got := auditRowsByEntity(e.ctx, t, e, mark)[entityConnection]; got != 1 {
		t.Errorf("%d connection audit rows for one failed source, want 1", got)
	}
}

// plantSecondConnection adds a second live source and answers its id. It is
// created FIRST in time — readConnection takes the newest — so the sync under
// test still runs against the fixture's own connection and this one is the
// bystander.
func (e *financeEnv) plantSecondConnection(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if err := e.store.tx(e.ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, `
			INSERT INTO finance_connection
			       (id, provider, status, credential_ref, source, captured_by, created_at)
			VALUES ($1, $2, 'active', 'offline://bystander', 'system', 'system:test', now() - interval '1 day')`,
			id, OfflineProviderName)
		return err
	}); err != nil {
		t.Fatalf("planting the bystander connection: %v", err)
	}
	return id
}

func (e *financeEnv) connectionStatus(t *testing.T, id ids.UUID) string {
	t.Helper()
	var status string
	if err := e.store.tx(e.ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx,
			`SELECT status FROM finance_connection WHERE id = $1`, id).Scan(&status)
	}); err != nil {
		t.Fatalf("reading the connection's status: %v", err)
	}
	return status
}

// A source that RESTATES what it said is the only way to reach the mirror's
// update arms, and it is where the audit trail earns its keep: the before image
// is what the money was, read off the row, and the after is what the source now
// says it is.
//
// The offline generator cannot produce this. It is deterministic by design — a
// source that never changes its mind — so the paths that record a change are
// unreachable through it. The double below stands at the PROVIDER seam and
// nowhere else: the store, the SQL, the images and the audit writer under test
// are all the real ones.
func TestARestatedSourceAuditsTheUpdateWithBothImages(t *testing.T) {
	e := setupFinance(t)
	source := restatingSource(e.external)
	mark := auditWatermark(e.ctx, t, e)

	if _, err := e.store.SyncConnection(e.ctx, source); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	created := auditRowsByEntity(e.ctx, t, e, mark)

	// A tax correction that leaves GROSS UNCHANGED — net 10000 → 11000, tax
	// 1900 → 900, gross still 11900 — plus a restated payment and a renamed
	// customer. Gross is held still deliberately: it is the case an audit image
	// narrower than the change key cannot record, because the only columns that
	// moved are ones such an image does not carry, and the row would say money
	// changed without saying what.
	source.customer.DisplayName = "Ledger GmbH (Hamburg)"
	source.ledger.Invoices[0].NetMinor = 11000
	source.ledger.Invoices[0].TaxMinor = 900
	source.ledger.Payments[0].AmountMinor = 6000
	if _, err := e.store.SyncConnection(e.ctx, source); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	for _, entity := range []string{entityInvoice, entityPayment, entityExternalCustomer} {
		if got, want := auditRowsByEntity(e.ctx, t, e, mark)[entity], created[entity]+1; got != want {
			t.Errorf("%d audit rows for %s after the source restated it, want %d",
				got, entity, want)
		}
	}

	// The before image has to be what the ROW held, not the source's new
	// figures rendered twice. A writer that rebuilt it from the source would
	// pass every count above and record a change that did not happen.
	before, after := updateImages(e.ctx, t, e, entityInvoice, mark)
	if before["net_minor"] != float64(10000) || after["net_minor"] != float64(11000) {
		t.Errorf("the invoice's update reads net %v → %v, want 10000 → 11000",
			before["net_minor"], after["net_minor"])
	}
	if before["gross_minor"] != after["gross_minor"] {
		t.Errorf("gross moved (%v → %v) and this case is about the columns that move UNDER a fixed gross",
			before["gross_minor"], after["gross_minor"])
	}
	before, after = updateImages(e.ctx, t, e, entityPayment, mark)
	if before["amount_minor"] != float64(5000) || after["amount_minor"] != float64(6000) {
		t.Errorf("the payment's update reads %v → %v, want 5000 → 6000",
			before["amount_minor"], after["amount_minor"])
	}
}

// What the mirror's rows say ADMITTED them. The sweep holds no grant — it runs
// on a path ratified as ungated, with no request and nobody behind it — and the
// row has to say that rather than render a merged policy it never had.
//
// The value is append-only once written, which is why it is asserted here and
// not left to a reader to notice years later.
func TestTheMirrorsAuditRowsNameNoGrantTheSweepDoesNotHold(t *testing.T) {
	e := setupFinance(t)
	if _, err := e.store.SyncConnection(e.ctx, e.provider()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var rules []string
	if err := e.store.tx(e.ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `
			SELECT array_agg(DISTINCT authorization_rule) FROM audit_log
			 WHERE entity_type LIKE 'finance\_%'`).Scan(&rules)
	}); err != nil {
		t.Fatalf("reading the mirror's authorization rules: %v", err)
	}
	if len(rules) != 1 || rules[0] != "system" {
		t.Errorf("the mirror's rows record authorization_rule %v, want exactly [system] — "+
			"anything shaped like `role[] x.create row_scope=` claims a policy the sweep never held", rules)
	}
	var actorTypes []string
	if err := e.store.tx(e.ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `
			SELECT array_agg(DISTINCT actor_type) FROM audit_log
			 WHERE entity_type LIKE 'finance\_%'`).Scan(&actorTypes)
	}); err != nil {
		t.Fatalf("reading the mirror's actor types: %v", err)
	}
	if len(actorTypes) != 1 || actorTypes[0] != "connector" {
		t.Errorf("the mirror's rows record actor_type %v, want exactly [connector] — "+
			"a `system` type beside a `connector:` actor_id is a row that contradicts itself", actorTypes)
	}
}

// updateImages reads the two images off the one `update` audit row this
// workspace holds for an entity type.
// updateImages reads the one update row this run wrote for entityType, fenced
// on the same watermark auditRowsByEntity uses: the package's tests share a
// database, so an unfenced read returns whichever update landed first, and the
// images it then asserts belong to another test's invoice.
func updateImages(
	ctx context.Context, t *testing.T, e *financeEnv, entityType string, since ids.UUID,
) (before, after map[string]any) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		var beforeRaw, afterRaw []byte
		if err := tx.QueryRow(ctx, `
			SELECT before, after FROM audit_log
			 WHERE entity_type = $1 AND action = 'update' AND id > $2`,
			entityType, since).Scan(&beforeRaw, &afterRaw); err != nil {
			return err
		}
		if err := json.Unmarshal(beforeRaw, &before); err != nil {
			return err
		}
		return json.Unmarshal(afterRaw, &after)
	}); err != nil {
		t.Fatalf("reading the %s update's images: %v", entityType, err)
	}
	return before, after
}

// restatingSource is one customer with one invoice and one payment against it,
// held in fields the test rewrites between passes.
func restatingSource(externalID string) *restatingProvider {
	issued := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	due := issued.AddDate(0, 0, 30)
	return &restatingProvider{
		customer: SourceCustomer{ExternalID: externalID, DisplayName: "Ledger GmbH"},
		ledger: SourceLedger{
			Invoices: []SourceInvoice{{
				ExternalID: "INV-RESTATED-1", Number: "2026-001",
				IssuedOn: issued, DueOn: &due, Currency: "EUR",
				NetMinor: 10000, TaxMinor: 1900, GrossMinor: 11900, PaidMinor: 5000,
			}},
			Payments: []SourcePayment{{
				ExternalID: "PAY-RESTATED-1", InvoiceExternalID: "INV-RESTATED-1",
				PaidAt: issued, Currency: "EUR", AmountMinor: 5000,
			}},
		},
	}
}

// restatingProvider answers whatever its fields currently hold, so a test can
// change the source's mind between two passes.
type restatingProvider struct {
	customer SourceCustomer
	ledger   SourceLedger
}

func (p *restatingProvider) Name() string { return OfflineProviderName }

func (p *restatingProvider) Customers(context.Context) ([]SourceCustomer, error) {
	return []SourceCustomer{p.customer}, nil
}

func (p *restatingProvider) InvoicesFor(context.Context, string) (SourceLedger, error) {
	return p.ledger, nil
}
