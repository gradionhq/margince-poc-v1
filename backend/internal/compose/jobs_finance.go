// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The finance mirror's River wiring (ADR-0083/A128): a sweep every six hours
// that fans out one pass per workspace, and the pass itself.
//
// The provider is chosen HERE rather than inside the module, which is the
// point of the seam: the store knows how to mirror a ledger and nothing about
// where one comes from. Today the only provider is the offline generator, so
// an installation that connects "offline_demo" gets a plausible ledger and one
// that connects anything else gets an honest refusal rather than a silent
// no-op.
//
// Job args and worker adapters only — the mirror stays River-agnostic.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/finance"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FinanceSyncSweepArgs runs one fleet-wide finance pass.
type FinanceSyncSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (FinanceSyncSweepArgs) Kind() string { return "finance_sync_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues, and does no
// tenant work of its own (jobs.FleetWide).
func (FinanceSyncSweepArgs) FleetWide() {}

// FinanceSyncArgs is one workspace's mirror pass.
type FinanceSyncArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (FinanceSyncArgs) Kind() string { return "finance_sync" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a FinanceSyncArgs) WorkspaceID() ids.UUID { return a.Workspace }

// financeSyncSweepWorker is the dispatcher.
type financeSyncSweepWorker struct {
	pool *pgxpool.Pool
}

func (w *financeSyncSweepWorker) Work(ctx context.Context, _ *river.Job[FinanceSyncSweepArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(FinanceSyncArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return FinanceSyncArgs{Workspace: ws} }))
}

// financeSyncWorker mirrors one workspace's accounting source.
type financeSyncWorker struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func (w *financeSyncWorker) Work(ctx context.Context, job *river.Job[FinanceSyncArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// The connector is the acting principal: every mirrored row carries
	// connector: provenance, so a reader can tell an imported invoice from
	// anything a person typed.
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "connector:finance",
	})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())

	provider, configured, err := w.providerFor(wsCtx, job.Args.Workspace)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if !configured {
		// No accounting source, or one this build has no provider for. Not an
		// error and not worth a line every six hours: most installations have
		// connected nothing, and a sweep over them has nothing to do.
		return nil
	}
	store := finance.NewStore(w.pool, identity.BaseCurrencyOf)
	result, err := store.SyncConnection(wsCtx, provider)
	if err != nil {
		return jobs.FaultContext(ctx, store.RecordSyncFailure(wsCtx, err))
	}
	// Logged only when something moved. The hash discipline means a pass over
	// an unchanged ledger writes nothing, and that is the common case — a line
	// on every tick would bury the passes that did something.
	if result.InvoicesInsert+result.InvoicesUpdate+result.PaymentsWrite > 0 {
		w.log.InfoContext(wsCtx, "finance sync: mirrored what changed",
			"invoices_new", result.InvoicesInsert,
			"invoices_updated", result.InvoicesUpdate,
			"payments_written", result.PaymentsWrite,
			"unchanged", result.Unchanged,
			"orphan_credits", result.OrphanCredits)
	}
	return nil
}

// providerFor resolves the reader for this workspace's configured source.
//
// `configured` is false when there is no live connection, which is the
// ordinary state of most installations. An unknown provider is an ERROR rather
// than a quiet skip: an operator who connected something this build cannot
// read deserves to be told, not left watching a card that never fills in.
//
// workspace's configured source needs, and returning the concrete generator
// would make the choice uninteresting and the second provider a rewrite.
//
//nolint:ireturn // the seam IS an interface: this resolves WHICH reader a
func (w *financeSyncWorker) providerFor(
	ctx context.Context, workspace ids.UUID,
) (finance.Provider, bool, error) {
	var (
		name      string
		customers []finance.SourceCustomer
	)
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT provider FROM finance_connection
			 WHERE archived_at IS NULL AND status <> 'disconnected'
			 ORDER BY created_at DESC
			 LIMIT 1`)
		if err := row.Scan(&name); err != nil {
			// pgx.ErrNoRows lands here and is answered as "not configured" by
			// the caller below; anything else is a real read failure.
			return err
		}
		var readErr error
		customers, readErr = linkedCustomers(ctx, tx)
		return readErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read the finance connection: %w", err)
	}
	if name != finance.OfflineProviderName {
		return nil, false, fmt.Errorf(
			"finance: no reader for provider %q — this build ships only %q",
			name, finance.OfflineProviderName)
	}
	return finance.NewOfflineProvider(workspace.String(), customers), true, nil
}

// linkedCustomers is the directory the offline provider generates for.
//
// It is built from the LINKS an operator already made, not from an invented
// customer list: the generator's job is to produce a ledger for a customer
// somebody mapped, and minting customers nobody asked for would put rows in
// the mirror that match no company.
func linkedCustomers(ctx context.Context, tx pgx.Tx) ([]finance.SourceCustomer, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.external_customer_id, coalesce(o.display_name, l.external_customer_id)
		  FROM finance_customer_link l
		  JOIN organization o ON o.id = l.organization_id
		 WHERE l.archived_at IS NULL
		 ORDER BY l.external_customer_id`)
	if err != nil {
		return nil, fmt.Errorf("read the customer links: %w", err)
	}
	defer rows.Close()
	var out []finance.SourceCustomer
	for rows.Next() {
		var each finance.SourceCustomer
		if err := rows.Scan(&each.ExternalID, &each.DisplayName); err != nil {
			return nil, fmt.Errorf("scan a customer link: %w", err)
		}
		out = append(out, each)
	}
	return out, rows.Err()
}

// addFinanceJobs registers the sweep and the per-workspace pass, and hands
// back the sweep's schedule. The cadence is api/jobs.yaml's, which is why
// periodicFor is what places it.
func addFinanceJobs(
	reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger,
) []*river.PeriodicJob {
	addDeclaredWorker[FinanceSyncSweepArgs](reg, &financeSyncSweepWorker{pool: pool})
	addDeclaredWorker[FinanceSyncArgs](reg, &financeSyncWorker{pool: pool, log: log})
	return periodicFor(cfg, FinanceSyncSweepArgs{})
}
