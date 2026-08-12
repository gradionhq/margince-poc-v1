// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// memMeter is the in-memory usageStore for router unit tests.
type memMeter struct {
	spent   int64
	records []Usage
}

func (m *memMeter) Record(_ context.Context, u Usage) error {
	m.records = append(m.records, u)
	return nil
}
func (m *memMeter) MonthTokens(context.Context) (int64, error) { return m.spent, nil }

// failingClient errors every call — the fallback trigger.
type failingClient struct{ model.Client }

func (failingClient) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("provider down")
}

// fixedResponseClient returns a canned completion — used to prove the router
// forwards a provider's itemized usage counters verbatim into the meter.
type fixedResponseClient struct {
	model.Client
	resp model.Response
}

func (c fixedResponseClient) Complete(context.Context, model.Request) (model.Response, error) {
	return c.resp, nil
}
func (fixedResponseClient) Caps() model.Capabilities { return model.Capabilities{} }

func wsContext(t *testing.T) context.Context {
	t.Helper()
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}

func testRouter(clients map[Tier]model.Client, meter usageStore, spentBudget BudgetPolicy, profile Profile) *Router {
	return assembleRouter(clients, NewFakeClient(), profile, meter, spentBudget, nil, nil, false, nil)
}

func TestRouterRoutesTaskToPrimaryTierAndMeters(t *testing.T) {
	meter := &memMeter{}
	cheap := NewFakeClient().Script("summary text")
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, meter, DefaultMonthlyTokens, ProfileEUHosted)

	resp, info, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "sum it"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "summary text" || info.Tier != TierCheapCloud || info.Degraded || info.Cached {
		t.Fatalf("unexpected route: %+v %+v", resp, info)
	}
	if len(meter.records) != 1 || meter.records[0].Task != TaskSummarize || meter.records[0].Tier != TierCheapCloud {
		t.Fatalf("metering wrong: %+v", meter.records)
	}
	if meter.records[0].TokensIn == 0 {
		t.Fatal("token usage not metered")
	}
}

func TestRouterForwardsCachedAndReasoningTokensToMeter(t *testing.T) {
	meter := &memMeter{}
	client := fixedResponseClient{resp: model.Response{InputTokens: 10, OutputTokens: 5, CachedTokens: 3, ReasoningTokens: 7}}
	r := testRouter(map[Tier]model.Client{TierCheapCloud: client}, meter, DefaultMonthlyTokens, ProfileEUHosted)
	if _, _, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if len(meter.records) != 1 {
		t.Fatalf("want one metered record, got %+v", meter.records)
	}
	if meter.records[0].CachedTokens != 3 || meter.records[0].ReasoningTokens != 7 {
		t.Fatalf("meter did not receive itemized tokens: %+v", meter.records[0])
	}
}

func TestRouterFallsBackOnProviderError(t *testing.T) {
	meter := &memMeter{}
	premium := NewFakeClient().Script("premium answer")
	r := testRouter(map[Tier]model.Client{
		TierCheapCloud: failingClient{},
		TierPremium:    premium,
	}, meter, DefaultMonthlyTokens, ProfileEUHosted)

	_, info, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Tier != TierPremium {
		t.Fatalf("expected premium fallback, got %s", info.Tier)
	}
}

func TestRouterEveryTierFailingSurfacesLastError(t *testing.T) {
	r := testRouter(map[Tier]model.Client{
		TierCheapCloud: failingClient{},
		TierPremium:    failingClient{},
	}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)
	_, _, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("want provider error surfaced, got %v", err)
	}
}

func TestRouterSoftDegradeAtEightyPercent(t *testing.T) {
	meter := &memMeter{spent: int64(float64(DefaultMonthlyTokens) * 0.85)}
	local := NewFakeClient().Script("economy answer")
	r := testRouter(map[Tier]model.Client{
		TierLocalSmall: local,
		TierCheapCloud: NewFakeClient().Script("full-price answer"),
	}, meter, DefaultMonthlyTokens, ProfileEUHosted)

	resp, info, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	// summarize C-C→L-S under economy mode: one tier down its ladder.
	if resp.Text != "economy answer" || info.Tier != TierLocalSmall || !info.Degraded {
		t.Fatalf("economy mode not applied: %+v %+v", resp, info)
	}
}

func TestRouterHardCapDefersBackgroundWithoutAttemptOrTrace(t *testing.T) {
	meter := &memMeter{spent: int64(DefaultMonthlyTokens) + 1}
	client := NewFakeClient()
	calls := &fakeCallStore{}
	r := testRouter(map[Tier]model.Client{TierLocalSmall: client}, meter, DefaultMonthlyTokens, ProfileEUHosted)
	r.calls = calls
	r.now = func() time.Time { return time.Date(2026, time.July, 19, 9, 30, 0, 0, time.FixedZone("ICT", 7*60*60)) }
	_, _, err := r.Complete(wsContext(t), TaskCaptureClassify, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if !errors.Is(err, ErrBudgetDeferred) {
		t.Fatalf("background task at hard cap must defer, got %v", err)
	}
	var deferral *BudgetDeferralError
	if !errors.As(err, &deferral) {
		t.Fatalf("budget error does not carry its retry boundary: %T", err)
	}
	wantNext := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	if deferral.Task != TaskCaptureClassify || !deferral.NextAttemptAt.Equal(wantNext) {
		t.Fatalf("deferral = %+v, want task %s at %s", deferral, TaskCaptureClassify, wantNext)
	}
	if len(client.Calls()) != 0 || len(calls.recorded) != 0 {
		t.Fatalf("deferral made model/trace attempts: model=%d trace=%d", len(client.Calls()), len(calls.recorded))
	}
}

func TestRouterHardCapPinsInteractiveToLocalSmall(t *testing.T) {
	meter := &memMeter{spent: int64(DefaultMonthlyTokens) + 1}
	local := NewFakeClient().Script("reduced quality")
	r := testRouter(map[Tier]model.Client{
		TierLocalSmall: local,
		TierCheapCloud: NewFakeClient().Script("should not run"),
	}, meter, DefaultMonthlyTokens, ProfileEUHosted)
	resp, info, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "reduced quality" || info.Tier != TierLocalSmall || !info.Degraded {
		t.Fatalf("interactive task at hard cap must run local-small degraded: %+v %+v", resp, info)
	}
}

func TestRouterZeroBudgetFailsClosed(t *testing.T) {
	r := testRouter(map[Tier]model.Client{TierCheapCloud: NewFakeClient()}, &memMeter{}, StaticBudget(0), ProfileEUHosted)
	_, _, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "non-positive token budget") {
		t.Fatalf("zero budget must fail closed, got %v", err)
	}
}

// The §1.4 sovereign guarantee: a cloud-defaulted task routes to a
// local tier or degrades honestly — no rung can egress.
func TestRouterSovereignRemapsCloudTiersToLocal(t *testing.T) {
	large := NewFakeClient().Script("sovereign answer")
	r := testRouter(map[Tier]model.Client{
		TierLocalSmall: NewFakeClient(),
		TierLocalLarge: large,
	}, &memMeter{}, DefaultMonthlyTokens, ProfileSovereign)
	resp, info, err := r.Complete(wsContext(t), TaskBriefRanking, model.Request{Messages: []model.Message{{Role: "user", Content: "rank"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "sovereign answer" || info.Tier != TierLocalLarge {
		t.Fatalf("P-F task must land on local-large under sovereign: %+v", info)
	}
}

// The gate P7 actually rests on: under sovereign, NO tier may be bound to a
// cloud provider, so no cloud client is ever constructed. Asserted per tier
// rather than for one sample, because a tier the validator skipped would be a
// hole nothing downstream could close.
func TestSovereignRefusesACloudBindingOnEveryTier(t *testing.T) {
	for tier := range knownTiers {
		cfg := fmt.Sprintf("profile: sovereign\ntiers:\n  %s: {provider: gemini}\nembeddings: {provider: fake}\n", tier)
		if _, err := ParseRouting([]byte(cfg)); err == nil {
			t.Errorf("tier %q accepts a cloud provider under sovereign — that binding would egress", tier)
		}
	}
}

// localTiers is the second line, and it is written as an allowlist of what is
// SAFE so that an unclassified tier is remapped rather than let through. This
// keeps it agreeing with the naming convention: a rung named local_* that
// applyProfile would needlessly remap is a bug in the other direction, and one
// NOT named local_* that it would pass through is the dangerous one.
func TestEveryLocallyNamedTierIsClassifiedLocal(t *testing.T) {
	for tier := range knownTiers {
		named := strings.HasPrefix(string(tier), "local_")
		if named != localTiers[tier] {
			t.Errorf("tier %q: named local=%v but classified local=%v — applyProfile would %s under sovereign",
				tier, named, localTiers[tier],
				map[bool]string{true: "remap a local rung needlessly", false: "leave a cloud-named rung on the ladder"}[named])
		}
	}
}

// applyProfile is exercised directly here because no task ladder names frontier
// yet: the sovereign guarantee has to hold for the rung BEFORE something routes
// to it, not after.
func TestApplyProfileRemapsFrontierUnderSovereign(t *testing.T) {
	r := testRouter(map[Tier]model.Client{
		TierLocalSmall: NewFakeClient(),
		TierLocalLarge: NewFakeClient(),
	}, &memMeter{}, DefaultMonthlyTokens, ProfileSovereign)
	got := r.applyProfile([]Tier{TierFrontier, TierPremium})
	if len(got) != 1 || got[0] != TierLocalLarge {
		t.Fatalf("a frontier-led ladder must collapse to the local rung under sovereign, got %v", got)
	}
}

// Economy mode steps one rung down, and frontier's step is premium — not
// straight to a cheap tier, which would drop two capability classes at the
// first sign of budget pressure.
func TestFrontierDegradesToPremium(t *testing.T) {
	if got := degradeTo[TierFrontier]; got != TierPremium {
		t.Fatalf("frontier must degrade to premium, got %q", got)
	}
}

// A tier with no degrade step would sit at full cost through the whole 80–100%
// band, which is the one band economy mode exists to handle.
func TestEveryTierHasADegradeStep(t *testing.T) {
	for tier := range knownTiers {
		if _, ok := degradeTo[tier]; !ok {
			t.Errorf("tier %q has no degrade_to entry — economy mode would leave it at full cost", tier)
		}
	}
}

// The alarm's numerator must cover every rung billed above the cheap cloud
// rate. A costlier rung left out counts toward the denominator only, so heavier
// spend on it would report a LOWER share.
func TestCostlyCloudTiersCoversEveryRungAbovePremium(t *testing.T) {
	covered := map[Tier]bool{}
	for _, tier := range costlyCloudTiers {
		covered[tier] = true
	}
	for _, tier := range []Tier{TierPremium, TierFrontier} {
		if !covered[tier] {
			t.Errorf("tier %q is billed above the cheap cloud rate but is not in costlyCloudTiers — PremiumShare would under-report it", tier)
		}
	}
	for _, tier := range costlyCloudTiers {
		if localTiers[tier] {
			t.Errorf("tier %q is local, so it has no cloud bill to alarm on", tier)
		}
	}
}

func TestRouterSovereignWithoutLocalDegradesHonestly(t *testing.T) {
	r := testRouter(map[Tier]model.Client{}, &memMeter{}, DefaultMonthlyTokens, ProfileSovereign)
	_, _, err := r.Complete(wsContext(t), TaskSummarize, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "no bound tier") {
		t.Fatalf("want the honest degraded state, got %v", err)
	}
}

func TestRouterResultCacheHitSkipsModelCall(t *testing.T) {
	meter := &memMeter{}
	cheap := NewFakeClient().Script("first answer", "second answer")
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, meter, DefaultMonthlyTokens, ProfileEUHosted)
	ctx := wsContext(t)
	req := model.Request{Messages: []model.Message{{Role: "user", Content: "same thread"}}}

	first, _, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	second, info, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Cached || second.Text != first.Text {
		t.Fatalf("expected cache hit with identical text: %+v %q vs %q", info, first.Text, second.Text)
	}
	if len(cheap.Calls()) != 1 {
		t.Fatalf("model called %d times; cache should have served the second", len(cheap.Calls()))
	}
	if !meter.records[1].Cached {
		t.Fatalf("cache hit not metered as cached: %+v", meter.records[1])
	}
}

// Two calls with identical prompt text but a different attached document must
// not share a cache entry — otherwise the second is served the first's answer,
// derived from a document the caller never attached (same workspace).
func TestRouterCacheKeyDistinguishesAttachments(t *testing.T) {
	cheap := NewFakeClient().Script("summary of A", "summary of B")
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)
	ctx := wsContext(t)
	reqA := model.Request{
		Messages:    []model.Message{{Role: "user", Content: "summarize the attached"}},
		Attachments: []model.Attachment{{MIME: "application/pdf", Bytes: []byte("PDF-A")}},
	}
	reqB := model.Request{
		Messages:    []model.Message{{Role: "user", Content: "summarize the attached"}},
		Attachments: []model.Attachment{{MIME: "application/pdf", Bytes: []byte("PDF-B")}},
	}
	if _, _, err := r.Complete(ctx, TaskSummarize, reqA); err != nil {
		t.Fatal(err)
	}
	_, info, err := r.Complete(ctx, TaskSummarize, reqB)
	if err != nil {
		t.Fatal(err)
	}
	if info.Cached {
		t.Fatal("different attachments must not share a cache entry")
	}
	if len(cheap.Calls()) != 2 {
		t.Fatalf("expected two real calls for two distinct attachments, got %d", len(cheap.Calls()))
	}
}

// RT-AI-M7: identical inputs in two workspaces never share a cache row.
func TestRouterCacheIsWorkspaceScoped(t *testing.T) {
	cheap := NewFakeClient()
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)
	req := model.Request{Messages: []model.Message{{Role: "user", Content: "identical"}}}

	if _, _, err := r.Complete(wsContext(t), TaskSummarize, req); err != nil {
		t.Fatal(err)
	}
	_, info, err := r.Complete(wsContext(t), TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	if info.Cached {
		t.Fatal("cache leaked across workspaces")
	}
	if len(cheap.Calls()) != 2 {
		t.Fatalf("expected two real calls, got %d", len(cheap.Calls()))
	}
}

func TestRouterEmbedStripsSecretsAndMeters(t *testing.T) {
	meter := &memMeter{}
	embedder := NewFakeClient()
	r := assembleRouter(map[Tier]model.Client{}, embedder, ProfileEUHosted, meter, DefaultMonthlyTokens, nil, nil, false, nil)
	_, err := r.Embed(wsContext(t), model.EmbedRequest{Inputs: []string{"note with password=topsecretvalue in it"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := embedder.Calls()
	if len(calls) != 1 || strings.Contains(string(calls[0].Payload), "topsecretvalue") {
		t.Fatalf("embed input not stripped: %+v", calls)
	}
	if len(meter.records) != 1 || meter.records[0].Task != TaskEmbeddings {
		t.Fatalf("embed lane not metered: %+v", meter.records)
	}
}

func TestRouterRequiresWorkspaceContext(t *testing.T) {
	r := testRouter(map[Tier]model.Client{TierCheapCloud: NewFakeClient()}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)
	_, _, err := r.Complete(context.Background(), TaskSummarize, model.Request{})
	if err == nil || !strings.Contains(err.Error(), "workspace context") {
		t.Fatalf("workspace-less call must fail, got %v", err)
	}
}

// Two calls differing only in one completion-shaping binding must never share
// a cache entry — especially a company-context edit whose prompt happened to
// remain byte-identical after bounded rendering.
func TestRouterCacheKeyDistinguishesEveryExternalBinding(t *testing.T) {
	base := model.Request{Messages: []model.Message{{Role: "user", Content: "same prompt"}}}
	withModel := base
	withModel.Model = "other-model"
	withSchema := base
	withSchema.ResponseSchema = []byte(`{"type":"object"}`)
	withContext := base
	withContext.ContextScopes = []string{"identity"}
	withContext.ContextFingerprint = strings.Repeat("a", 64)

	wsID := ids.New[ids.WorkspaceKind]()
	baseKey, err := cacheKey(wsID, TaskSummarize, base)
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]model.Request{
		"model override": withModel, "response schema": withSchema, "company context": withContext,
	} {
		key, err := cacheKey(wsID, TaskSummarize, req)
		if err != nil {
			t.Fatal(err)
		}
		if key == baseKey {
			t.Fatalf("%s must change the cache key", name)
		}
	}
}

// The data boundary is minted per call, so two identical prompts never share a
// byte. If it reached the cache key, nothing would ever hit again — and capture
// auto-enrich would pay a fresh extraction for every mail from the same sender.
func TestRouterCacheKeyIgnoresThePerCallDataBoundary(t *testing.T) {
	wsID := ids.New[ids.WorkspaceKind]()
	keyFor := func(t *testing.T, fence promptfence.Fence) string {
		t.Helper()
		key, err := cacheKey(wsID, TaskSummarize, model.Request{
			System:   "Summarize the page.\n" + fence.Rule("page"),
			Messages: []model.Message{{Role: "user", Content: fence.Wrap("the same page text")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	if first, second := keyFor(t, promptfence.New()), keyFor(t, promptfence.New()); first != second {
		t.Fatalf("the same prompt under two boundaries got two cache keys:\n%s\n%s", first, second)
	}
}

// Canonicalising the boundary must not blur the data: the placeholder replaces
// only the marker the system prompt declares, so different page text still keys
// differently.
func TestRouterCacheKeyStillSeparatesDifferentFencedData(t *testing.T) {
	wsID := ids.New[ids.WorkspaceKind]()
	keyFor := func(t *testing.T, page string) string {
		t.Helper()
		fence := promptfence.New()
		key, err := cacheKey(wsID, TaskSummarize, model.Request{
			System:   "Summarize the page.\n" + fence.Rule("page"),
			Messages: []model.Message{{Role: "user", Content: fence.Wrap(page)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	if keyFor(t, "page one") == keyFor(t, "page two") {
		t.Fatal("two different pages collapsed onto one cache key")
	}
}
