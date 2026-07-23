// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The backfill wire (CAP-WIRE-4) over real migrated Postgres: preview →
// explicit start (which enqueues the pager job) → single-row status →
// cancel, plus every refusal the transport promises — 501 unwired, 401
// non-human, 422 malformed/out-of-set windows, 404 missing connection,
// 422 non-Backfiller provider, 502 provider outage, and the 409 pair
// (already running / nothing to cancel). The River pager worker is driven
// directly so the run's queued → running → done|error row transitions are
// asserted end to end.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/compose/costestimate"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// backfillFakeConnector is a paged Backfiller with injectable provider
// faults, so the transport's 502 branch and the engine's error-class
// recording are drivable from a test.
type backfillFakeConnector struct {
	name        string
	messages    int
	pageSize    int
	estimateErr error
	pageErr     error
}

func (f *backfillFakeConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: f.name, Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (f *backfillFakeConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (f *backfillFakeConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (f *backfillFakeConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (f *backfillFakeConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

func (f *backfillFakeConnector) EstimateBackfill(context.Context, connector.Auth, time.Time) (int, error) {
	if f.estimateErr != nil {
		return 0, f.estimateErr
	}
	return f.messages, nil
}

func (f *backfillFakeConnector) BackfillPage(_ context.Context, _ connector.Auth, _ time.Time, pageToken string, _ connector.Sink) (connector.BackfillPageResult, error) {
	if f.pageErr != nil {
		return connector.BackfillPageResult{}, f.pageErr
	}
	offset := 0
	if pageToken != "" {
		if _, err := fmt.Sscanf(pageToken, "off:%d", &offset); err != nil {
			return connector.BackfillPageResult{}, fmt.Errorf("bad token %q: %w", pageToken, err)
		}
	}
	n := f.pageSize
	if offset+n > f.messages {
		n = f.messages - offset
	}
	res := connector.BackfillPageResult{Scanned: n, Captured: n, Skipped: 0}
	if offset+n < f.messages {
		res.NextToken = fmt.Sprintf("off:%d", offset+n)
	}
	return res, nil
}

// plainSyncConnector deliberately implements only the base Connector — the
// non-Backfiller shape the 422 connector_unsupported branch guards.
type plainSyncConnector struct{}

func (plainSyncConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "graph", Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (plainSyncConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (plainSyncConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (plainSyncConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (plainSyncConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

// backfillAuthority stands in for identity's live resolver with rep-grade
// authority — the resolver-integration line itself is not under test here.
type backfillAuthority struct{}

func (backfillAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"activity": {Create: true, Read: true},
			"person":   {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}}, nil
}

func (backfillAuthority) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

type backfillWireEnv struct {
	env      *integration.Env
	registry *capture.Registry
	handlers backfillHandlers
	gmail    *backfillFakeConnector
	human    context.Context
}

func setupBackfillWire(t *testing.T) *backfillWireEnv {
	t.Helper()
	e := integration.Setup(t)
	applyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := capture.NewRegistry(e.Pool, capture.NewSink(e.Pool), backfillAuthority{}, keyvault.NewMemory())
	gm := &backfillFakeConnector{name: "gmail", messages: 25, pageSize: 10}
	registry.Register(gm)
	registry.Register(plainSyncConnector{})

	human := principal.WithWorkspaceID(context.Background(), e.WS)
	human = principal.WithCorrelationID(human, ids.NewV7())
	human = principal.WithActor(human, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		TeamIDs: []ids.UUID{e.Team1}, SeatType: principal.SeatFull,
		Scopes: principal.NewScopeSet(principal.ScopeRead),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true, Read: true}},
			RowScope: principal.RowScopeTeam,
		},
	})
	if _, err := registry.Connect(human, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect gmail: %v", err)
	}
	if _, err := registry.Connect(human, "graph", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect graph: %v", err)
	}
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	// The ADR-0068 cost pre-flight over the same registry (its yields) and a
	// DB-less local router whose tiers bind to distinct fake-provider models —
	// the resolvers BoundLadder / CurrentModelForTier need real (provider, model)
	// identities, no network. A fixed clock keeps the 7-day window deterministic.
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
	estimator := costestimate.NewEstimator(
		ai.NewCallReadStore(e.Pool), ai.NewRateStore(e.Pool), router,
		activities.NewStore(e.Pool), registry, backfillFixedClock{},
	)
	return &backfillWireEnv{
		env: e, registry: registry, gmail: gm, human: human,
		handlers: backfillHandlers{registry: registry, inserter: inserter, estimator: estimator, log: quiet},
	}
}

// backfillFixedClock pins the estimator's 7-day window so the preview wire test
// never depends on the wall clock (T11: inject a clock, never read the real one).
type backfillFixedClock struct{}

func (backfillFixedClock) Now() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }

// faultyEstimator is the backfillEstimator seam returning a cost-read fault, so
// the preview's degrade path (cost is transparency, never a gate) is drivable
// without a broken database.
type faultyEstimator struct{ err error }

func (f faultyEstimator) EstimateBackfill(context.Context, string, ids.UserID, int64) (costestimate.BackfillCost, error) {
	return costestimate.BackfillCost{}, f.err
}

// TestBackfillPreviewDegradesOnEstimatorFault proves the ADR-0068 guardrail: a
// cost-estimate fault must NOT fail the preview. The message count still answers
// 200; every estimator-sourced field (tokens, cost, currency, quality) is
// absent — never a fabricated 0 or a stale label — and the fault is logged, not
// swallowed (T2).
func TestBackfillPreviewDegradesOnEstimatorFault(t *testing.T) {
	b := setupBackfillWire(t)
	var logbuf bytes.Buffer
	h := b.handlers
	h.estimator = faultyEstimator{err: errors.New("rate store unreachable")}
	h.log = slog.New(slog.NewTextHandler(&logbuf, nil))

	var out crmcontracts.BackfillPreview
	code, _ := b.do(b.human, t, func(w http.ResponseWriter, r *http.Request) {
		h.PreviewConnectorBackfill(w, r, crmcontracts.Gmail)
	}, `{"window":"6m"}`, &out)

	if code != http.StatusOK {
		t.Fatalf("preview under estimator fault = %d, want 200 (cost is transparency, never a gate)", code)
	}
	if out.EstimatedMessages != 25 {
		t.Fatalf("estimated_messages = %d, want 25 (the message count survives a cost fault)", out.EstimatedMessages)
	}
	if out.EstimatedAiTokens != nil || out.EstimatedCostMinor != nil || out.Currency != nil || out.EstimateQuality != nil {
		t.Fatalf("estimator outputs must be absent on fault, got tokens=%v cost=%v currency=%v quality=%v",
			out.EstimatedAiTokens, out.EstimatedCostMinor, out.Currency, out.EstimateQuality)
	}
	if !strings.Contains(logbuf.String(), "backfill preview cost estimate") {
		t.Fatalf("estimator fault must be logged, not swallowed; got log %q", logbuf.String())
	}
}

// do invokes one backfill handler with a JSON body under ctx and decodes
// the response into out (when non-nil), returning status and problem code.
func (b *backfillWireEnv) do(ctx context.Context, t *testing.T, invoke func(http.ResponseWriter, *http.Request), body string, out any) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/backfill-op", bytes.NewReader([]byte(body))).WithContext(ctx)
	rec := httptest.NewRecorder()
	invoke(rec, req)
	raw := rec.Body.Bytes()
	var problem struct {
		Code string `json:"code"`
	}
	if len(raw) > 0 {
		// Every backfill response — success or problem — is JSON; anything
		// else is a transport defect this suite must surface, not mask.
		if err := json.Unmarshal(raw, &problem); err != nil {
			t.Fatalf("decoding response envelope %q: %v", raw, err)
		}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decoding %q: %v", raw, err)
		}
	}
	return rec.Code, problem.Code
}

func TestBackfillWire(t *testing.T) {
	b := setupBackfillWire(t)
	h := b.handlers

	preview := func(p crmcontracts.CaptureProvider) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { h.PreviewConnectorBackfill(w, r, p) }
	}
	start := func(p crmcontracts.CaptureProvider) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { h.StartConnectorBackfill(w, r, p) }
	}
	status := func(p crmcontracts.CaptureProvider) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { h.GetConnectorBackfillStatus(w, r, p) }
	}
	cancel := func(p crmcontracts.CaptureProvider) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { h.CancelConnectorBackfill(w, r, p) }
	}

	t.Run("an unwired role answers the declared 501 on every op", func(t *testing.T) {
		unwired := backfillHandlers{}
		for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){
			"preview": func(w http.ResponseWriter, r *http.Request) {
				unwired.PreviewConnectorBackfill(w, r, crmcontracts.Gmail)
			},
			"start": func(w http.ResponseWriter, r *http.Request) { unwired.StartConnectorBackfill(w, r, crmcontracts.Gmail) },
			"status": func(w http.ResponseWriter, r *http.Request) {
				unwired.GetConnectorBackfillStatus(w, r, crmcontracts.Gmail)
			},
			"cancel": func(w http.ResponseWriter, r *http.Request) {
				unwired.CancelConnectorBackfill(w, r, crmcontracts.Gmail)
			},
			"digest": func(w http.ResponseWriter, r *http.Request) {
				unwired.GetMorningDigest(w, r, crmcontracts.GetMorningDigestParams{})
			},
		} {
			if code, _ := b.do(b.human, t, invoke, "", nil); code != http.StatusNotImplemented {
				t.Fatalf("%s unwired = %d, want 501", name, code)
			}
		}
	})

	t.Run("every op is a signed-in human action", func(t *testing.T) {
		anon := principal.WithWorkspaceID(context.Background(), b.env.WS)
		for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){
			"preview": preview(crmcontracts.Gmail), "start": start(crmcontracts.Gmail),
			"status": status(crmcontracts.Gmail), "cancel": cancel(crmcontracts.Gmail),
		} {
			code, pcode := b.do(anon, t, invoke, `{"window":"6m"}`, nil)
			if code != http.StatusUnauthorized || pcode != "unauthorized" {
				t.Fatalf("%s without a principal = %d/%s, want 401/unauthorized (the contract's documented code)", name, code, pcode)
			}
		}
	})

	t.Run("preview refuses malformed and out-of-set windows", func(t *testing.T) {
		if code, pcode := b.do(b.human, t, preview(crmcontracts.Gmail), `{`, nil); code != http.StatusUnprocessableEntity || pcode != "window_required" {
			t.Fatalf("malformed body = %d/%s, want 422/window_required", code, pcode)
		}
		if code, pcode := b.do(b.human, t, preview(crmcontracts.Gmail), `{"window":"9m"}`, nil); code != http.StatusUnprocessableEntity || pcode != "window_invalid" {
			t.Fatalf("9m window = %d/%s, want 422/window_invalid", code, pcode)
		}
	})

	t.Run("preview 'none' is an honest zero — no scan, no spend", func(t *testing.T) {
		var out crmcontracts.BackfillPreview
		if code, _ := b.do(b.human, t, preview(crmcontracts.Gmail), `{"window":"none"}`, &out); code != http.StatusOK {
			t.Fatalf("none preview = %d, want 200", code)
		}
		if out.EstimatedMessages != 0 || string(out.Window) != "none" {
			t.Fatalf("none preview = %+v, want zero estimate", out)
		}
	})

	t.Run("preview carries the estimate and suppresses an unpriced cost honestly", func(t *testing.T) {
		var out crmcontracts.BackfillPreview
		if code, _ := b.do(b.human, t, preview(crmcontracts.Gmail), `{"window":"6m"}`, &out); code != http.StatusOK {
			t.Fatalf("preview = %d, want 200", code)
		}
		if out.EstimatedMessages != 25 {
			t.Fatalf("estimated_messages = %d, want 25 (the provider count)", out.EstimatedMessages)
		}
		// No ai_call history, no rate, no completed backfill for this connection →
		// the estimator falls to the work-shape floor: it still surfaces projected
		// tokens and marks the estimate heuristic, but with nothing priced it
		// SUPPRESSES the cost field (and currency) rather than fabricating a 0.
		if out.EstimatedAiTokens == nil || *out.EstimatedAiTokens <= 0 {
			t.Fatalf("estimated_ai_tokens = %+v, want floor tokens > 0", out.EstimatedAiTokens)
		}
		if out.EstimateQuality == nil || *out.EstimateQuality != crmcontracts.BackfillPreviewEstimateQualityHeuristic {
			t.Fatalf("estimate_quality = %+v, want heuristic (cold-start floor)", out.EstimateQuality)
		}
		if out.EstimatedCostMinor != nil {
			t.Fatalf("unpriced cost must be suppressed (nil), never a fabricated 0, got %+v", out.EstimatedCostMinor)
		}
		if out.Currency != nil {
			t.Fatalf("currency must be absent when cost is suppressed, got %+v", out.Currency)
		}
	})

	t.Run("a provider without a connection is a 404 on every op", func(t *testing.T) {
		for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){
			"preview": preview(crmcontracts.Gcal), "start": start(crmcontracts.Gcal),
			"status": status(crmcontracts.Gcal), "cancel": cancel(crmcontracts.Gcal),
		} {
			code, pcode := b.do(b.human, t, invoke, `{"window":"6m"}`, nil)
			if code != http.StatusNotFound || pcode != "connection_not_found" {
				t.Fatalf("%s without a connection = %d/%s, want 404/connection_not_found", name, code, pcode)
			}
		}
	})

	t.Run("a connector that cannot page backward is refused as unsupported", func(t *testing.T) {
		code, pcode := b.do(b.human, t, preview(crmcontracts.Graph), `{"window":"6m"}`, nil)
		if code != http.StatusUnprocessableEntity || pcode != "connector_unsupported" {
			t.Fatalf("non-Backfiller preview = %d/%s, want 422/connector_unsupported", code, pcode)
		}
	})

	t.Run("a provider outage on preview is the 502, never a fake estimate", func(t *testing.T) {
		b.gmail.estimateErr = errors.New("google is down")
		defer func() { b.gmail.estimateErr = nil }()
		code, pcode := b.do(b.human, t, preview(crmcontracts.Gmail), `{"window":"6m"}`, nil)
		if code != http.StatusBadGateway || pcode != "provider_unreachable" {
			t.Fatalf("outage preview = %d/%s, want 502/provider_unreachable", code, pcode)
		}
	})

	t.Run("start validates its window like preview", func(t *testing.T) {
		if code, pcode := b.do(b.human, t, start(crmcontracts.Gmail), `{`, nil); code != http.StatusUnprocessableEntity || pcode != "window_required" {
			t.Fatalf("malformed start = %d/%s, want 422/window_required", code, pcode)
		}
		if code, pcode := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"none"}`, nil); code != http.StatusUnprocessableEntity || pcode != "window_invalid" {
			t.Fatalf("start 'none' = %d/%s, want 422/window_invalid ('none' is not starting)", code, pcode)
		}
	})

	var runID string
	t.Run("start records the run, enqueues the pager, and answers 202", func(t *testing.T) {
		var out crmcontracts.BackfillStatus
		code, _ := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"6m"}`, &out)
		if code != http.StatusAccepted {
			t.Fatalf("start = %d, want 202", code)
		}
		if out.State != crmcontracts.BackfillStatusStateQueued || out.BackfillId == nil {
			t.Fatalf("started run = %+v, want queued with an id", out)
		}
		if out.Window == nil || string(*out.Window) != "6m" {
			t.Fatalf("started window = %+v, want 6m", out.Window)
		}
		if out.EstimatedMessages == nil || *out.EstimatedMessages != 25 {
			t.Fatalf("the previewed estimate must ride along as denominator, got %+v", out.EstimatedMessages)
		}
		runID = out.BackfillId.String()
	})

	t.Run("a second start while running is the 409", func(t *testing.T) {
		code, pcode := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"6m"}`, nil)
		if code != http.StatusConflict || pcode != "backfill_running" {
			t.Fatalf("second start = %d/%s, want 409/backfill_running", code, pcode)
		}
	})

	t.Run("status is the single-row activation read", func(t *testing.T) {
		var out crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, status(crmcontracts.Gmail), "", &out); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if out.State != crmcontracts.BackfillStatusStateQueued || out.Counts == nil {
			t.Fatalf("status = %+v, want queued with counts", out)
		}
	})

	worker := &captureBackfillWorker{registry: b.registry, log: b.handlers.log}
	t.Run("the pager worker refuses malformed job args", func(t *testing.T) {
		if err := worker.Work(context.Background(), &river.Job[CaptureBackfillArgs]{
			JobRow: &rivertype.JobRow{}, Args: CaptureBackfillArgs{Workspace: "not-a-uuid", BackfillID: runID},
		}); err == nil {
			t.Fatal("a malformed workspace id must fail the job")
		}
		if err := worker.Work(context.Background(), &river.Job[CaptureBackfillArgs]{
			JobRow: &rivertype.JobRow{}, Args: CaptureBackfillArgs{Workspace: b.env.WS.String(), BackfillID: "not-a-uuid"},
		}); err == nil {
			t.Fatal("a malformed backfill id must fail the job")
		}
	})

	t.Run("the pager worker walks the run to done", func(t *testing.T) {
		if err := worker.Work(context.Background(), &river.Job[CaptureBackfillArgs]{
			JobRow: &rivertype.JobRow{}, Args: CaptureBackfillArgs{Workspace: b.env.WS.String(), BackfillID: runID},
		}); err != nil {
			t.Fatalf("Work: %v", err)
		}
		var out crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, status(crmcontracts.Gmail), "", &out); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if out.State != crmcontracts.BackfillStatusStateDone {
			t.Fatalf("state = %s, want done (25 messages at 10/page finish in one 10-page tick)", out.State)
		}
		if out.Counts == nil || out.Counts.MessagesScanned == nil || *out.Counts.MessagesScanned != 25 {
			t.Fatalf("counts = %+v, want 25 scanned", out.Counts)
		}
	})

	t.Run("windows only widen — narrowing is the 409", func(t *testing.T) {
		code, pcode := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"3m"}`, nil)
		if code != http.StatusConflict || pcode != "window_narrowing" {
			t.Fatalf("narrowing start = %d/%s, want 409/window_narrowing", code, pcode)
		}
	})

	t.Run("a failed page records the class and the run finishes error", func(t *testing.T) {
		b.gmail.pageErr = errors.New("mailbox went away")
		defer func() { b.gmail.pageErr = nil }()
		var out crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"12m"}`, &out); code != http.StatusAccepted {
			t.Fatalf("widened start = %d, want 202", code)
		}
		// A page fault is recorded on the row, not retried by River.
		if err := worker.Work(context.Background(), &river.Job[CaptureBackfillArgs]{
			JobRow: &rivertype.JobRow{}, Args: CaptureBackfillArgs{Workspace: b.env.WS.String(), BackfillID: out.BackfillId.String()},
		}); err != nil {
			t.Fatalf("Work must absorb a page fault (the row owns retry), got %v", err)
		}
		var after crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, status(crmcontracts.Gmail), "", &after); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if after.State != crmcontracts.BackfillStatusStateError || after.LastErrorClass == nil {
			t.Fatalf("failed run = %+v, want state error with a recorded class", after)
		}
	})

	t.Run("cancel stops a live run and keeps what was captured", func(t *testing.T) {
		var started crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, start(crmcontracts.Gmail), `{"window":"12m"}`, &started); code != http.StatusAccepted {
			t.Fatalf("start = %d, want 202", code)
		}
		var out crmcontracts.BackfillStatus
		if code, _ := b.do(b.human, t, cancel(crmcontracts.Gmail), "", &out); code != http.StatusAccepted {
			t.Fatalf("cancel = %d, want 202", code)
		}
		if out.State != crmcontracts.BackfillStatusStateCancelled {
			t.Fatalf("cancelled run state = %s, want cancelled", out.State)
		}
		if code, pcode := b.do(b.human, t, cancel(crmcontracts.Gmail), "", nil); code != http.StatusConflict || pcode != "not_running" {
			t.Fatalf("cancel with nothing live = %d/%s, want 409/not_running", code, pcode)
		}
	})

	t.Run("a step on a vanished run is terminal, not a loop", func(t *testing.T) {
		wsCtx := principal.WithWorkspaceID(context.Background(), b.env.WS)
		done, completed, err := b.registry.RunBackfillStep(wsCtx, ids.NewV7())
		if !done || completed || err == nil {
			t.Fatalf("missing run step = done=%v completed=%v err=%v, want terminal-not-completed with the not-found error", done, completed, err)
		}
	})
}
