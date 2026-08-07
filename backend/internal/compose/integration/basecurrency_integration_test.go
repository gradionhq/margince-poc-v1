// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The workspace base currency: freely correctable until a closed deal has
// frozen a conversion against it, and immovable afterwards.
//
// The lock is not a policy preference. Every closed deal stores
// fx_rate_to_base against the base in force when it closed (DM-FX-4), so
// changing the base later would silently restate what those deals were worth.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

func TestTheBaseCurrencyIsCorrectableWhileNoDealHasFrozenARate(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()

	before, err := e.Deals.BaseCurrencyStatus(ctx)
	if err != nil {
		t.Fatalf("read base currency: %v", err)
	}
	if before.Locked {
		t.Fatalf("a fresh workspace has no frozen rates, got locked with %d", before.FrozenDeals)
	}

	after, err := e.Deals.SetBaseCurrency(ctx, "chf")
	if err != nil {
		t.Fatalf("set base currency: %v", err)
	}
	// Lower case in, canonical out: the sheet and the deal rows both key on
	// the ISO code, so one spelling has to win at the boundary.
	if after.BaseCurrency != "CHF" {
		t.Errorf("base currency = %q, want CHF", after.BaseCurrency)
	}

	reread, err := e.Deals.BaseCurrencyStatus(ctx)
	if err != nil {
		t.Fatalf("re-read base currency: %v", err)
	}
	if reread.BaseCurrency != "CHF" {
		t.Errorf("re-read base currency = %q, want CHF — the write did not persist", reread.BaseCurrency)
	}
}

func TestTheBaseCurrencyLocksOnceADealHasFrozenARateAgainstIt(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()

	// A won deal is the state that freezes a rate: the conversion it stored is
	// against the base in force on the day it closed.
	st := seedRollupStages(t, e)
	e.WsExec(t, `
		INSERT INTO deal (id, workspace_id, name, amount_minor, currency, fx_rate_to_base, fx_rate_date,
			pipeline_id, stage_id, status, closed_at, source, captured_by)
		VALUES ($1, $2, 'Voltaq retrofit', 250000, 'EUR', 1, DATE '2026-01-15',
			$3, $4, 'won', TIMESTAMPTZ '2026-01-15 12:00:00Z', 'manual', 'human:test')`,
		ids.NewV7(), e.WS, st.pipeline, st.won)

	status, err := e.Deals.BaseCurrencyStatus(ctx)
	if err != nil {
		t.Fatalf("read base currency: %v", err)
	}
	if !status.Locked || status.FrozenDeals != 1 {
		t.Fatalf("status = %+v, want locked with 1 frozen deal — a surface must be able to say so before the refusal", status)
	}

	_, err = e.Deals.SetBaseCurrency(ctx, "CHF")
	if !errors.Is(err, apperrors.ErrBaseCurrencyLocked) {
		t.Fatalf("got %v, want the locked sentinel — changing the base would restate a closed deal's worth", err)
	}
}

func TestAMalformedBaseCurrencyIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()

	before, err := e.Deals.BaseCurrencyStatus(ctx)
	if err != nil {
		t.Fatalf("read base currency: %v", err)
	}

	var parse *values.ParseError
	if _, err := e.Deals.SetBaseCurrency(ctx, "euro"); !errors.As(err, &parse) {
		t.Fatalf("got %v, want a 422 naming base_currency", err)
	}
	if parse.Code != "base_currency_malformed" {
		t.Errorf("code = %q, want base_currency_malformed", parse.Code)
	}

	after, err := e.Deals.BaseCurrencyStatus(ctx)
	if err != nil {
		t.Fatalf("re-read base currency: %v", err)
	}
	if after.BaseCurrency != before.BaseCurrency {
		t.Errorf("base currency moved to %q on a refused write", after.BaseCurrency)
	}
}

func TestReadingTheBaseCurrencyNeedsTheRateSheetGrant(t *testing.T) {
	e := Setup(t)

	// A rep holds no fx_rate grant: the base currency is priced-sheet config,
	// governed by the same authority as the rates that convert into it.
	if _, err := e.Deals.BaseCurrencyStatus(e.As(ids.NewV7(), nil, RepPerms)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("got %v, want permission denied", err)
	}
	if _, err := e.Deals.SetBaseCurrency(e.As(ids.NewV7(), nil, RepPerms), "CHF"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("got %v, want permission denied", err)
	}
}
