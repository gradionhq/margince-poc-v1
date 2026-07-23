// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The model-cost refresh producer over real Postgres with an offline fake
// brain: a changed model and a brand-new model are proposed, while an
// ungrounded (no-evidence) and a low-confidence extraction are dropped
// (no-guess), and nothing is written to the sheet (only approvals staged).

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type fakeBrain struct{ text string }

func (f fakeBrain) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: f.text}, nil
}

type fakeFetcher struct{ text string }

func (f fakeFetcher) Fetch(_ context.Context, _ string) (string, error) { return f.text, nil }

func TestModelCostRefreshStagesChangedAndDropsUngrounded(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	rates := ai.NewRateStore(e.Pool)

	// The sheet currently prices opus input at 5.
	if _, err := rates.SetModelRate(adminCtx, ai.SetModelRateInput{
		Provider: "anthropic", ModelID: "claude-opus-4-8",
		InputUsd: "5", OutputUsd: "25", CacheReadUsd: "0.5", CacheWriteUsd: "6.25",
		EffectiveDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed opus: %v", err)
	}

	// The extraction: opus changed (5->4), a new model, one ungrounded (no
	// evidence), one low-confidence — only the first two should stage.
	extraction := `{"models":[
      {"provider":"anthropic","model_id":"claude-opus-4-8","input_per_mtok":"4","output_per_mtok":"25","cache_read_per_mtok":"0.5","cache_write_per_mtok":"6.25","evidence":"s0","confidence":"0.95"},
      {"provider":"anthropic","model_id":"claude-new","input_per_mtok":"2","output_per_mtok":"10","cache_read_per_mtok":"0","cache_write_per_mtok":"0","evidence":"s1","confidence":"0.9"},
      {"provider":"anthropic","model_id":"ungrounded","input_per_mtok":"1","output_per_mtok":"1","cache_read_per_mtok":"0","cache_write_per_mtok":"0","evidence":"","confidence":"0.9"},
      {"provider":"anthropic","model_id":"lowconf","input_per_mtok":"1","output_per_mtok":"1","cache_read_per_mtok":"0","cache_write_per_mtok":"0","evidence":"s2","confidence":"0.2"}
    ]}`

	m := modelCostRefresh{
		rates:   rates,
		svc:     approvals.NewService(e.Pool),
		fetcher: fakeFetcher{text: "some pricing page text"},
		brain:   fakeBrain{text: extraction},
		sources: []pricingSource{{Provider: "anthropic", URL: "https://prices.test/pricing"}},
		log:     quietLog(),
	}
	wctx := rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())
	if err := m.run(wctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	pending := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='ai_model_rate_proposal' AND status='pending'`)
	if pending != 2 {
		t.Fatalf("staged %d model proposals, want 2 (opus changed + new; ungrounded + low-confidence dropped)", pending)
	}
	// The producer stages only — nothing new is written to the sheet.
	if n := e.WsCount(t, `SELECT count(*) FROM ai_model_rate WHERE model_id IN ('claude-new','ungrounded','lowconf')`); n != 0 {
		t.Fatalf("producer wrote %d sheet rows, want 0 (stage-only)", n)
	}
}

func seedModelRate(t *testing.T, ctx context.Context, rates *ai.RateStore, provider, modelID, inputUsd string, eff time.Time) {
	t.Helper()
	if _, err := rates.SetModelRate(ctx, ai.SetModelRateInput{
		Provider: provider, ModelID: modelID,
		InputUsd: inputUsd, OutputUsd: "25", CacheReadUsd: "0", CacheWriteUsd: "0",
		EffectiveDate: eff,
	}); err != nil {
		t.Fatalf("seed %s/%s@%s: %v", provider, modelID, inputUsd, err)
	}
}

func extractionFixture(provider, modelID, inputUsd string) string {
	return `{"models":[{"provider":"` + provider + `","model_id":"` + modelID +
		`","input_per_mtok":"` + inputUsd +
		`","output_per_mtok":"25","cache_read_per_mtok":"0","cache_write_per_mtok":"0","evidence":"s0","confidence":"0.95"}]}`
}

func runModelRefresh(t *testing.T, e *integration.Env, extraction string) {
	t.Helper()
	m := modelCostRefresh{
		rates:   ai.NewRateStore(e.Pool),
		svc:     approvals.NewService(e.Pool),
		fetcher: fakeFetcher{text: "pricing page text"},
		brain:   fakeBrain{text: extraction},
		sources: []pricingSource{{Provider: "acme", URL: "https://prices.test/pricing"}},
		log:     quietLog(),
	}
	if err := m.run(rateRefreshWorkerCtx(context.Background(), e.WS, e.Rep1.String())); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// The diff base is the price in force TODAY: a future-scheduled row must not
// mask a change to today's price, the proposal carries today's buckets as
// expected_prior, and a re-run extracting a different price supersedes the
// first pending proposal instead of competing with it.
func TestModelRefreshDiffsAgainstEffectiveTodayAndSupersedes(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	rates := ai.NewRateStore(e.Pool)
	tomorrow := time.Now().UTC().Add(24 * time.Hour)

	// Today the model costs input=5; tomorrow a scheduled row already says 6.
	seedModelRate(t, adminCtx, rates, "acme", "m1", "5", time.Time{})
	seedModelRate(t, adminCtx, rates, "acme", "m1", "6", tomorrow)

	// The page states input=6: equal to the future row, but a diff vs TODAY.
	runModelRefresh(t, e, extractionFixture("acme", "m1", "6"))
	livePending := func() int {
		return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='ai_model_rate_proposal' AND status='pending' AND expires_at > now()`)
	}
	if livePending() != 1 {
		t.Fatalf("live proposals = %d, want 1 (future row must not mask today's diff)", livePending())
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='ai_model_rate_proposal'
			AND proposed_change#>>'{expected_prior,input_per_mtok}'='5'`); n != 1 {
		t.Fatal("proposal must carry today's price as expected_prior")
	}

	// A later run extracts input=7: the fresher diff replaces the pending 6.
	runModelRefresh(t, e, extractionFixture("acme", "m1", "7"))
	if livePending() != 1 {
		t.Fatalf("live proposals after re-run = %d, want 1 (stale diff superseded)", livePending())
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind='ai_model_rate_proposal' AND status='pending' AND expires_at > now()
			AND proposed_change->>'input_per_mtok'='7'`); n != 1 {
		t.Fatal("the surviving proposal must be the fresher extraction")
	}
}
