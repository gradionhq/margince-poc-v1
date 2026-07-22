// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"sync"
)

// localOpts collects the LocalOption knobs NewLocalRouter assembles a
// Router from. monthlyBudget defaults to DefaultMonthlyTokens so a caller
// that names no budget still gets a static single-seat ceiling, never an
// unbounded one.
type localOpts struct {
	callStore       CallRecorder
	cacheOff        bool
	monthlyBudget   int64
	fakeClient      *FakeClient
	capturePayloads bool
}

// LocalOption configures the DB-less router NewLocalRouter builds — the
// composition seam every DB-less caller (the worker's siteread debug
// tool, the aicert lane) assembles a Router through instead of hand-rolling
// one.
type LocalOption func(*localOpts)

// WithCallStore installs a CallRecorder so a DB-less caller can still
// observe every completion terminal (an in-memory store for a cert run
// or a test), instead of the default nil recorder that traces nothing.
func WithCallStore(cs CallRecorder) LocalOption {
	return func(o *localOpts) { o.callStore = cs }
}

// WithoutResultCache disables the §6 result cache entirely. The cert lane
// and a scripted-repeat test both need every identical request to reach
// the model again — a cache hit would silently collapse two distinct
// scripted responses into one.
func WithoutResultCache() LocalOption {
	return func(o *localOpts) { o.cacheOff = true }
}

// WithMonthlyBudget overrides the static token ceiling the in-memory
// meter is judged against (default DefaultMonthlyTokens). The budget is
// static for the life of the Router — it cannot degrade mid-run when set
// generously — but a small value proves the budget-band guardrail (§1.3)
// is genuinely live over this DB-less path, not merely wired and idle.
func WithMonthlyBudget(tokens int64) LocalOption {
	return func(o *localOpts) { o.monthlyBudget = tokens }
}

// WithFakeClient replaces every fake-provider client NewLocalRouter would
// otherwise build fresh (each tier bound to ProviderFake, and the
// embedder when its lane is bound to ProviderFake) with the caller's own
// *FakeClient. Building fresh fakes from cfg.buildClients gives the
// caller no handle on the instance actually serving calls; this option is
// how a scripted test (or a cert run) gets one to script and to inspect
// afterward via Calls().
func WithFakeClient(c *FakeClient) LocalOption {
	return func(o *localOpts) { o.fakeClient = c }
}

// WithPayloadCapture turns on the Layer-3 content capture (the same
// post-SecretStripper request+response the production router writes to
// ai_call_payload) on this DB-less router, so a caller that installed a
// CallRecorder sees each terminal Call carry its Payload. Off by default:
// the debug/dev paths only pay the marshal+strip cost when a caller (the
// aicert lane's trace dump) actually wants the bodies.
func WithPayloadCapture() LocalOption {
	return func(o *localOpts) { o.capturePayloads = true }
}

// NewLocalRouter builds a Router over an in-memory meter and no
// Postgres — the DB-less path for dev tooling (the worker's siteread
// debug subcommand) and the aicert lane. Calls ride the full routing,
// budget-band, retry and secret-stripping pipeline; only the spend
// record lives in process memory instead of ai_usage, and by default no
// ai_call rows are traced (WithCallStore installs a recorder) and the
// result cache runs (WithoutResultCache turns it off).
func NewLocalRouter(cfg RoutingConfig, opts ...LocalOption) (*Router, error) {
	clients, embedder, err := cfg.buildClients()
	if err != nil {
		return nil, err
	}
	o := localOpts{monthlyBudget: int64(DefaultMonthlyTokens)}
	for _, opt := range opts {
		opt(&o)
	}
	if o.fakeClient != nil {
		// Swap in the caller's fake for every slot cfg.buildClients would
		// otherwise have filled with an untracked fresh one — matched by
		// binding, not by client identity, since buildClients never hands
		// back which Client instance it built.
		for tier, binding := range cfg.Tiers {
			if binding.Provider == ProviderFake {
				clients[tier] = o.fakeClient
			}
		}
		if cfg.Embeddings.Provider == ProviderFake {
			embedder = o.fakeClient
		}
	}
	meta := embedInclusiveMeta(cfg)
	// o.callStore: the DB-less debug path traces no ai_call rows by
	// default (the router skips tracing when calls == nil), captures no
	// payloads (WithPayloadCapture opts in), and logs through slog.Default;
	// WithCallStore opts a caller into tracing.
	router := assembleRouter(clients, embedder, cfg.Profile, &memoryMeter{}, StaticBudget(o.monthlyBudget), o.callStore, meta, o.capturePayloads, nil)
	router.cacheOff = o.cacheOff
	router.installConfigSnapshot(cfg.sourceHash, cfg.Embeddings.Dimensions)
	return router, nil
}

// memoryMeter accumulates spend for the life of one process: enough for
// the budget bands to behave honestly during a debug run, gone at exit.
type memoryMeter struct {
	mu     sync.Mutex
	tokens int64
}

func (m *memoryMeter) Record(_ context.Context, u Usage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens += int64(u.TokensIn + u.TokensOut)
	return nil
}

func (m *memoryMeter) MonthTokens(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens, nil
}
