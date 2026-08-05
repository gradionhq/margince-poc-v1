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
	latest, err := store.ListLatestModelRates(ctx)
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
	if _, err := store.ListLatestModelRates(roCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
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

// modelRatePerms is a config-editing principal narrowed to ONE ai_model_rate
// grant row. The matrix distinguishes inserting a new (provider, model, day)
// from overwriting an existing one; only a principal missing one of the two
// grants can prove that the endpoint actually asks for the right one.
func modelRatePerms(g principal.ObjectGrant) principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"ops"},
		Objects:  map[string]principal.ObjectGrant{"ai_model_rate": g},
		RowScope: principal.RowScopeAll,
	}
}

// One endpoint, two grants: it inserts under `create` and overwrites under
// `update`, and neither grant substitutes for the other. Every refusal is the
// 403 sentinel, and a refused write leaves the sheet untouched. The fx_rate
// sheet carries the identical pair.
func TestModelRateCreateAndUpdateGrantsGateSeparately(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	setOn := func(ctx context.Context, input string, day time.Time) error {
		_, err := store.SetModelRate(ctx, ai.SetModelRateInput{
			Provider: "anthropic", ModelID: "m",
			InputUsd: input, OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0",
			EffectiveDate: day,
		})
		return err
	}
	creator := e.As(e.Rep1, nil, modelRatePerms(principal.ObjectGrant{Create: true, Read: true}))
	updater := e.As(e.Rep1, nil, modelRatePerms(principal.ObjectGrant{Update: true, Read: true}))

	// Nothing on the sheet for (anthropic/m, today): there is no price to
	// update, so the update-only principal is refused the insert.
	if err := setOn(updater, "1", today); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("update-only insert = %v, want ErrPermissionDenied", err)
	}
	if err := setOn(creator, "2", today); err != nil {
		t.Fatalf("create-only insert: %v", err)
	}
	// The row exists now, so the SAME call is an overwrite — which holding
	// create alone must not buy.
	if err := setOn(creator, "3", today); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("create-only overwrite = %v, want ErrPermissionDenied", err)
	}
	if err := setOn(updater, "4", today); err != nil {
		t.Fatalf("update-only overwrite: %v", err)
	}

	hist, err := store.ModelRateHistory(e.Admin(), "anthropic", "m")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 || hist[0].InputUsd != "4" {
		t.Fatalf("sheet = %+v, want one row at 4 USD/MTok (the refused writes wrote nothing)", hist)
	}

	// Holding both does both halves: a new day inserts, the same day overwrites.
	both := e.As(e.Rep1, nil, modelRatePerms(principal.ObjectGrant{Create: true, Read: true, Update: true}))
	if err := setOn(both, "5", today.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("both-grants insert on a new day: %v", err)
	}
	if err := setOn(both, "6", today); err != nil {
		t.Fatalf("both-grants overwrite: %v", err)
	}
}

// An overwrite is audited as the verb that admitted it, so
// audit_log.authorization_rule attributes the update grant rather than the
// create grant the insert used.
func TestModelRateOverwriteAuditsAsUpdate(t *testing.T) {
	e := Setup(t)
	store := ai.NewRateStore(e.Pool)
	ctx := e.Admin()
	for _, price := range []string{"1", "2"} {
		if _, err := store.SetModelRate(ctx, ai.SetModelRateInput{
			Provider: "anthropic", ModelID: "m",
			InputUsd: price, OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0",
			EffectiveDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("set %s: %v", price, err)
		}
	}
	for action, want := range map[string]int{"create": 1, "update": 1} {
		if n := e.WsCount(t, `SELECT count(*) FROM audit_log
			WHERE entity_type='ai_model_rate' AND action='`+action+`'`); n != want {
			t.Fatalf("audit rows for %s = %d, want %d", action, n, want)
		}
	}
	// The update audit carries what it displaced — an overwrite whose before
	// image is null is an unusable ledger entry.
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE entity_type='ai_model_rate' AND action='update'
		  AND (before->>'input_microusd')::bigint = 1000000`); n != 1 {
		t.Fatalf("update audit rows carrying the displaced price = %d, want 1", n)
	}
}
