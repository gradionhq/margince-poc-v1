// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The FX refresh producer over real Postgres: it AI-extracts rates from a
// fetched page, prices only the currencies the workspace tracks (or the
// bootstrap candidate set on an empty sheet), diffs against the rate in force
// TODAY, and stages one proposal per changed rate carrying the prior rate as a
// precondition; a fresh diff supersedes a stale pending one and an unchanged
// re-run stages nothing new. webread's SSRF guard refuses loopback, so the page
// fetcher and the model are stubbed rather than served over httptest — the page
// text is irrelevant here (the fake brain returns the extracted pairs directly).

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubFetcher returns fixed page text for any URL (webread's SSRF guard would
// refuse a loopback httptest server). The text is inert — the fake brain below
// returns the extracted pairs regardless of it.
type stubFetcher struct{}

func (stubFetcher) Fetch(context.Context, string) (webread.Doc, error) {
	return webread.Doc{Text: "rates page"}, nil
}

// fixedBrain returns fixed extractor JSON, ignoring the request — it stands in
// for the rate_extract model lane.
type fixedBrain struct{ json string }

func (b fixedBrain) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: b.json}, nil
}

// fxRefreshWith builds the producer over the workspace pool with a fake brain
// that will "extract" the given pairs JSON.
func fxRefreshWith(e *integration.Env, reply string, bootstrap []string) fxRefresh {
	return fxRefresh{
		store:               deals.NewStore(e.Pool),
		svc:                 approvals.NewService(e.Pool),
		fetcher:             stubFetcher{},
		brain:               fixedBrain{json: reply},
		url:                 "https://rates.test/latest",
		bootstrapCurrencies: bootstrap,
		log:                 quietLog(),
	}
}

// pair is a shorthand for one extracted, grounded, confident pair.
func pair(from, to, rate string) string {
	return `{"from_currency":"` + from + `","to_currency":"` + to + `","rate":"` + rate + `","evidence":"s0","confidence":"0.9"}`
}

func fxReply(pairs ...string) string {
	out := `{"pairs":[`
	for i, p := range pairs {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out + `]}`
}

func TestFxRefreshStagesChangedRates(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	// The workspace tracks USD at 0.92 (base EUR).
	if _, err := deals.NewStore(e.Pool).SetFxRate(adminCtx,
		deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.92", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("seed USD: %v", err)
	}

	// The page states 1 EUR = 1.08 USD -> USD->EUR = 0.9259..., a change. JPY is
	// also on the page but untracked, so it must not be proposed.
	f := fxRefreshWith(e, fxReply(pair("EUR", "USD", "1.08"), pair("EUR", "JPY", "160.5")), nil)
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if n := fxPending(t, e); n != 1 {
		t.Fatalf("staged %d fx proposals, want 1 (only tracked+changed USD)", n)
	}
	// A re-run with the same rate stages nothing new (per-identity dedupe).
	if err := f.run(wctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := fxPending(t, e); n != 1 {
		t.Fatalf("after re-run staged %d, want still 1 (JoinPending dedupe)", n)
	}
}

func fxPending(t *testing.T, e *integration.Env) int {
	return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending'`)
}

func seedFx(ctx context.Context, t *testing.T, store *deals.Store, cur, rate string, eff time.Time) {
	t.Helper()
	if _, err := store.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: cur, Rate: rate, EffectiveDate: eff}); err != nil {
		t.Fatalf("seed %s@%s: %v", cur, rate, err)
	}
}

// The diff base is the rate in force TODAY, not the sheet head: a
// future-scheduled row must neither trigger a proposal when today's rate
// already matches the source, nor mask a real change to today's rate.
func TestFxRefreshDiffsAgainstEffectiveTodayNotSheetHead(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	store := deals.NewStore(e.Pool)
	tomorrow := time.Now().UTC().Add(24 * time.Hour)

	// USD: today matches the source; a future row differs (must NOT propose).
	seedFx(adminCtx, t, store, "USD", "0.8", time.Time{})
	seedFx(adminCtx, t, store, "USD", "0.95", tomorrow)
	// CHF: today differs from the source; a future row matches (MUST propose).
	seedFx(adminCtx, t, store, "CHF", "2.0", time.Time{})
	seedFx(adminCtx, t, store, "CHF", "0.5", tomorrow)

	// 1 EUR = 1.25 USD -> USD->EUR 0.8000000000 (matches today); 1 EUR = 2 CHF
	// -> CHF->EUR 0.5 (differs from today's 2.0).
	f := fxRefreshWith(e, fxReply(pair("EUR", "USD", "1.25"), pair("EUR", "CHF", "2")), nil)
	if err := f.run(rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())); err != nil {
		t.Fatalf("run: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending' AND expires_at > now()`); n != 1 {
		t.Fatalf("live proposals = %d, want 1 (CHF only: USD unchanged today, future row ignored)", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal'
			AND proposed_change->>'from_currency'='CHF'
			AND proposed_change->>'expected_prior_rate'='2.0000000000'`); n != 1 {
		t.Fatal("CHF proposal must carry today's effective rate as expected_prior_rate")
	}
}

// A second refresh with a different fetched rate replaces the first pending
// proposal for that currency instead of stacking a competitor whose late
// approval would restore the stale rate.
func TestFxRefreshSupersedesStalePendingProposal(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	store := deals.NewStore(e.Pool)
	seedFx(adminCtx, t, store, "USD", "0.9", time.Time{})

	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())
	for _, rate := range []string{
		"1.25", // 1 EUR = 1.25 USD -> USD->EUR 0.8000000000
		"1",    // 1 EUR = 1 USD    -> USD->EUR 1.0000000000
	} {
		f := fxRefreshWith(e, fxReply(pair("EUR", "USD", rate)), nil)
		if err := f.run(wctx); err != nil {
			t.Fatalf("run %s: %v", rate, err)
		}
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending' AND expires_at > now()`); n != 1 {
		t.Fatalf("live proposals = %d, want 1 (fresh diff supersedes the stale one)", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending' AND expires_at > now()
			AND proposed_change->>'rate'='1.0000000000'`); n != 1 {
		t.Fatal("the surviving proposal must be the fresher fetched rate")
	}
}

func TestFxRefreshBootstrapsEmptySheet(t *testing.T) {
	e := integration.Setup(t)

	// Base EUR, empty sheet. The page offers USD/GBP/CHF (and JPY, outside the
	// candidate set); EUR is the base and must be dropped from the candidates.
	reply := fxReply(
		pair("EUR", "USD", "1.08"), pair("EUR", "GBP", "0.85"),
		pair("EUR", "CHF", "0.96"), pair("EUR", "JPY", "160.5"),
	)
	f := fxRefreshWith(e, reply, []string{"USD", "GBP", "CHF", "EUR"})
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("bootstrap run: %v", err)
	}
	// USD/GBP/CHF are proposed; EUR (the base) and JPY (not a candidate) are not.
	if n := fxPending(t, e); n != 3 {
		t.Fatalf("bootstrap staged %d fx proposals, want 3 (USD/GBP/CHF, not base EUR or non-candidate JPY)", n)
	}
	// A re-run bootstraps nothing new — the proposals are live, so JoinPending
	// collapses each identical diff.
	if err := f.run(wctx); err != nil {
		t.Fatalf("second bootstrap run: %v", err)
	}
	if n := fxPending(t, e); n != 3 {
		t.Fatalf("after re-run staged %d, want still 3 (JoinPending dedupe)", n)
	}
}

func TestFxRefreshSkipsCandidatesTheSourceOmits(t *testing.T) {
	e := integration.Setup(t)

	// USX is ISO 4217-shaped (so it clears the config gate) but the page never
	// prices it. The refresh must stage USD and skip USX gracefully — no error,
	// no phantom proposal.
	f := fxRefreshWith(e, fxReply(pair("EUR", "USD", "1.08")), []string{"USD", "USX"})
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := fxPending(t, e); n != 1 {
		t.Fatalf("staged %d fx proposals, want 1 (USD only — USX is omitted by the source)", n)
	}
}

func TestFxRefreshEmptySheetNoBootstrapSetIsNoOp(t *testing.T) {
	e := integration.Setup(t)

	// A page that would answer if extracted — proving the no-op is the empty
	// sheet + empty candidate set, not a dead fetcher.
	f := fxRefreshWith(e, fxReply(pair("EUR", "USD", "1.08")), nil)
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("no-op run: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal'`); n != 0 {
		t.Fatalf("staged %d fx proposals, want 0 (empty sheet, no bootstrap set)", n)
	}
}

func TestFxRefreshDropsLowConfidenceAndCrossPairs(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	seedFx(adminCtx, t, deals.NewStore(e.Pool), "USD", "0.92", time.Now().UTC())

	// USD arrives only via a low-confidence pair and a cross-pair (USD/GBP,
	// neither side is the base EUR). Both are dropped, so nothing is staged.
	reply := fxReply(
		`{"from_currency":"EUR","to_currency":"USD","rate":"1.08","evidence":"s0","confidence":"0.3"}`,
		pair("USD", "GBP", "1.27"),
	)
	f := fxRefreshWith(e, reply, nil)
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())
	if err := f.run(wctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := fxPending(t, e); n != 0 {
		t.Fatalf("staged %d fx proposals, want 0 (low-confidence + cross-pair both dropped)", n)
	}
}
