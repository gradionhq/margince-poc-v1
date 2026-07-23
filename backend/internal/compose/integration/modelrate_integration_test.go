// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ai_model_rate editor suite: strict append-forward writes with
// USD->µUSD conversion, the admin/ops read+write gate, audit-only write
// shape, and cross-tenant RLS isolation on the ai-owned price sheet.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func modelRateOf(rows []ai.ModelRateRow, provider, model string) (ai.ModelRateRow, bool) {
	for _, r := range rows {
		if r.Provider == provider && r.ModelID == model {
			return r, true
		}
	}
	return ai.ModelRateRow{}, false
}

func TestModelRateAppendForwardAndConversion(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	ctx := e.Admin()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	r1, err := store.SetModelRate(ctx, ai.SetModelRateInput{
		Provider: "anthropic", ModelID: "claude-opus-4-8",
		InputUsd: "5.00", OutputUsd: "25", CacheReadUsd: "0.5", CacheWriteUsd: "6.25",
		EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if r1.InputUsd != "5" || r1.OutputUsd != "25" || r1.CacheReadUsd != "0.5" || r1.CacheWriteUsd != "6.25" {
		t.Fatalf("got %+v, want 5/25/0.5/6.25", r1)
	}

	// The stored value is µUSD (5.00 USD/MTok -> 5_000_000).
	if n := e.WsCount(t, `SELECT count(*) FROM ai_model_rate WHERE provider='anthropic' AND model_id='claude-opus-4-8' AND input_per_mtok_microusd=5000000`); n != 1 {
		t.Fatalf("expected one row with 5_000_000 µUSD, got %d", n)
	}

	// Same day corrects in place; future date appends a new row.
	if _, err := store.SetModelRate(ctx, ai.SetModelRateInput{
		Provider: "anthropic", ModelID: "claude-opus-4-8",
		InputUsd: "4", OutputUsd: "25", CacheReadUsd: "0.5", CacheWriteUsd: "6.25", EffectiveDate: today,
	}); err != nil {
		t.Fatalf("correct: %v", err)
	}
	hist, err := store.ModelRateHistory(ctx, "anthropic", "claude-opus-4-8")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 || hist[0].InputUsd != "4" {
		t.Fatalf("history = %+v, want one row at input 4", hist)
	}

	if _, err := store.SetModelRate(ctx, ai.SetModelRateInput{
		Provider: "anthropic", ModelID: "claude-opus-4-8",
		InputUsd: "3", OutputUsd: "25", CacheReadUsd: "0.5", CacheWriteUsd: "6.25", EffectiveDate: today.AddDate(0, 0, 1),
	}); err != nil {
		t.Fatalf("future: %v", err)
	}
	latest, err := store.ListEffectiveModelRates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if row, ok := modelRateOf(latest, "anthropic", "claude-opus-4-8"); !ok || row.InputUsd != "3" {
		t.Fatalf("latest = %+v (ok=%v), want input 3", row, ok)
	}
}

func TestModelRateRejects(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	ctx := e.Admin()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	base := ai.SetModelRateInput{Provider: "anthropic", ModelID: "m", InputUsd: "1", OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0", EffectiveDate: today}

	assertInvalid := func(t *testing.T, in ai.SetModelRateInput) {
		t.Helper()
		_, err := store.SetModelRate(ctx, in)
		var v *ai.RateValidationError
		if !errors.As(err, &v) {
			t.Fatalf("expected RateValidationError, got %v", err)
		}
	}
	past := base
	past.EffectiveDate = today.AddDate(0, 0, -1)
	assertInvalid(t, past)
	noProvider := base
	noProvider.Provider = ""
	assertInvalid(t, noProvider)
	negPrice := base
	negPrice.InputUsd = "-1"
	assertInvalid(t, negPrice)
}

func TestModelRateWriteDeniedForNonAdmin(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	repCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	_, err := store.SetModelRate(repCtx, ai.SetModelRateInput{Provider: "anthropic", ModelID: "m", InputUsd: "1", OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0", EffectiveDate: time.Now().UTC()})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep write err = %v, want ErrPermissionDenied", err)
	}
}

func TestModelRateReadDeniedForNonAdmin(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	roCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, ReadOnlyPerms)
	if _, err := store.ListEffectiveModelRates(roCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("read_only list err = %v, want ErrPermissionDenied", err)
	}
}

func TestModelRateWritesAuditRow(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	if _, err := store.SetModelRate(e.Admin(), ai.SetModelRateInput{Provider: "anthropic", ModelID: "m", InputUsd: "1", OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type='ai_model_rate' AND action='create'`); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}

func TestModelRateCrossWorkspaceIsolation(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	if _, err := store.SetModelRate(e.Admin(), ai.SetModelRateInput{Provider: "anthropic", ModelID: "m", InputUsd: "1", OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0", EffectiveDate: time.Now().UTC()}); err != nil {
		t.Fatalf("set in workspace A: %v", err)
	}
	wsB, _ := seedSecondWorkspace(t, OwnerConn(t))
	ctxB := principal.WithWorkspaceID(context.Background(), wsB)
	var n int
	if err := database.WithWorkspaceTx(ctxB, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctxB, `SELECT count(*) FROM ai_model_rate WHERE provider='anthropic' AND model_id='m'`).Scan(&n)
	}); err != nil {
		t.Fatalf("count in workspace B: %v", err)
	}
	if n != 0 {
		t.Fatalf("workspace B sees %d rows, want 0 (RLS leak)", n)
	}
}
