// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The fx_rate editor suite: strict append-forward writes (today or later,
// same-day correction, immutable past), the admin/ops read+write gate, and
// cross-tenant RLS isolation on the deals-owned fx_rate price sheet.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func fxRateOf(rows []deals.FxRateRow, from string) (string, bool) {
	for _, r := range rows {
		if r.FromCurrency == from {
			return r.Rate, true
		}
	}
	return "", false
}

func TestFxRateAppendForward(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	r1, err := e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "usd", Rate: "0.9150", EffectiveDate: today})
	if err != nil {
		t.Fatalf("set USD: %v", err)
	}
	if r1.FromCurrency != "USD" || r1.ToCurrency != "EUR" {
		t.Fatalf("got %+v, want USD→EUR", r1)
	}

	// Same UTC day → corrects the row in place (one row survives).
	if _, err := e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9200", EffectiveDate: today}); err != nil {
		t.Fatalf("correct USD: %v", err)
	}
	hist, err := e.Deals.FxRateHistory(ctx, "USD")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1 (same-day correction)", len(hist))
	}
	if hist[0].Rate != "0.9200000000" {
		t.Fatalf("rate = %q, want 0.9200000000", hist[0].Rate)
	}

	// Future date → a new effective-dated row.
	if _, err := e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9300", EffectiveDate: today.AddDate(0, 0, 1)}); err != nil {
		t.Fatalf("future USD: %v", err)
	}
	latest, err := e.Deals.ListLatestFxRates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rate, ok := fxRateOf(latest, "USD"); !ok || rate != "0.9300000000" {
		t.Fatalf("latest USD = %q (ok=%v), want 0.9300000000", rate, ok)
	}
}

func TestFxRateRejectsPastBaseAndNonPositive(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	assertInvalid := func(t *testing.T, err error) {
		t.Helper()
		var v *deals.FxRateValidationError
		if !errors.As(err, &v) {
			t.Fatalf("expected FxRateValidationError, got %v", err)
		}
	}
	_, err := e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9", EffectiveDate: today.AddDate(0, 0, -1)})
	assertInvalid(t, err) // past
	_, err = e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "EUR", Rate: "1", EffectiveDate: today})
	assertInvalid(t, err) // from == base
	_, err = e.Deals.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "USD", Rate: "0", EffectiveDate: today})
	assertInvalid(t, err) // not > 0
}

func TestFxRateWriteDeniedForNonAdmin(t *testing.T) {
	e := Setup(t)
	repCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	_, err := e.Deals.SetFxRate(repCtx, deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9", EffectiveDate: time.Now().UTC()})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep write err = %v, want ErrPermissionDenied", err)
	}
}

func TestFxRateReadDeniedForNonAdmin(t *testing.T) {
	e := Setup(t)
	roCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, ReadOnlyPerms)
	if _, err := e.Deals.ListLatestFxRates(roCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("read_only list err = %v, want ErrPermissionDenied", err)
	}
}

func TestFxRateWritesAuditRow(t *testing.T) {
	e := Setup(t)
	if _, err := e.Deals.SetFxRate(e.Admin(), deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type='fx_rate' AND action='create'`); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}

func TestFxRateCrossWorkspaceIsolation(t *testing.T) {
	e := Setup(t)
	if _, err := e.Deals.SetFxRate(e.Admin(), deals.SetFxRateInput{FromCurrency: "USD", Rate: "0.9", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("set in workspace A: %v", err)
	}
	// A second tenant must not see workspace A's row (FORCE RLS on fx_rate).
	wsB, _ := seedSecondWorkspace(t, OwnerConn(t))
	ctxB := principal.WithWorkspaceID(context.Background(), wsB)
	var n int
	if err := database.WithWorkspaceTx(ctxB, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctxB, `SELECT count(*) FROM fx_rate WHERE from_currency='USD'`).Scan(&n)
	}); err != nil {
		t.Fatalf("count in workspace B: %v", err)
	}
	if n != 0 {
		t.Fatalf("workspace B sees %d USD rows, want 0 (RLS leak)", n)
	}
}
