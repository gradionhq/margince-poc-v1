// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package costestimate

// ADR-0068 phase 2's real-Postgres proof: the estimator composes five RLS reads
// (served ai_call totals, ai_model_rate, the router's bindings,
// capture_labeled_at counts, and a connection's capture_backfill yields) into a
// priced backfill estimate. This lane exercises the SQL a pure-Go fake cannot:
// the served-row filter, the labeled-message denominator, connection-scoped
// yields (no cross-connection blend), rate-less exclusion, the cold-start floor,
// and cross-workspace invisibility carried by RLS alone.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// clockAt is the estimator's injected clock — the 7-day window and the rate
// as-of day both derive from it, so the fixtures below place their history
// inside [now-7d, now] and their rates before it.
var clockAt = fixedClock{t: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}

// inWindow is a served-history instant safely inside the trailing 7-day window;
// rateDay is an effective date the fixtures' rates all predate.
var (
	inWindow = clockAt.t.Add(-2 * 24 * time.Hour)
	rateDay  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

type estEnv struct {
	owner  *pgx.Conn
	pool   *pgxpool.Pool
	router *ai.Router
}

func setupEstimator(t *testing.T) *estEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// A router whose tiers bind to the offline fake provider but carry DISTINCT
	// model names per tier, so BoundLadder / CurrentModelForTier resolve real
	// (provider, model) identities the served slices and rates are keyed on —
	// no network, no API keys.
	router, err := ai.NewLocalRouter(ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierLocalSmall: {Provider: ai.ProviderFake, Model: "local-model"},
			ai.TierCheapCloud: {Provider: ai.ProviderFake, Model: "cloud-model"},
			ai.TierPremium:    {Provider: ai.ProviderFake, Model: "premium-model"},
		},
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake, Model: "embed-model"}},
	})
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	return &estEnv{owner: owner, pool: pool, router: router}
}

// newEstimatorOverPool wires the estimator over the real stores — the same
// construction B6 wires in compose.
func (e *estEnv) newEstimator() *Estimator {
	return NewEstimator(
		ai.NewCallReadStore(e.pool),
		ai.NewRateStore(e.pool),
		e.router,
		activities.NewStore(e.pool),
		capture.NewRegistry(e.pool, nil, nil, nil),
		clockAt,
	)
}

func (e *estEnv) seedWorkspace(t *testing.T) (ids.UUID, context.Context) {
	t.Helper()
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'CostEst', $2, 'EUR')`,
		ws, "ce-"+ws.String()); err != nil {
		t.Fatal(err)
	}
	return ws, principal.WithWorkspaceID(context.Background(), ws)
}

func (e *estEnv) seedUser(t *testing.T, ws ids.UUID) ids.UserID {
	t.Helper()
	uid := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		uid, ws, uid.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.UserKind](uid)
}

// seedConnection inserts a connected capture_connection for (provider, user) and
// returns its id.
func (e *estEnv) seedConnection(t *testing.T, ws ids.UUID, user ids.UserID, provider string) ids.UUID {
	t.Helper()
	connID := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO capture_connection (id, workspace_id, provider, user_id, scopes, status)
		VALUES ($1, $2, $3, $4, '{}', 'connected')`,
		connID, ws, provider, user.UUID); err != nil {
		t.Fatal(err)
	}
	return connID
}

// seedBackfill inserts a completed capture_backfill run — the connection's
// representative yields.
func (e *estEnv) seedBackfill(t *testing.T, ws, connID ids.UUID, windowMonths int, scanned, captured, people, orgs int) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO capture_backfill (workspace_id, connection_id, window_months, after_date, status,
		  scanned, captured, people_created, organizations_created, started_at, completed_at)
		VALUES ($1, $2, $3, $4, 'done', $5, $6, $7, $8, now(), now())`,
		ws, connID, windowMonths, rateDay, scanned, captured, people, orgs); err != nil {
		t.Fatal(err)
	}
}

type callRow struct {
	task                                                ai.Task
	tier                                                ai.Tier
	provider, model                                     string
	tokensIn, tokensOut, cachedTokens, cacheWriteTokens int
	errorSentinel                                       string // "" ⇒ NULL (a clean served row)
}

// insertCall seeds one served ai_call row (occurred_at inWindow, no cache hit).
// errorSentinel defaults to NULL; set it to 'metering_failed' to seed a call the
// model served (tokens spent) whose only failure was the meter write.
func (e *estEnv) insertCall(t *testing.T, ws ids.UUID, c callRow) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO ai_call (workspace_id, task, tier, provider, model_id, request_fingerprint,
		  tokens_in, tokens_out, cached_tokens, cache_write_tokens, cache_hit, occurred_at, logical_call_id, error_sentinel)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,false,$11,$12,NULLIF($13,''))`,
		ws, string(c.task), string(c.tier), c.provider, c.model, "fp-"+ids.NewV7().String(),
		c.tokensIn, c.tokensOut, c.cachedTokens, c.cacheWriteTokens, inWindow, ids.NewV7(), c.errorSentinel); err != nil {
		t.Fatalf("insert call %+v: %v", c, err)
	}
}

// insertLabeledActivity seeds one activity stamped with capture_labeled_at in
// the window — classify's exact observed-units denominator.
func (e *estEnv) insertLabeledActivity(t *testing.T, ws ids.UUID) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (workspace_id, kind, source, captured_by, capture_labeled_at)
		VALUES ($1, 'email', 'gmail', 'system', $2)`, ws, inWindow); err != nil {
		t.Fatal(err)
	}
}

// insertRate seeds one ai_model_rate for a fake-provider model (every fixture
// here rates the offline fake router's bindings, so the provider is fixed).
func (e *estEnv) insertRate(t *testing.T, ws ids.UUID, model string, in, out int64) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO ai_model_rate (workspace_id, provider, model_id, input_per_mtok_microusd,
		  output_per_mtok_microusd, cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date)
		VALUES ($1,$2,$3,$4,$5,0,0,$6)`,
		ws, ai.ProviderFake, model, in, out, rateDay); err != nil {
		t.Fatal(err)
	}
}

// TestEstimatorPricesObservedHistory is the hand-computed cost proof: classify
// priced at cloud-model, embeddings at embed-model, enrich at a real $0
// local-model — every task observed and priced, yields present, over real PG.
//
// This fixture seeds people_created=10 / organizations_created=2 — the
// FUTURE-populated-counters state. Production does NOT reach it today: the
// backfill loop (capture/backfill.go RunBackfillStep) never increments those
// counters, so a real completed run carries 0 (see the follow-up flag in the
// PR and TestEstimatorEnrichFloorsWhenPeopleCreatedZero for today's reality).
// The case is retained so the observed-enrich path stays covered for when the
// counters are populated.
func TestEstimatorPricesObservedHistory(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 80, 10, 2)

	// Rates: cloud + embed priced, local a real $0.
	e.insertRate(t, ws, "cloud-model", 1_000_000, 2_000_000)
	e.insertRate(t, ws, "embed-model", 500_000, 0)
	e.insertRate(t, ws, "local-model", 0, 0)

	// One served slice per task (Calls=1 each), plus four labeled messages.
	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 2_000_000, tokensOut: 200_000})
	e.insertCall(t, ws, callRow{task: ai.TaskEnrich, tier: ai.TierLocalSmall, provider: ai.ProviderFake, model: "local-model", tokensIn: 500_000, tokensOut: 50_000})
	e.insertCall(t, ws, callRow{task: ai.TaskEmbeddings, tier: ai.TierEmbedLane, provider: ai.ProviderFake, model: "embed-model", tokensIn: 1_000_000})
	for i := 0; i < 4; i++ {
		e.insertLabeledActivity(t, ws)
	}

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}

	// classify: units = 100×80/100 = 80; denom = labeled = 4; pricedDenom = 4.
	classifyMicro := ai.PriceCall(ai.Usage{TokensIn: 2_000_000, TokensOut: 200_000},
		ai.ModelRate{InputPerMTokMicroUSD: 1_000_000, OutputPerMTokMicroUSD: 2_000_000}) * 80 / 4
	// embeddings: units = 100×(80+10+2)/100 = 92; denom = 1 call; pricedDenom = 1.
	embedMicro := ai.PriceCall(ai.Usage{TokensIn: 1_000_000},
		ai.ModelRate{InputPerMTokMicroUSD: 500_000}) * 92 / 1
	// enrich prices at the real $0 local rate → adds nothing.
	wantMinor := (classifyMicro + embedMicro) / microsPerMinor

	if !got.HasCost {
		t.Fatal("HasCost = false, want true")
	}
	if got.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", got.Currency)
	}
	if got.Quality != QualityObserved {
		t.Fatalf("Quality = %s, want observed (all tasks priced from history + yields)", got.Quality)
	}
	if got.CostMinor != wantMinor {
		t.Fatalf("CostMinor = %d, want %d (classify + embed priced, enrich $0)", got.CostMinor, wantMinor)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0", got.InputTokens)
	}
}

// TestEstimatorEnrichFloorsWhenPeopleCreatedZero is TODAY's production reality:
// the backfill loop never populates capture_backfill.people_created, so a
// completed run carries people_created=0. Enrich must FLOOR (heuristic) and
// price the floor units — never quote a silent observed $0 — even though
// classify and embeddings price observed from the same completed run.
func TestEstimatorEnrichFloorsWhenPeopleCreatedZero(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 80, 0, 0) // people/orgs 0, as production leaves them

	e.insertRate(t, ws, "cloud-model", 1_000_000, 2_000_000)
	e.insertRate(t, ws, "embed-model", 500_000, 0)
	e.insertRate(t, ws, "local-model", 1_000_000, 0) // enrich floor prices at the local head

	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 2_000_000, tokensOut: 200_000})
	e.insertCall(t, ws, callRow{task: ai.TaskEnrich, tier: ai.TierLocalSmall, provider: ai.ProviderFake, model: "local-model", tokensIn: 500_000, tokensOut: 50_000})
	e.insertCall(t, ws, callRow{task: ai.TaskEmbeddings, tier: ai.TierEmbedLane, provider: ai.ProviderFake, model: "embed-model", tokensIn: 1_000_000})
	for i := 0; i < 4; i++ {
		e.insertLabeledActivity(t, ws)
	}

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (people_created=0 floors enrich)", got.Quality)
	}
	// Classify + embeddings price observed; enrich floors but is still priced at
	// its floor units — nothing is a silent $0.
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (classify + embeddings priced observed; enrich priced at floor units)")
	}
	if got.CostMinor <= 0 {
		t.Fatalf("CostMinor = %d, want > 0", got.CostMinor)
	}
}

// TestEstimatorExcludesRatelessModelAndFlagsHeuristic: two classify slices on
// two tiers — local-model priced, cloud-model rate-less. The rate-less slice is
// excluded from the cost and the estimate is heuristic, but the priced slice
// still contributes.
func TestEstimatorExcludesRatelessModelAndFlagsHeuristic(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 100, 0, 0)

	// local-model priced; cloud-model deliberately UNrated.
	e.insertRate(t, ws, "local-model", 1_000_000, 0)

	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierLocalSmall, provider: ai.ProviderFake, model: "local-model", tokensIn: 1_000_000, tokensOut: 0})
	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 4_000_000, tokensOut: 0})
	for i := 0; i < 2; i++ {
		e.insertLabeledActivity(t, ws)
	}

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (a rate-less slice is in the mix)", got.Quality)
	}
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (the local-model slice priced)")
	}
	// Cost reflects ONLY the priced local slice, spread over the FULL labeled
	// denominator — classify counts labeled MESSAGES, not calls, so a partly
	// unpriced mix is NOT call-reweighted (that would overquote): units = 100;
	// denom = labeled = 2; pricedDenom = 2; the unpriced cloud share falls to $0.
	wantMicro := ai.PriceCall(ai.Usage{TokensIn: 1_000_000},
		ai.ModelRate{InputPerMTokMicroUSD: 1_000_000}) * 100 / 2
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d (only the priced local slice)", got.CostMinor, wantMicro/microsPerMinor)
	}
}

// TestEstimatorCountsMeteringFailedRows: a call whose meter write failed
// (error_sentinel='metering_failed') was still SERVED — the model answered and
// spent provider tokens (only the metering-DB write failed, callstore.go's
// errMeteringFailed). Excluding it would understate the next preview, so the
// served-row filter must count it. Here the ONLY classify slice is a
// metering_failed row: if it were dropped, classify would fall to the cheap
// work-shape floor and the cost would be a tiny floor figure instead of the
// large observed one this asserts.
func TestEstimatorCountsMeteringFailedRows(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 100, 0, 0)

	e.insertRate(t, ws, "cloud-model", 1_000_000, 0)
	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 2_000_000, tokensOut: 0, errorSentinel: "metering_failed"})
	e.insertLabeledActivity(t, ws)

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	// The metering_failed slice is counted: units = 100, denom = labeled = 1,
	// pricedDenom = 1 (single priced classify slice). A dropped row would floor.
	wantMicro := ai.PriceCall(ai.Usage{TokensIn: 2_000_000},
		ai.ModelRate{InputPerMTokMicroUSD: 1_000_000}) * 100 / 1
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (the metering_failed slice still spent tokens)")
	}
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d — the metering_failed slice was dropped and classify fell to the floor", got.CostMinor, wantMicro/microsPerMinor)
	}
}

// TestEstimatorEnrichMeteringFailedRetryDoesNotInflateDenominator (#4): for a
// call-based denominator (enrich per person) a metering_failed retry spent
// provider tokens — carried in the token sums — but completed no fresh person.
// The SQL splits the two: Calls counts both served rows, CompletedCalls counts
// only the clean one. So an enrich person with one clean call + one
// metering_failed retry projects the FULL doubled spend over ONE completed call,
// never dividing the retry cost back out. Enrich is isolated on cheap_cloud
// (the only rated model) so classify/embeddings floor unpriced and add no cost.
func TestEstimatorEnrichMeteringFailedRetryDoesNotInflateDenominator(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 100, 50, 0) // people_created=50 → observed enrich ratio

	// ONLY cloud-model is rated: enrich (served on cheap_cloud) prices; classify's
	// and embeddings' floor heads (local-model, embed-model) stay unrated → $0.
	e.insertRate(t, ws, "cloud-model", 1_000_000, 0)

	// One clean enrich call + one metering_failed retry, same (tier, provider,
	// model) so they group into a single slice: TokensIn=1_000_000, Calls=2,
	// CompletedCalls=1.
	e.insertCall(t, ws, callRow{task: ai.TaskEnrich, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 500_000, tokensOut: 0})
	e.insertCall(t, ws, callRow{task: ai.TaskEnrich, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 500_000, tokensOut: 0, errorSentinel: "metering_failed"})

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	// units = 100×50/100 = 50; denom = CompletedCalls = 1 (NOT 2). The doubled
	// spend of both rows is priced over the single completed call. Had the
	// metering_failed row inflated the denominator to 2, this figure would halve.
	wantMicro := ai.PriceCall(ai.Usage{TokensIn: 1_000_000},
		ai.ModelRate{InputPerMTokMicroUSD: 1_000_000}) * 50 / 1
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (the enrich slice priced at cloud-model)")
	}
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d — the metering_failed retry inflated the call denominator and cancelled its spend", got.CostMinor, wantMicro/microsPerMinor)
	}
}

// TestEstimatorRepricesSinceUnboundSlice: a classify slice served on a model
// that has since left the ladder reprices at cheap_cloud's CURRENT binding
// (which is rated), never the $0 local head — the cloud cost survives a swap.
func TestEstimatorRepricesSinceUnboundSlice(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	connID := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, connID, 6, 100, 100, 0, 0)

	// The router binds cheap_cloud → cloud-model (rated) and local → local-model
	// ($0). The served slice ran on old-cloud-model, now departed.
	e.insertRate(t, ws, "cloud-model", 1_000_000, 0)
	e.insertRate(t, ws, "local-model", 0, 0)
	e.insertCall(t, ws, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "old-cloud-model", tokensIn: 2_000_000, tokensOut: 0})
	e.insertLabeledActivity(t, ws)

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	// Repriced at cloud-model: units = 100, denom = labeled = 1, pricedDenom = 1.
	wantMicro := ai.PriceCall(ai.Usage{TokensIn: 2_000_000},
		ai.ModelRate{InputPerMTokMicroUSD: 1_000_000}) * 100 / 1
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d (repriced at cheap_cloud's current binding, not the $0 head)", got.CostMinor, wantMicro/microsPerMinor)
	}
	if got.CostMinor <= 0 {
		t.Fatal("cost vanished — the departed slice was mispriced at the $0 local head")
	}
}

// TestEstimatorNoHistoryUsesFloor: a workspace with a connection but no ai_call,
// no labeled activity and no backfill falls entirely to the work-shape floor.
func TestEstimatorNoHistoryUsesFloor(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)
	e.seedConnection(t, ws, user, "gmail")
	// Rate the floor's classify head (local-model) so the floor prices honestly.
	e.insertRate(t, ws, "local-model", 1_000_000, 0)

	got, err := e.newEstimator().EstimateBackfill(wsCtx, "gmail", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (no history → floor)", got.Quality)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0 (floor tokens surfaced)", got.InputTokens)
	}
}

// TestEstimatorConnectionScopedYields: two connections, only the first has a
// completed backfill. Previewing the SECOND must use floor units — never the
// first connection's yields (no cross-connection blend).
func TestEstimatorConnectionScopedYields(t *testing.T) {
	e := setupEstimator(t)
	ws, wsCtx := e.seedWorkspace(t)
	user := e.seedUser(t, ws)

	gmailConn := e.seedConnection(t, ws, user, "gmail")
	e.seedBackfill(t, ws, gmailConn, 12, 1000, 900, 200, 50) // a rich yield on gmail
	e.seedConnection(t, ws, user, "imap")                    // imap has NO completed run

	e.insertRate(t, ws, "local-model", 1_000_000, 0)

	est := e.newEstimator()
	imap, err := est.EstimateBackfill(wsCtx, "imap", user, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill(imap): %v", err)
	}
	if imap.Quality != QualityHeuristic {
		t.Fatalf("imap Quality = %s, want heuristic (no run → floor units)", imap.Quality)
	}
	// The imap preview's classify tokens must equal the FLOOR (units = scanned =
	// 100), not gmail's captured/scanned ratio — proof the yield did not bleed
	// across connections. classify floor tokens = floor.TokensIn × 100.
	wantFloorClassifyTokens := int64(workShapeFloor(ai.TaskCaptureClassify).TokensIn) * 100
	if imap.InputTokens < wantFloorClassifyTokens {
		t.Fatalf("imap InputTokens = %d, want ≥ %d (classify floor units, no cross-connection blend)", imap.InputTokens, wantFloorClassifyTokens)
	}
}

// TestEstimatorRLSCrossWorkspaceInvisibility: workspace A's estimate never reads
// workspace B's ai_call / rates / backfill — RLS is the only gate.
func TestEstimatorRLSCrossWorkspaceInvisibility(t *testing.T) {
	e := setupEstimator(t)
	wsA, ctxA := e.seedWorkspace(t)
	userA := e.seedUser(t, wsA)
	e.seedConnection(t, wsA, userA, "gmail")

	wsB, _ := e.seedWorkspace(t)
	userB := e.seedUser(t, wsB)
	connB := e.seedConnection(t, wsB, userB, "gmail")
	e.seedBackfill(t, wsB, connB, 6, 100, 100, 10, 0)
	e.insertRate(t, wsB, "cloud-model", 9_000_000, 0)
	e.insertCall(t, wsB, callRow{task: ai.TaskCaptureClassify, tier: ai.TierCheapCloud, provider: ai.ProviderFake, model: "cloud-model", tokensIn: 5_000_000, tokensOut: 0})
	e.insertLabeledActivity(t, wsB)

	// Workspace A (its own context, its own user) sees none of B's history — it
	// falls to the floor, and its cost never reflects B's expensive slice.
	got, err := e.newEstimator().EstimateBackfill(ctxA, "gmail", userA, 100)
	if err != nil {
		t.Fatalf("EstimateBackfill(A): %v", err)
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("workspace A Quality = %s, want heuristic (A has no history of its own)", got.Quality)
	}
	if got.HasCost {
		t.Fatalf("workspace A HasCost = true (CostMinor=%d) — it must not see B's rated slice", got.CostMinor)
	}
}
