// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type fakeCallStore struct{ recorded []Call }

func (f *fakeCallStore) Record(_ context.Context, c Call) error {
	f.recorded = append(f.recorded, c)
	return nil
}

// stubClient returns a fixed response or error.
type stubClient struct {
	resp model.Response
	err  error
}

func (s stubClient) Complete(context.Context, model.Request) (model.Response, error) {
	return s.resp, s.err
}

func (s stubClient) Stream(context.Context, model.Request) (model.TokenStream, error) {
	return nil, errors.New("unused")
}

func (s stubClient) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, errors.New("unused")
}
func (s stubClient) Caps() model.Capabilities { return model.Capabilities{} }

func wsCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}

type stubMeter struct{}

func (stubMeter) Record(context.Context, Usage) error        { return nil }
func (stubMeter) MonthTokens(context.Context) (int64, error) { return 0, nil }

type unlimitedBudget struct{}

func (unlimitedBudget) MonthlyTokenBudget(context.Context, ids.WorkspaceID) (int64, error) {
	return 1_000_000_000, nil
}

func newTracingRouter(t *testing.T, client model.Client, fcs *fakeCallStore) *Router {
	t.Helper()
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: client},
		client, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	return r
}

func TestCompleteRecordsServedCall(t *testing.T) {
	fcs := &fakeCallStore{}
	r := newTracingRouter(t, stubClient{resp: model.Response{Text: "hi", InputTokens: 10, OutputTokens: 5}}, fcs)
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, model.Request{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(fcs.recorded) != 1 {
		t.Fatalf("recorded %d calls; want 1", len(fcs.recorded))
	}
	got := fcs.recorded[0]
	if got.Provider != "openai" || got.ModelID != "gpt-x" || got.TokensIn != 10 || got.TokensOut != 5 || got.ErrorSentinel != "" || got.CacheHit {
		t.Fatalf("served call recorded wrong: %+v", got)
	}
}

func TestCompleteRecordsFailure(t *testing.T) {
	fcs := &fakeCallStore{}
	r := newTracingRouter(t, stubClient{err: errors.New("provider down")}, fcs)
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, model.Request{}); err == nil {
		t.Fatal("expected error when the only tier fails")
	}
	if len(fcs.recorded) != 1 || fcs.recorded[0].ErrorSentinel != "provider_error" {
		t.Fatalf("failure not traced with sentinel: %+v", fcs.recorded)
	}
	// The trace names the rung where the walk died, not an empty tier.
	if fcs.recorded[0].Tier != TierCheapCloud {
		t.Fatalf("all-rungs-failed trace lost the attempted tier: %+v", fcs.recorded[0])
	}
}

// TestCompleteEmitsSlog verifies that the router's observeCall emits an
// "ai.call" slog line with the expected attributes (task, tier, provider,
// tokens_in, tokens_out, latency_ms, cache_hit, degraded, error).
func TestCompleteEmitsSlog(t *testing.T) {
	// Capture slog output to a buffer
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	// Set up a router with a fake client
	meter := &memMeter{}
	cheap := NewFakeClient().Script("test response")
	meta := map[Tier]routeMeta{
		TierCheapCloud: {provider: "fake-provider", model: "fake-model"},
	}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: cheap},
		NewFakeClient(),
		ProfileEUHosted,
		meter,
		DefaultMonthlyTokens,
		nil, // callStore
		meta,
		false, // capturePayloads
		logger,
	)

	// Execute a completion
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	_, _, err := r.Complete(ctx, TaskSummarize, model.Request{
		Messages: []model.Message{{Role: "user", Content: "summarize this"}},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	// Parse the slog output
	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		t.Fatal("no slog output captured")
	}

	// Find the "ai.call" message
	var logEntry map[string]interface{}
	for _, line := range lines {
		if err := json.Unmarshal([]byte(line), &logEntry); err != nil {
			continue
		}
		msg, ok := logEntry["msg"].(string)
		if !ok || msg != "ai.call" {
			continue
		}
		// Verify the expected attributes are present
		if task, ok := logEntry["task"].(string); !ok || task != string(TaskSummarize) {
			t.Errorf("expected task=%s, got %v", TaskSummarize, task)
		}
		if tier, ok := logEntry["tier"].(string); !ok || tier != string(TierCheapCloud) {
			t.Errorf("expected tier=%s, got %v", TierCheapCloud, tier)
		}
		if provider, ok := logEntry["provider"].(string); !ok || provider != "fake-provider" {
			t.Errorf("expected provider=fake-provider, got %v", provider)
		}
		if tokensIn, ok := logEntry["tokens_in"].(float64); !ok || tokensIn == 0 {
			t.Errorf("expected tokens_in > 0, got %v", tokensIn)
		}
		if latencyMS, ok := logEntry["latency_ms"].(float64); !ok || latencyMS < 0 {
			t.Errorf("expected latency_ms >= 0, got %v", latencyMS)
		}
		cacheHit, _ := logEntry["cache_hit"].(bool)
		if cacheHit {
			t.Errorf("expected cache_hit=false, got true")
		}
		degraded, _ := logEntry["degraded"].(bool)
		if degraded {
			t.Errorf("expected degraded=false, got true")
		}
		return
	}
	t.Fatalf("ai.call slog message not found in output: %s", output)
}

func TestBuildPayloadStripsSecrets(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "answer", OutputTokens: 2}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		true, nil, // capturePayloads = true
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	req := model.Request{System: "sys", Messages: []model.Message{{Role: "user", Content: "my key sk-ABCDEF0123456789 leaks"}}}
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(fcs.recorded) != 1 || fcs.recorded[0].Payload == nil {
		t.Fatalf("expected a captured payload; got %+v", fcs.recorded)
	}
	if strings.Contains(string(fcs.recorded[0].Payload.Request), "sk-ABCDEF0123456789") {
		t.Fatal("captured request payload still contains the secret")
	}
}

// TestBuildPayloadStripsSecretsFromResponse: a model that echoes a
// credential from its context must not land it verbatim in
// ai_call_payload — the response rides the same stripper as the request.
func TestBuildPayloadStripsSecretsFromResponse(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "your key is sk-ABCDEF0123456789", OutputTokens: 2}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		true, nil, // capturePayloads = true
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	req := model.Request{System: "sys", Messages: []model.Message{{Role: "user", Content: "what key did I paste?"}}}
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(fcs.recorded) != 1 || fcs.recorded[0].Payload == nil {
		t.Fatalf("expected a captured payload; got %+v", fcs.recorded)
	}
	if strings.Contains(string(fcs.recorded[0].Payload.Response), "sk-ABCDEF0123456789") {
		t.Fatal("captured response payload still contains the secret")
	}
}

// TestBuildPayloadBoundsWholeRequest: a long agent-loop message list whose
// every message is individually under the field cap must still land under
// the request-side aggregate budget — per-field caps alone leave the row
// unbounded in the message count.
func TestBuildPayloadBoundsWholeRequest(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "ok", OutputTokens: 1}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		true, nil, // capturePayloads = true
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	msgs := make([]model.Message, 12)
	for i := range msgs {
		msgs[i] = model.Message{Role: "user", Content: strings.Repeat("m", 6_000)} // each under the 16k field cap
	}
	req := model.Request{System: "sys", Messages: msgs} // 72k content runes offered, 48k budgeted
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(fcs.recorded) != 1 || fcs.recorded[0].Payload == nil {
		t.Fatalf("expected a captured payload; got %+v", fcs.recorded)
	}
	p := fcs.recorded[0].Payload
	if !json.Valid(p.Request) {
		t.Fatalf("captured request is not valid JSON: %s", p.Request)
	}
	kept := strings.Count(string(p.Request), "m")
	if kept > maxCapturedRequestRunes {
		t.Fatalf("request-side capture kept %d content runes, over the %d aggregate budget", kept, maxCapturedRequestRunes)
	}
}

// cancelingClient cancels the request context and fails, standing in for
// a provider call that died by timeout/cancellation.
type cancelingClient struct {
	stubClient
	cancel context.CancelFunc
}

func (c cancelingClient) Complete(context.Context, model.Request) (model.Response, error) {
	c.cancel()
	return model.Response{}, context.Canceled
}

// ctxCheckingCallStore records whether the trace write arrived on an
// already-dead context.
type ctxCheckingCallStore struct {
	recorded []Call
	ctxErr   error
}

func (f *ctxCheckingCallStore) Record(ctx context.Context, c Call) error {
	f.ctxErr = ctx.Err()
	f.recorded = append(f.recorded, c)
	return nil
}

// TestTraceSurvivesRequestCancellation: a canceled call is exactly the
// terminal worth recording, so the deferred trace write must arrive on a
// live context even though the request's context is already dead — a
// recorder handed the canceled context could never open its workspace
// transaction, silently dropping every timeout trace.
func TestTraceSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(wsCtx())
	defer cancel()
	fcs := &ctxCheckingCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: cancelingClient{cancel: cancel}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	if _, _, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierCheapCloud}, model.Request{}); err == nil {
		t.Fatal("expected the canceled call to fail")
	}
	if len(fcs.recorded) != 1 {
		t.Fatalf("canceled call not traced: recorded %d rows", len(fcs.recorded))
	}
	if fcs.ctxErr != nil {
		t.Fatalf("trace write arrived on a dead context: %v", fcs.ctxErr)
	}
}

// TestCacheHitRespectsAdjustedLadder: an entry cached from a tier the
// current (budget/profile-adjusted) ladder no longer offers must be a
// miss — a premium answer cached before the band tightened must not
// smuggle premium output into an economy route.
func TestCacheHitRespectsAdjustedLadder(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{
			TierPremium:    stubClient{resp: model.Response{Text: "premium answer", OutputTokens: 3}},
			TierCheapCloud: stubClient{resp: model.Response{Text: "cheap answer", OutputTokens: 2}},
		},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{
			TierPremium:    {provider: "anthropic", model: "claude-x"},
			TierCheapCloud: {provider: "openai", model: "gpt-x"},
		},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	ctx := wsCtx()
	req := model.Request{System: "same request"}
	if _, info, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierPremium}, req); err != nil || info.Tier != TierPremium {
		t.Fatalf("premium serve: %v %+v", err, info)
	}
	_, info, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierCheapCloud}, req)
	if err != nil {
		t.Fatalf("cheap serve: %v", err)
	}
	if info.Cached || info.Tier != TierCheapCloud {
		t.Fatalf("cached premium entry served onto a ladder without premium: %+v", info)
	}
}

func TestBuildPayloadTruncatesOverlongContent(t *testing.T) {
	fcs := &fakeCallStore{}
	long := strings.Repeat("a", maxCapturedPayloadRunes+5000)
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: strings.Repeat("b", maxCapturedPayloadRunes+5000), OutputTokens: 2}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		true, nil, // capturePayloads = true
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	req := model.Request{System: long, Messages: []model.Message{{Role: "user", Content: long}}}
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(fcs.recorded) != 1 || fcs.recorded[0].Payload == nil {
		t.Fatalf("expected a captured payload; got %+v", fcs.recorded)
	}
	p := fcs.recorded[0].Payload
	// The stored jsonb must stay valid — truncation happens on the content
	// before marshaling, never on the marshaled bytes.
	if !json.Valid(p.Request) || !json.Valid(p.Response) {
		t.Fatalf("captured payload is not valid JSON: req=%s resp=%s", p.Request, p.Response)
	}
	// One marker per truncated field: system + the one message in the request,
	// and the response text.
	if got := strings.Count(string(p.Request), "…[truncated]"); got != 2 {
		t.Fatalf("expected 2 truncation markers in request (system + message); got %d", got)
	}
	if got := strings.Count(string(p.Response), "…[truncated]"); got != 1 {
		t.Fatalf("expected 1 truncation marker in response; got %d", got)
	}
	// Each field is bounded: no single content run exceeds the cap.
	if got := longestRun(string(p.Request), 'a'); got > maxCapturedPayloadRunes {
		t.Fatalf("a request content field exceeded the cap: %d runes", got)
	}
	if got := longestRun(string(p.Response), 'b'); got > maxCapturedPayloadRunes {
		t.Fatalf("the response content field exceeded the cap: %d runes", got)
	}
}

// longestRun reports the length of the longest consecutive run of c in s.
func longestRun(s string, c rune) int {
	best, cur := 0, 0
	for _, r := range s {
		if r == c {
			cur++
			if cur > best {
				best = cur
			}
			continue
		}
		cur = 0
	}
	return best
}

// failingMeter fails every metering write, producing the
// served-but-metering-failed terminal.
type failingMeter struct{}

func (failingMeter) Record(context.Context, Usage) error        { return errors.New("meter db down") }
func (failingMeter) MonthTokens(context.Context) (int64, error) { return 0, nil }

func TestServedButMeteringFailedTracesTier(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "hi", OutputTokens: 1}}},
		nil, ProfileCloudFrontier, failingMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-x"}},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, model.Request{}); err == nil {
		t.Fatal("expected the metering failure to surface (fail loud)")
	}
	if len(fcs.recorded) != 1 {
		t.Fatalf("recorded %d calls; want 1", len(fcs.recorded))
	}
	got := fcs.recorded[0]
	if got.Tier != TierCheapCloud || got.Provider != "openai" || got.ModelID != "gpt-x" {
		t.Fatalf("served tier/route not traced on metering failure: %+v", got)
	}
	if got.ErrorSentinel != "metering_failed" {
		t.Fatalf("expected metering_failed sentinel, got %q", got.ErrorSentinel)
	}
	// Provider tokens were spent even though metering failed — the trace
	// must bill them, not zero them out with the discarded response.
	if got.TokensOut != 1 {
		t.Fatalf("served call's token spend lost on metering failure: %+v", got)
	}
}

func TestCaptureOffRecordsNoPayload(t *testing.T) {
	fcs := &fakeCallStore{}
	r := newTracingRouter(t, stubClient{resp: model.Response{Text: "x", OutputTokens: 1}}, fcs) // capture=false
	if _, _, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierCheapCloud}, model.Request{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if fcs.recorded[0].Payload != nil {
		t.Fatal("capture off must record no payload")
	}
}
