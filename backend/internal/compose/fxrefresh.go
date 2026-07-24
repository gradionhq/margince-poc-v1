// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
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

// fxRefresh is the FX producer: fetch the configured rates page, AI-extract the
// currency pairs it states (evidence-cited, confidence-gated), normalize each to
// from->base, diff against the rate in force today, and stage a proposal per
// changed rate. It has two modes on one path — refresh the currencies the sheet
// already tracks, or, when the sheet is empty, bootstrap the configured
// candidate set against the workspace base so the admin button is not dead on a
// fresh install. Either way a human approves every proposal — a rate is never
// invented, only proposed from the source. A nil fetcher/brain or empty URL (no
// source configured) is an honest no-op.
type fxRefresh struct {
	store   *deals.Store
	svc     *approvals.Service
	fetcher pageFetcher
	brain   completer
	url     string
	// bootstrapCurrencies is the candidate foreign-currency set proposed when
	// the sheet is empty (there is nothing tracked to derive symbols from).
	// Empty ⇒ an empty sheet stays a no-op (never a fabricated rate).
	bootstrapCurrencies []string
	log                 *slog.Logger
}

func (f fxRefresh) run(ctx context.Context) error {
	if f.fetcher == nil || f.brain == nil || f.url == "" {
		f.log.Info("fx rate refresh skipped: no FX source or model configured")
		return nil
	}
	current, err := f.store.ListLatestFxRates(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: read tracked currencies: %w", err)
	}

	base, symbols, err := f.plan(ctx, current)
	if err != nil {
		return err
	}
	if len(symbols) == 0 {
		// Either an empty sheet with no candidate set, or a candidate set that
		// held only the base currency — nothing to price.
		f.log.Info("fx rate refresh: nothing to refresh", "tracked", len(current))
		return nil
	}

	// Diff against what is in force TODAY, not the sheet head: approval writes
	// effective today, so a future-scheduled row must neither mask a real
	// change nor manufacture an ineffective proposal. Empty on a bootstrap run
	// (empty sheet) — every fetched rate is then a fresh proposal.
	effective, err := f.store.ListEffectiveFxRates(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: read effective rates: %w", err)
	}
	priorRate := make(map[string]string, len(effective))
	for _, r := range effective {
		priorRate[r.FromCurrency] = r.Rate
	}

	pairs, err := f.extract(ctx)
	if err != nil {
		return fmt.Errorf("fx refresh: %w", err)
	}
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	fetched := f.collect(base, pairs, want)

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
	f.log.Info("fx rate refresh complete", "staged", staged, "tracked", len(current), "bootstrap", len(current) == 0)
	return nil
}

// extract fetches the configured page and returns the model's extracted pairs
// (raw — gating and anchoring happen in collect). The page text is wrapped in
// the <untrusted> data envelope so a hostile page cannot break out of it.
func (f fxRefresh) extract(ctx context.Context) ([]extractedFxPair, error) {
	text, err := f.fetcher.Fetch(ctx, f.url)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	req := model.Request{
		System: fxExtractSystem,
		Messages: []model.Message{{
			Role:    chatRoleUser,
			Content: "<untrusted>\n" + numberPassages(text) + "\n</untrusted>",
		}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: fxExtractSchema,
		SecretStripper: ai.NewSecretStripper(),
	}
	resp, err := f.brain.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	return parseFxExtraction(resp.Text)
}

// collect gates, anchors, and normalizes the extracted pairs into a
// currency->(from->base rate) map restricted to the currencies we asked for.
// Every dropped pair is named at Warn with the actual reason (ungrounded,
// un-anchorable cross-pair, or unusable rate), and a requested currency the
// page never priced is called out too — no gap stays silent.
func (f fxRefresh) collect(base string, pairs []extractedFxPair, want map[string]bool) map[string]string {
	fetched := make(map[string]string, len(want))
	for _, p := range pairs {
		if !fxPairAccepted(p) {
			continue // no-guess: ungrounded or low-confidence
		}
		cur, invert, ok := fxAnchor(base, p)
		if !ok {
			// A cross-pair (neither side is the base) cannot be expressed
			// against the base — dropped rather than guessed. Name it.
			f.log.Warn("fx rate refresh: dropped a cross-pair not anchorable to base",
				"from", p.FromCurrency, "to", p.ToCurrency, "base", base)
			continue
		}
		if !want[cur] {
			continue // not tracked / not a bootstrap candidate
		}
		rate, err := fxRateString(p.Rate, invert)
		if err != nil {
			// Anchored, but the stated rate is unusable (garbage decimal,
			// rounds to zero, or over numeric(20,10)) — dropped, not staged.
			f.log.Warn("fx rate refresh: dropped a pair with an unusable rate",
				"currency", cur, "rate", p.Rate, "base", base, "err", err)
			continue
		}
		fetched[cur] = rate
	}
	// A requested currency the page did not price stages no proposal — name it
	// rather than let the gap stay silent.
	for cur := range want {
		if _, ok := fetched[cur]; !ok {
			f.log.Warn("fx rate refresh: source priced no rate for a requested currency — nothing staged for it",
				"currency", cur, "base", base)
		}
	}
	return fetched
}

// plan resolves the base currency and the symbols to fetch. With a non-empty
// sheet it derives both from the tracked rows (base = every row's ToCurrency).
// With an empty sheet it reads the workspace base and uses the configured
// candidate set (dropping the base itself — a base→base rate is always 1) so a
// fresh install can bootstrap; an empty candidate set leaves symbols empty and
// the run no-ops. The diff base is read separately (effective-as-of-today), so
// plan does not compute it.
func (f fxRefresh) plan(ctx context.Context, current []deals.FxRateRow) (base string, symbols []string, err error) {
	if len(current) > 0 {
		base = current[0].ToCurrency
		symbols = make([]string, 0, len(current))
		for _, r := range current {
			symbols = append(symbols, r.FromCurrency)
		}
		return base, symbols, nil
	}
	if len(f.bootstrapCurrencies) == 0 {
		return "", nil, nil
	}
	base, err = f.store.WorkspaceBaseCurrency(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("fx refresh: %w", err)
	}
	symbols = make([]string, 0, len(f.bootstrapCurrencies))
	for _, c := range f.bootstrapCurrencies {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || c == base {
			continue
		}
		symbols = append(symbols, c)
	}
	return base, symbols, nil
}

type fxRefreshWorker struct {
	river.WorkerDefaults[FxRateRefreshArgs]
	refresh fxRefresh
}

func (w *fxRefreshWorker) Work(ctx context.Context, job *river.Job[FxRateRefreshArgs]) error {
	return w.refresh.run(rateRefreshWorkerCtx(ctx, job.Args.WorkspaceID, job.Args.RequestedBy))
}

func newFxRefreshWorker(pool *pgxpool.Pool, fetcher pageFetcher, brain completer, url string, bootstrapCurrencies []string, log *slog.Logger) *fxRefreshWorker {
	return &fxRefreshWorker{refresh: fxRefresh{
		store:               deals.NewStore(pool),
		svc:                 approvals.NewService(pool),
		fetcher:             fetcher,
		brain:               brain,
		url:                 url,
		bootstrapCurrencies: bootstrapCurrencies,
		log:                 log,
	}}
}
