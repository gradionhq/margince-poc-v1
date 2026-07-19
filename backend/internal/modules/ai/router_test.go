// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

func TestRouterHardCapQueuesNonInteractive(t *testing.T) {
	meter := &memMeter{spent: int64(DefaultMonthlyTokens) + 1}
	r := testRouter(map[Tier]model.Client{TierLocalSmall: NewFakeClient()}, meter, DefaultMonthlyTokens, ProfileEUHosted)
	_, _, err := r.Complete(wsContext(t), TaskCaptureClassify, model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("non-interactive task at hard cap must queue, got %v", err)
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
