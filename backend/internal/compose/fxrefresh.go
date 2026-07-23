// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// fxRefresh is the FX producer: fetch fresh rates from the source and stage a
// proposal per changed rate. It has two modes on one path — refresh the
// currencies the sheet already tracks, or, when the sheet is still empty,
// bootstrap the configured candidate set against the workspace base so the
// admin button is not dead on a fresh install. Either way a human approves
// every proposal — a rate is never invented, only proposed from the source. A
// nil client (no source configured) is an honest no-op.
type fxRefresh struct {
	store  *deals.Store
	svc    *approvals.Service
	client *fxsource.Client
	// bootstrapCurrencies is the candidate foreign-currency set proposed when
	// the sheet is empty (there is nothing tracked to derive symbols from).
	// Empty ⇒ an empty sheet stays a no-op (never a fabricated rate).
	bootstrapCurrencies []string
	log                 *slog.Logger
}

func (f fxRefresh) run(ctx context.Context) error {
	if f.client == nil {
		f.log.Info("fx rate refresh skipped: no FX source configured")
		return nil
	}
	current, err := f.store.ListLatestFxRates(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: read current rates: %w", err)
	}

	base, symbols, currentRate, err := f.plan(ctx, current)
	if err != nil {
		return err
	}
	if len(symbols) == 0 {
		// Either an empty sheet with no candidate set, or a candidate set that
		// held only the base currency — nothing the source can price for us.
		f.log.Info("fx rate refresh: nothing to refresh", "tracked", len(current))
		return nil
	}

	fetched, err := f.client.LatestRates(ctx, base, symbols)
	if err != nil {
		return fmt.Errorf("fx refresh: fetch rates: %w", err)
	}
	// A requested currency the source did not price (a well-formed but
	// unsupported/misspelled code — the config gate only checks the ISO 4217
	// shape, matching organization.base_currency) is dropped by the client, so
	// it stages no proposal. Name it rather than let the gap stay silent.
	for _, sym := range symbols {
		if _, ok := fetched[sym]; !ok {
			f.log.Warn("fx rate refresh: source returned no rate for a requested currency — nothing staged for it",
				"currency", sym, "base", base)
		}
	}
	ws := storekit.MustWorkspace(ctx)
	staged := 0
	for cur, newRate := range fetched {
		was, tracked := currentRate[cur]
		if tracked && newRate == was {
			continue // unchanged
		}
		var summary string
		if tracked {
			summary = fmt.Sprintf("%s → %s %s (was %s)", cur, base, newRate, was)
		} else {
			summary = fmt.Sprintf("%s → %s %s (new)", cur, base, newRate)
		}
		if err := stageRateProposal(ctx, f.svc, fxRateProposalKind, fxRateTargetType, ws,
			fxRateProposal{FromCurrency: cur, Rate: newRate}, summary); err != nil {
			return fmt.Errorf("fx refresh: stage %s: %w", cur, err)
		}
		staged++
	}
	f.log.Info("fx rate refresh complete", "staged", staged, "tracked", len(current), "bootstrap", len(current) == 0)
	return nil
}

// plan resolves the base currency, the symbols to fetch, and the currently
// tracked rate per currency for the diff. With a non-empty sheet it derives all
// three from the tracked rows (base = every row's ToCurrency). With an empty
// sheet it reads the workspace base and uses the configured candidate set
// (dropping the base itself — a base→base rate is always 1), leaving currentRate
// empty so every fetched rate is a fresh proposal.
func (f fxRefresh) plan(ctx context.Context, current []deals.FxRateRow) (base string, symbols []string, currentRate map[string]string, err error) {
	if len(current) > 0 {
		base = current[0].ToCurrency
		symbols = make([]string, 0, len(current))
		currentRate = make(map[string]string, len(current))
		for _, r := range current {
			symbols = append(symbols, r.FromCurrency)
			currentRate[r.FromCurrency] = r.Rate
		}
		return base, symbols, currentRate, nil
	}
	if len(f.bootstrapCurrencies) == 0 {
		return "", nil, nil, nil
	}
	base, err = f.store.WorkspaceBaseCurrency(ctx)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fx refresh: %w", err)
	}
	symbols = make([]string, 0, len(f.bootstrapCurrencies))
	for _, c := range f.bootstrapCurrencies {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || c == base {
			continue
		}
		symbols = append(symbols, c)
	}
	return base, symbols, map[string]string{}, nil
}

type fxRefreshWorker struct {
	river.WorkerDefaults[FxRateRefreshArgs]
	refresh fxRefresh
}

func (w *fxRefreshWorker) Work(ctx context.Context, job *river.Job[FxRateRefreshArgs]) error {
	return w.refresh.run(rateRefreshWorkerCtx(ctx, job.Args.WorkspaceID, job.Args.RequestedBy))
}

func newFxRefreshWorker(pool *pgxpool.Pool, client *fxsource.Client, bootstrapCurrencies []string, log *slog.Logger) *fxRefreshWorker {
	return &fxRefreshWorker{refresh: fxRefresh{
		store:               deals.NewStore(pool),
		svc:                 approvals.NewService(pool),
		client:              client,
		bootstrapCurrencies: bootstrapCurrencies,
		log:                 log,
	}}
}
