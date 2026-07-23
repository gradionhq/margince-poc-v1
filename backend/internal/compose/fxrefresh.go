// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/fxsource"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FxRateRefreshArgs is the async FX-rate refresh job: fetch fresh rates for the
// currencies this workspace already tracks and stage a proposal per changed
// rate. WorkspaceID + RequestedBy carry the acting admin so the worker binds a
// system principal on behalf of them. Uniqueness is keyed on WorkspaceID alone
// (river:"unique") so two admins refreshing the same workspace collapse to one
// crawl; RequestedBy is provenance-only, outside the uniqueness hash.
type FxRateRefreshArgs struct {
	WorkspaceID ids.UUID `json:"workspace_id" river:"unique"`
	RequestedBy string   `json:"requested_by"`
}

// Kind is the stable River job identifier.
func (FxRateRefreshArgs) Kind() string { return "fx_rate_refresh" }

// rateRefreshWorkerCtx binds the system principal a refresh producer runs
// under (bypasses auth.Require), on the requesting admin's workspace, with a
// fresh correlation id for the staged approvals' write shape.
func rateRefreshWorkerCtx(ctx context.Context, ws ids.UUID, requestedBy string) context.Context {
	requester := requestedByUserID(requestedBy)
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:rate-refresh",
		UserID: requester, OnBehalfOf: requester,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// fxRefresh is the FX producer: read the tracked currencies, fetch fresh rates,
// and stage a proposal for each that changed. A nil client (no source
// configured) is an honest no-op.
type fxRefresh struct {
	store  *deals.Store
	svc    *approvals.Service
	client *fxsource.Client
	log    *slog.Logger
}

func (f fxRefresh) run(ctx context.Context) error {
	if f.client == nil {
		f.log.Info("fx rate refresh skipped: no FX source configured")
		return nil
	}
	current, err := f.store.ListLatestFxRates(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: read tracked currencies: %w", err)
	}
	if len(current) == 0 {
		f.log.Info("fx rate refresh: no tracked currencies to refresh")
		return nil
	}
	base := current[0].ToCurrency
	symbols := make([]string, 0, len(current))
	for _, r := range current {
		symbols = append(symbols, r.FromCurrency)
	}
	// Diff against what is in force TODAY, not the sheet head: approval writes
	// effective today, so a future-scheduled row must neither mask a real
	// change nor manufacture an ineffective proposal.
	effective, err := f.store.ListEffectiveFxRates(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: read effective rates: %w", err)
	}
	priorRate := make(map[string]string, len(effective))
	for _, r := range effective {
		priorRate[r.FromCurrency] = r.Rate
	}

	fetched, err := f.client.LatestRates(ctx, base, symbols)
	if err != nil {
		return fmt.Errorf("fx refresh: fetch rates: %w", err)
	}
	ws := storekit.MustWorkspace(ctx)
	staged := 0
	for cur, newRate := range fetched {
		prior := priorRate[cur]
		if prior != "" && sameRate(newRate, prior) {
			continue // unchanged vs the rate in force
		}
		was := prior
		if was == "" {
			was = "none in force today"
		}
		summary := fmt.Sprintf("%s → %s %s (was %s)", cur, base, newRate, was)
		identity, err := json.Marshal(map[string]string{"from_currency": cur})
		if err != nil {
			return fmt.Errorf("fx refresh: identity %s: %w", cur, err)
		}
		if err := stageRateProposal(ctx, f.svc, fxRateProposalKind, fxRateTargetType, ws,
			fxRateProposal{FromCurrency: cur, Rate: newRate, ExpectedPriorRate: prior}, identity, summary); err != nil {
			return fmt.Errorf("fx refresh: stage %s: %w", cur, err)
		}
		staged++
	}
	f.log.Info("fx rate refresh complete", "staged", staged, "tracked", len(current))
	return nil
}

type fxRefreshWorker struct {
	river.WorkerDefaults[FxRateRefreshArgs]
	refresh fxRefresh
}

func (w *fxRefreshWorker) Work(ctx context.Context, job *river.Job[FxRateRefreshArgs]) error {
	return w.refresh.run(rateRefreshWorkerCtx(ctx, job.Args.WorkspaceID, job.Args.RequestedBy))
}

func newFxRefreshWorker(pool *pgxpool.Pool, client *fxsource.Client, log *slog.Logger) *fxRefreshWorker {
	return &fxRefreshWorker{refresh: fxRefresh{
		store:  deals.NewStore(pool),
		svc:    approvals.NewService(pool),
		client: client,
		log:    log,
	}}
}
