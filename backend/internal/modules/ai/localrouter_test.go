// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// memCallStore is the in-memory CallRecorder these tests inject through
// WithCallStore — off Postgres, same contract CallMeter serves in
// production.
type memCallStore struct {
	calls []Call
}

func (s *memCallStore) Record(_ context.Context, attempts []Call) error {
	s.calls = append(s.calls, attempts...)
	return nil
}

func (s *memCallStore) EnsureConfig(context.Context, ConfigSnapshot) error { return nil }

// localFakeConfig binds one tier (and the embeddings lane) to the fake
// provider so cfg.buildClients() succeeds without a real routing file —
// the shape every NewLocalRouter test starts from.
func localFakeConfig() RoutingConfig {
	return RoutingConfig{
		Profile:    ProfileEUHosted,
		Tiers:      map[Tier]ProviderConfig{TierCheapCloud: {Provider: ProviderFake}},
		Embeddings: ProviderConfig{Provider: ProviderFake},
	}
}

// TestNewLocalRouterWithoutResultCacheServesEveryScriptedAnswer proves
// WithoutResultCache actually disables the cache: with it on (the
// default, covered below) two identical requests collapse onto one
// scripted answer. With it off, both scripted answers are served in
// order — cache-off, not "cache with a shorter TTL".
func TestNewLocalRouterWithoutResultCacheServesEveryScriptedAnswer(t *testing.T) {
	fake := NewFakeClient().Script("first answer", "second answer")
	r, err := NewLocalRouter(localFakeConfig(), WithoutResultCache(), WithFakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	ctx := wsContext(t)
	req := model.Request{Messages: []model.Message{{Role: "user", Content: "same prompt"}}}

	first, info1, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	second, info2, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "first answer" || second.Text != "second answer" {
		t.Fatalf("cache-off must serve both scripted answers in order, got %q then %q", first.Text, second.Text)
	}
	if info1.Cached || info2.Cached {
		t.Fatalf("cache-off must never report a cache hit: %+v %+v", info1, info2)
	}
	if len(fake.Calls()) != 2 {
		t.Fatalf("cache-off must reach the model both times, got %d calls", len(fake.Calls()))
	}
}

// TestNewLocalRouterWithCallStoreRecordsOneCallPerCompletionAndStampsCacheOff
// covers Step 1 requirement 2: WithCallStore wires a recorder that gets
// one Call per completion, and every one of them carries CacheOff=true
// when WithoutResultCache is also set — the trace tells the truth about
// the Router's own posture, not just what happened to this one request.
func TestNewLocalRouterWithCallStoreRecordsOneCallPerCompletionAndStampsCacheOff(t *testing.T) {
	fake := NewFakeClient().Script("a", "b")
	store := &memCallStore{}
	r, err := NewLocalRouter(localFakeConfig(), WithCallStore(store), WithoutResultCache(), WithFakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	ctx := wsContext(t)
	req1 := model.Request{Messages: []model.Message{{Role: "user", Content: "one"}}}
	req2 := model.Request{Messages: []model.Message{{Role: "user", Content: "two"}}}

	if _, _, err := r.Complete(ctx, TaskSummarize, req1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Complete(ctx, TaskSummarize, req2); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 2 {
		t.Fatalf("want one recorded Call per completion, got %d", len(store.calls))
	}
	for i, c := range store.calls {
		if !c.CacheOff {
			t.Fatalf("call %d: CacheOff must be stamped true when WithoutResultCache is set, got %+v", i, c)
		}
	}
}

// TestNewLocalRouterCacheOnByDefaultCollapsesIdenticalRequests is the
// regression guard for the pre-existing NewUnmeteredRouter/sitereaddebug
// behavior: with no LocalOption at all, the result cache still runs, so
// the second of two identical requests is served from cache rather than
// reaching the model again.
func TestNewLocalRouterCacheOnByDefaultCollapsesIdenticalRequests(t *testing.T) {
	fake := NewFakeClient().Script("first answer", "second answer")
	r, err := NewLocalRouter(localFakeConfig(), WithFakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	ctx := wsContext(t)
	req := model.Request{Messages: []model.Message{{Role: "user", Content: "same prompt"}}}

	first, _, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	second, info, err := r.Complete(ctx, TaskSummarize, req)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Cached || second.Text != first.Text {
		t.Fatalf("cache-on default must collapse identical requests, got %+v %q vs %q", info, first.Text, second.Text)
	}
	if len(fake.Calls()) != 1 {
		t.Fatalf("model should only be called once under the default cache, got %d calls", len(fake.Calls()))
	}
}

// TestNewLocalRouterWithMonthlyBudgetIsLive proves the static budget
// WithMonthlyBudget installs actually gates calls, not just a wired but
// idle field: a tiny budget spent past by the first call must degrade
// (or queue) the second, per §1.3. The budget (20 tokens) is set below
// what even a short fake completion spends (~20-30 tokens on the
// fnv-hash fallback text), so the first call alone is guaranteed to push
// utilization past the 100% hard-cap band for the second — no dependency
// on the exact byte count of the marshaled wire payload.
func TestNewLocalRouterWithMonthlyBudgetIsLive(t *testing.T) {
	fake := NewFakeClient()
	r, err := NewLocalRouter(localFakeConfig(), WithMonthlyBudget(20), WithoutResultCache(), WithFakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	ctx := wsContext(t)
	req := model.Request{Messages: []model.Message{{Role: "user", Content: "burn the budget"}}}

	if _, info, err := r.Complete(ctx, TaskSummarize, req); err != nil || info.Degraded {
		t.Fatalf("first call should serve un-degraded and spend real tokens, got info=%+v err=%v", info, err)
	}

	// TaskSummarize is an interactive task (not in the non-interactive
	// set), so at ≥100% utilization it pins to TierLocalSmall degraded
	// rather than queuing — but this config binds no TierLocalSmall
	// client, so the honest degraded-state error is what proves the
	// budget bit past 100%.
	_, _, err = r.Complete(ctx, TaskSummarize, req)
	if err == nil {
		t.Fatal("second call must be gated by the now-exhausted 100-token budget")
	}
	if !errors.Is(err, ErrBudgetExhausted) && !strings.Contains(err.Error(), "no bound tier") {
		t.Fatalf("want a budget-band effect (queue or honest degrade), got %v", err)
	}
}
