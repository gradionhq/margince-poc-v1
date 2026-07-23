// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The FX refresh producer over real Postgres: it prices only the currencies
// the workspace already tracks, stages a proposal for each changed rate, and a
// re-run stages nothing new (per-identity JoinPending dedupe). When the sheet
// is still empty it bootstraps from a configured candidate set against the
// workspace base currency, so "Refresh from sources" is not a dead button on a
// fresh install.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/fxsource"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestFxRefreshStagesChangedRates(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	// The workspace tracks USD at 0.92.
	if _, err := deals.NewStore(e.Pool).SetFxRate(adminCtx,
		deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.92", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("seed USD: %v", err)
	}

	// The source reports 1 EUR = 1.08 USD -> USD->EUR = 0.9259..., a change.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"base":"EUR","rates":{"USD":1.08,"JPY":160.5}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	f := fxRefresh{
		store:  deals.NewStore(e.Pool),
		svc:    approvals.NewService(e.Pool),
		client: fxsource.New(srv.URL, srv.Client()),
		log:    quietLog(),
	}
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	pending := func() int {
		return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending'`)
	}
	// JPY is not tracked, so only the changed USD is proposed.
	if n := pending(); n != 1 {
		t.Fatalf("staged %d fx proposals, want 1 (only tracked+changed USD)", n)
	}

	// A re-run with the same rate stages nothing new (per-identity dedupe).
	if err := f.run(wctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := pending(); n != 1 {
		t.Fatalf("after re-run staged %d, want still 1 (JoinPending dedupe)", n)
	}
}

func seedFx(ctx context.Context, t *testing.T, store *deals.Store, cur, rate string, eff time.Time) {
	t.Helper()
	if _, err := store.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: cur, Rate: rate, EffectiveDate: eff}); err != nil {
		t.Fatalf("seed %s@%s: %v", cur, rate, err)
	}
}

func fxServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
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

	// 1/1.25 = USD->EUR 0.8000000000 (matches today), 1/2 = CHF->EUR 0.5.
	srv := fxServer(t, `{"base":"EUR","rates":{"USD":1.25,"CHF":2}}`)
	defer srv.Close()
	f := fxRefresh{
		store: store, svc: approvals.NewService(e.Pool),
		client: fxsource.New(srv.URL, srv.Client()), log: quietLog(),
	}
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

	svc := approvals.NewService(e.Pool)
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())
	for _, body := range []string{
		`{"base":"EUR","rates":{"USD":1.25}}`, // -> 0.8000000000
		`{"base":"EUR","rates":{"USD":1}}`,    // -> 1.0000000000
	} {
		srv := fxServer(t, body)
		f := fxRefresh{store: store, svc: svc, client: fxsource.New(srv.URL, srv.Client()), log: quietLog()}
		err := f.run(wctx)
		srv.Close()
		if err != nil {
			t.Fatalf("run %s: %v", body, err)
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

	// The workspace base is EUR and the sheet is empty (nothing seeded). The
	// source offers USD/GBP/CHF (and JPY, which is outside the candidate set).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"base":"EUR","rates":{"USD":1.08,"GBP":0.85,"CHF":0.96,"JPY":160.5}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	f := fxRefresh{
		store:  deals.NewStore(e.Pool),
		svc:    approvals.NewService(e.Pool),
		client: fxsource.New(srv.URL, srv.Client()),
		// EUR is the base and must be dropped from the candidate set; JPY is
		// not offered as a candidate, so neither is proposed.
		bootstrapCurrencies: []string{"USD", "GBP", "CHF", "EUR"},
		log:                 quietLog(),
	}
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("bootstrap run: %v", err)
	}
	pending := func() int {
		return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending'`)
	}
	// USD/GBP/CHF are proposed; EUR (the base) and JPY (not a candidate) are not.
	if n := pending(); n != 3 {
		t.Fatalf("bootstrap staged %d fx proposals, want 3 (USD/GBP/CHF, not base EUR or non-candidate JPY)", n)
	}

	// A re-run bootstraps nothing new — the proposals are live, so JoinPending
	// collapses each identical diff.
	if err := f.run(wctx); err != nil {
		t.Fatalf("second bootstrap run: %v", err)
	}
	if n := pending(); n != 3 {
		t.Fatalf("after re-run staged %d, want still 3 (JoinPending dedupe)", n)
	}
}

func TestFxRefreshSkipsCandidatesTheSourceOmits(t *testing.T) {
	e := integration.Setup(t)

	// USX is ISO 4217-shaped (so it clears the config gate) but unsupported, so
	// the source never prices it. The refresh must stage USD and skip USX
	// gracefully — no error, no phantom proposal.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"base":"EUR","rates":{"USD":1.08}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	f := fxRefresh{
		store:               deals.NewStore(e.Pool),
		svc:                 approvals.NewService(e.Pool),
		client:              fxsource.New(srv.URL, srv.Client()),
		bootstrapCurrencies: []string{"USD", "USX"},
		log:                 quietLog(),
	}
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal' AND status='pending'`); n != 1 {
		t.Fatalf("staged %d fx proposals, want 1 (USD only — USX is omitted by the source)", n)
	}
}

func TestFxRefreshEmptySheetNoBootstrapSetIsNoOp(t *testing.T) {
	e := integration.Setup(t)

	// A source that would answer if asked — proving the no-op is the empty
	// sheet + empty candidate set, not a dead client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"base":"EUR","rates":{"USD":1.08}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	f := fxRefresh{
		store:  deals.NewStore(e.Pool),
		svc:    approvals.NewService(e.Pool),
		client: fxsource.New(srv.URL, srv.Client()),
		// No candidate set configured: an empty sheet has nothing to refresh
		// and nothing to bootstrap — an honest no-op, never an invented rate.
		bootstrapCurrencies: nil,
		log:                 quietLog(),
	}
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())

	if err := f.run(wctx); err != nil {
		t.Fatalf("no-op run: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='fx_rate_proposal'`); n != 0 {
		t.Fatalf("staged %d fx proposals, want 0 (empty sheet, no bootstrap set)", n)
	}
}
