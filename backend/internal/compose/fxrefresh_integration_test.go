// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The FX refresh producer over real Postgres: it prices only the currencies
// the workspace already tracks, stages a proposal for each changed rate, and a
// re-run stages nothing new (per-identity HasPendingFor dedupe).

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
		_, _ = w.Write([]byte(`{"base":"EUR","rates":{"USD":1.08,"JPY":160.5}}`))
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
		t.Fatalf("after re-run staged %d, want still 1 (HasPendingFor dedupe)", n)
	}
}
