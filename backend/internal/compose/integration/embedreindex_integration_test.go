// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The embed-reindex transport end to end (Task 15, ADR-0068 design
// §5.6-swap): the three /embeddings/reindex* HTTP ops over a real
// migrated Postgres, the CAS+enqueue-in-one-tx confirm, and the REAL
// River worker (compose.NewJobRunner, exactly as cmd/worker wires it —
// no hand-called ReembedCorpus standing in for the worker role).
// Completion/cancellation is observed on River's own subscription
// channels (SubscribeCompleted/SubscribeCancelled, subscribed before
// Start), never a sleep or a poll — the same idiom
// no_activity_reminder_workqueue_integration_test.go established for
// this package's other real-worker suites (applyRiverSchema/
// awaitKindCompleted are that file's helpers, reused here unchanged).
//
// The 409-vs-202 split rides the enqueue's own unique-skip outcome
// (EnqueueTxUnique, Task 11), proven here through the real handler, not
// asserted against the store directly — search.Store's own CAS/rollup
// behaviour is already covered by binding_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// embedReindexStatusWire mirrors crmcontracts.EmbedReindexStatus field by
// field, decoded loosely so a wire-shape regression fails an assertion
// here instead of silently zeroing.
type embedReindexStatusWire struct {
	ConfiguredIdentity string `json:"configured_identity"`
	PopulatedIdentity  string `json:"populated_identity"`
	Status             string `json:"status"`
	ReindexNeeded      bool   `json:"reindex_needed"`
	EntitiesPending    int    `json:"entities_pending"`
	PerWorkspace       []struct {
		EntitiesPending int    `json:"entities_pending"`
		WorkspaceID     string `json:"workspace_id"`
	} `json:"per_workspace"`
}

// embedReindexPreviewWire mirrors crmcontracts.EmbedReindexPreview.
type embedReindexPreviewWire struct {
	ComputedAt         string  `json:"computed_at"`
	Currency           *string `json:"currency"`
	EntitiesPending    int     `json:"entities_pending"`
	EstimateQuality    string  `json:"estimate_quality"`
	EstimatedAiTokens  *int    `json:"estimated_ai_tokens"`
	EstimatedCostMinor *int    `json:"estimated_cost_minor"`
	PerWorkspace       []struct {
		EntitiesPending   int    `json:"entities_pending"`
		EstimatedAiTokens *int   `json:"estimated_ai_tokens"`
		UtilizationImpact string `json:"utilization_impact"`
		WorkspaceID       string `json:"workspace_id"`
	} `json:"per_workspace"`
}

// embedReindexProblem absorbs the RFC 7807 shapes this suite's error
// scenarios produce: permission_denied, and this transport's own
// reindex_running / reindex_not_needed / reindex_identity_drift codes.
type embedReindexProblem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// embedReindexRouter builds a DB-less local router (compose.NewLocalModelPath)
// over the deterministic offline fake, bound to a named embeddings model —
// naming it (rather than leaving ai.FakeRoutingConfig's Embeddings.Model
// empty) matters because EmbedIdentity() reports the unhelpful unbound
// "", 0 identity otherwise (embedding_integration_test.go's own fakeEmbedder
// doc explains this). Two different modelName values yield two genuinely
// distinct identities, the raw material the identity-drift scenarios need.
func embedReindexRouter(t *testing.T, modelName string) *ai.Router {
	t.Helper()
	cfg := ai.FakeRoutingConfig()
	cfg.Embeddings = ai.EmbeddingsConfig{
		ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake, Model: modelName},
		Dimensions:     fakeEmbedDims,
	}
	modelPath, err := compose.NewLocalModelPath(cfg, ai.WithoutResultCache())
	if err != nil {
		t.Fatalf("NewLocalModelPath: %v", err)
	}
	return modelPath.Router()
}

// setupEmbedReindex boots the HTTP harness with the embed-reindex ops
// wired over router and its own insert-only River client (the api-side
// half of the api-enqueues/worker-works split — deepReadOption's own
// pattern in cmd/api), seeds the binding marker at router's OWN identity
// (the vacuous "nothing populated yet, nothing configured differently"
// baseline binding_integration_test.go's SeedBinding test proves), and
// leaves the bootstrap admin's session in the client jar.
func setupEmbedReindex(t *testing.T, router *ai.Router) *env {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	// A separate pool from the pointed-at-the-same-DSN pool env's own
	// setupWithOptions opens below — jobs.NewInserter just needs SOME
	// pool reaching the same Postgres, not the exact same *pgxpool.Pool
	// object (mirrors SchemaPool(t)'s own separate-connection precedent).
	wirePool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening the insert-only wiring pool: %v", err)
	}
	t.Cleanup(wirePool.Close)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(wirePool, quiet)
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}

	e := setupWithOptions(t, compose.WithEmbedReindex(router, inserter))
	// The confirm handler enqueues into river_job on ITS OWN transaction
	// (search.Store.ClaimAndEnqueueReembedding), so the schema must exist
	// before the FIRST confirm call, not merely before a worker starts —
	// applyRiverSchema is idempotent (existence-guarded), so every
	// caller of this setup pays for it at most once per process.
	applyRiverSchema(t)
	bootstrapWorkspaceSession(t, e, "Embed Reindex E2E", "embed-reindex@fable.test", "Admin")
	e.slug = "embed-reindex-e2e" // slugify("Embed Reindex E2E")

	identity, _ := router.EmbedIdentity()
	if err := search.NewStore(e.pool).SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	return e
}

// embedReindexWorkspaceID resolves the bootstrapped workspace's raw id —
// there is no /v1/workspaces endpoint to read it from, so this reads it
// the same way demoteToRep/setWorkspaceSeat already do (owner connection,
// by slug).
func embedReindexWorkspaceID(t *testing.T, e *env) string {
	t.Helper()
	var wsID string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	return wsID
}

// seedStaleEmbeddingRow plants one person carrying an embedding row under
// a DIFFERENT identity than currentIdentity — the "swap case"
// binding_integration_test.go's TestReindexNeededAfterStaleIdentityRow
// proves at the store: an entity with an embedding row, just not a
// CURRENT one, must still count as pending.
func seedStaleEmbeddingRow(t *testing.T, e *env, wsID string) {
	t.Helper()
	ctx := context.Background()
	var personID string
	if err := e.owner.QueryRow(ctx,
		`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Stale Row Person', 'manual', 'human:x') RETURNING id`,
		wsID).Scan(&personID); err != nil {
		t.Fatalf("seeding the stale-row person: %v", err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', 'fake/stale-identity@1024', '[1,2,3]'::vector)`,
		wsID, personID); err != nil {
		t.Fatalf("seeding the stale embedding row: %v", err)
	}
}

// newEmbedReindexRunner builds (but does not start) the real worker-role
// River runner over embedder — compose.NewJobRunner exactly as
// cmd/worker wires it, so the embed_reindex job that runs here is the
// same code the production worker binary runs, not a hand-called
// ReembedCorpus standing in for it. The three always-on periodic passes
// are given a long interval so they don't interleave noise into this
// suite's completion/cancellation stream.
func newEmbedReindexRunner(t *testing.T, e *env, embedder search.Embedder) *jobs.Runner {
	t.Helper()
	applyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := compose.NewJobRunner(e.pool, quiet, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		Embedder:          embedder,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	return runner
}

// startEmbedReindexRunner starts runner and registers its graceful stop —
// callers subscribe BEFORE calling this so no completion/cancellation
// is missed (the no_activity_reminder suite's own ordering).
func startEmbedReindexRunner(t *testing.T, runner *jobs.Runner) {
	t.Helper()
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
}

// embedStatus/embedPreview/embedConfirm are the three ops' thin call
// wrappers — every scenario below drives the SAME transport, never the
// store directly (that coverage is binding_integration_test.go's).
func embedStatus(t *testing.T, e *env) (int, embedReindexStatusWire, embedReindexProblem) {
	t.Helper()
	return embedReindexDecode[embedReindexStatusWire](t, e, "GET", "/v1/embeddings/reindex/status", nil)
}

func embedPreview(t *testing.T, e *env) (int, embedReindexPreviewWire, embedReindexProblem) {
	t.Helper()
	return embedReindexDecode[embedReindexPreviewWire](t, e, "GET", "/v1/embeddings/reindex/preview", nil)
}

// embedConfirm issues the confirm call. body is nil for a bare confirm
// (an empty request — decodeEmbedReindexStart's zero-value path) or an
// anyMap carrying previewed_identity/force; passed straight through as
// `any` so a literal nil stays a true nil interface (never a boxed nil
// map, which would marshal to the 4-byte literal "null" instead of an
// empty body — harmless to the handler's own decode either way, but this
// keeps the ContentLength==0 path genuinely exercised).
func embedConfirm(t *testing.T, e *env, body any) (int, embedReindexStatusWire, embedReindexProblem) {
	t.Helper()
	return embedReindexDecode[embedReindexStatusWire](t, e, "POST", "/v1/embeddings/reindex", body)
}

// embedReindexDecode issues one call and decodes the raw body as whichever
// shape the status code implies — a 2xx success payload of type T, or a
// 4xx/5xx problem+json — mirroring customFieldWire's own
// decide-on-status-first pattern in this package (a problem envelope's
// numeric `status` field would fail to unmarshal into a mismatched
// success shape, so the status code must pick the target type first).
func embedReindexDecode[T any](t *testing.T, e *env, method, path string, body any) (int, T, embedReindexProblem) {
	t.Helper()
	var raw json.RawMessage
	status := e.call(t, method, path, body, nil, &raw)
	var success T
	var problem embedReindexProblem
	if len(raw) == 0 {
		return status, success, problem
	}
	if status >= http.StatusBadRequest {
		if err := json.Unmarshal(raw, &problem); err != nil {
			t.Fatalf("decoding problem body: %v (body: %s)", err, raw)
		}
		return status, success, problem
	}
	if err := json.Unmarshal(raw, &success); err != nil {
		t.Fatalf("decoding success body: %v (body: %s)", err, raw)
	}
	return status, success, problem
}

// TestEmbedReindexStatusAndPreviewReflectPendingEntities proves the two
// read ops (status: returns reindex_needed after a stale-identity row is
// present; preview: labeled per-workspace+total) over the real handler +
// search.Store.
func TestEmbedReindexStatusAndPreviewReflectPendingEntities(t *testing.T) {
	router := embedReindexRouter(t, "reindex-status-v1")
	e := setupEmbedReindex(t, router)
	wsID := embedReindexWorkspaceID(t, e)
	seedStaleEmbeddingRow(t, e, wsID)

	identity, _ := router.EmbedIdentity()

	status, st, _ := embedStatus(t, e)
	if status != http.StatusOK {
		t.Fatalf("status -> %d, want 200", status)
	}
	if st.ConfiguredIdentity != identity || st.PopulatedIdentity != identity {
		t.Fatalf("status identities = configured=%q populated=%q, want both %q", st.ConfiguredIdentity, st.PopulatedIdentity, identity)
	}
	if st.Status != "idle" {
		t.Fatalf("status.status = %q, want idle (nothing confirmed yet)", st.Status)
	}
	if !st.ReindexNeeded {
		t.Fatal("a stale-identity row must read reindex_needed = true")
	}
	if st.EntitiesPending < 1 {
		t.Fatalf("entities_pending = %d, want >= 1 (the seeded stale row)", st.EntitiesPending)
	}
	if len(st.PerWorkspace) != 1 || st.PerWorkspace[0].WorkspaceID != wsID || st.PerWorkspace[0].EntitiesPending != st.EntitiesPending {
		t.Fatalf("per_workspace = %+v, want exactly one row for %s matching the fleet total %d", st.PerWorkspace, wsID, st.EntitiesPending)
	}

	pstatus, pv, _ := embedPreview(t, e)
	if pstatus != http.StatusOK {
		t.Fatalf("preview -> %d, want 200", pstatus)
	}
	if pv.EstimateQuality != "heuristic" {
		t.Fatalf("estimate_quality = %q, want heuristic", pv.EstimateQuality)
	}
	if pv.EntitiesPending != st.EntitiesPending {
		t.Fatalf("preview entities_pending = %d, want it to match status's %d", pv.EntitiesPending, st.EntitiesPending)
	}
	if len(pv.PerWorkspace) != 1 || pv.PerWorkspace[0].WorkspaceID != wsID {
		t.Fatalf("preview per_workspace = %+v, want exactly one row for %s", pv.PerWorkspace, wsID)
	}
	if pv.PerWorkspace[0].EstimatedAiTokens == nil {
		t.Fatal("per-workspace estimated_ai_tokens must be present (a work-shape floor, never fabricated as absent)")
	}
}

// TestEmbedReindexConfirmRequiresAdminOrOps proves the object RBAC gate:
// a rep (read-only on embedding_reindex per migration 0114) sees status/
// preview exactly like an admin, but confirm answers 403 — discharging
// the rbacgate_test.go waiver's own promise that this transport is what
// gates search.Store's binding-marker methods.
func TestEmbedReindexConfirmRequiresAdminOrOps(t *testing.T) {
	router := embedReindexRouter(t, "reindex-rbac-v1")
	e := setupEmbedReindex(t, router)

	if status, _, _ := embedStatus(t, e); status != http.StatusOK {
		t.Fatalf("admin status -> %d, want 200", status)
	}

	demoteToRep(t, e)

	if status, _, _ := embedStatus(t, e); status != http.StatusOK {
		t.Fatalf("rep status -> %d, want 200 (read is granted to every role)", status)
	}
	if status, _, _ := embedPreview(t, e); status != http.StatusOK {
		t.Fatalf("rep preview -> %d, want 200 (read is granted to every role)", status)
	}
	status, _, problem := embedConfirm(t, e, nil)
	if status != http.StatusForbidden || problem.Code != "permission_denied" {
		t.Fatalf("rep confirm -> %d %+v, want 403 permission_denied", status, problem)
	}
}

// TestEmbedReindexConfirmLifecycle drives the full CAS+enqueue+worker
// loop through the real handler and the real River worker: confirm
// flips idle->reembedding and enqueues; a second confirm while that job
// is still live is 409 reindex_running (the enqueue's own unique-skip,
// not the CAS); discarding the live job (simulating an exhausted-retry
// terminal state) lets a fresh confirm re-enqueue (202); starting the
// real worker then re-embeds the fleet and the status read returns to
// idle/not-needed.
func TestEmbedReindexConfirmLifecycle(t *testing.T) {
	router := embedReindexRouter(t, "reindex-lifecycle-v1")
	e := setupEmbedReindex(t, router)
	wsID := embedReindexWorkspaceID(t, e)
	seedStaleEmbeddingRow(t, e, wsID)

	status, confirmed, _ := embedConfirm(t, e, nil)
	if status != http.StatusAccepted {
		t.Fatalf("first confirm -> %d, want 202", status)
	}
	if confirmed.Status != "reembedding" {
		t.Fatalf("status after confirm = %q, want reembedding", confirmed.Status)
	}

	status, _, problem := embedConfirm(t, e, nil)
	if status != http.StatusConflict || problem.Code != "reindex_running" {
		t.Fatalf("second confirm while live -> %d %+v, want 409 reindex_running", status, problem)
	}

	if _, err := e.owner.Exec(context.Background(),
		`UPDATE river_job SET state = 'discarded', finalized_at = now() WHERE kind = 'embed_reindex'`); err != nil {
		t.Fatalf("simulating the discarded job: %v", err)
	}

	status, confirmed, _ = embedConfirm(t, e, nil)
	if status != http.StatusAccepted {
		t.Fatalf("confirm after the discarded job -> %d, want 202 (a discarded job must not block a fresh confirm)", status)
	}
	if confirmed.Status != "reembedding" {
		t.Fatalf("status after re-confirm = %q, want reembedding", confirmed.Status)
	}

	runner := newEmbedReindexRunner(t, e, router)
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()
	startEmbedReindexRunner(t, runner)

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, "embed_reindex")

	status, final, _ := embedStatus(t, e)
	if status != http.StatusOK {
		t.Fatalf("final status -> %d, want 200", status)
	}
	if final.ReindexNeeded {
		t.Fatal("after a completed re-embed, reindex_needed must be false")
	}
	if final.Status != "idle" {
		t.Fatalf("final status.status = %q, want idle", final.Status)
	}
	if final.EntitiesPending != 0 {
		t.Fatalf("final entities_pending = %d, want 0 (the whole fleet was re-embedded)", final.EntitiesPending)
	}
	identity, _ := router.EmbedIdentity()
	if final.PopulatedIdentity != identity {
		t.Fatalf("final populated_identity = %q, want %q", final.PopulatedIdentity, identity)
	}
}

// TestEmbedReindexConfirmRefusesIdentityDrift proves the confirm's own
// drift check: a previewed_identity that no longer matches what's
// configured NOW (the operator changed the embed binding between preview
// and confirm) is refused with 409, and — critically — no job is enqueued
// for it (a second, driftless confirm right after still gets a fresh 202,
// proving the drifted attempt left no live claim behind).
func TestEmbedReindexConfirmRefusesIdentityDrift(t *testing.T) {
	router := embedReindexRouter(t, "reindex-drift-v1")
	e := setupEmbedReindex(t, router)
	wsID := embedReindexWorkspaceID(t, e)
	seedStaleEmbeddingRow(t, e, wsID)

	status, _, problem := embedConfirm(t, e, anyMap{"previewed_identity": "fake/someone-else@1024"})
	if status != http.StatusConflict || problem.Code != "reindex_identity_drift" {
		t.Fatalf("drifted confirm -> %d %+v, want 409 reindex_identity_drift", status, problem)
	}

	identity, _ := router.EmbedIdentity()
	status, confirmed, _ := embedConfirm(t, e, anyMap{"previewed_identity": identity})
	if status != http.StatusAccepted {
		t.Fatalf("matching-identity confirm -> %d, want 202 (the drifted attempt must not have left a live claim)", status)
	}
	if confirmed.Status != "reembedding" {
		t.Fatalf("status after confirm = %q, want reembedding", confirmed.Status)
	}
}

// TestEmbedReindexIdentityMismatchedJobCancels proves the worker's own
// entry guard: a job enqueued under one identity, picked up by a worker
// role whose CURRENT embed lane reports a DIFFERENT identity (the
// operator changed the config after enqueue, before the worker ran),
// cancels via river.JobCancel rather than retrying — observed as a
// genuine river_job 'cancelled' terminal state, not 25 burned attempts.
func TestEmbedReindexIdentityMismatchedJobCancels(t *testing.T) {
	enqueueRouter := embedReindexRouter(t, "reindex-cancel-enqueue-v1")
	e := setupEmbedReindex(t, enqueueRouter)

	// force=true: this scenario is about the WORKER's own drift guard,
	// not about whether the store happens to derive reindex-needed —
	// force is the affordance that lets the enqueue happen regardless.
	status, confirmed, _ := embedConfirm(t, e, anyMap{"force": true})
	if status != http.StatusAccepted || confirmed.Status != "reembedding" {
		t.Fatalf("confirm -> %d %+v, want 202/reembedding", status, confirmed)
	}

	// The worker role's OWN embed lane reports a DIFFERENT identity than
	// the job was enqueued under — the drift the entry guard must catch.
	driftedRouter := embedReindexRouter(t, "reindex-cancel-drifted-v1")
	runner := newEmbedReindexRunner(t, e, driftedRouter)
	sub, cancelSub := runner.SubscribeCancelled()
	defer cancelSub()
	startEmbedReindexRunner(t, runner)

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, "embed_reindex")

	var jobState string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT state FROM river_job WHERE kind = 'embed_reindex' ORDER BY id DESC LIMIT 1`).Scan(&jobState); err != nil {
		t.Fatalf("reading the job's terminal state: %v", err)
	}
	if jobState != "cancelled" {
		t.Fatalf("job state = %q, want cancelled (river.JobCancel, not a burned retry)", jobState)
	}

	// The marker must still read reembedding — a cancelled job must NOT
	// silently stamp the binding as complete under an identity nothing
	// actually re-embedded the corpus for.
	status, st, _ := embedStatus(t, e)
	if status != http.StatusOK {
		t.Fatalf("status -> %d", status)
	}
	if st.Status != "reembedding" {
		t.Fatalf("status.status = %q after a cancelled job, want reembedding (the marker must not be stamped complete)", st.Status)
	}
}

// TestEmbedReindexForceReindexWhenNotNeeded proves the v6 B2 rebuild
// affordance: once the store is genuinely caught up (reindex_needed
// false), a bare confirm refuses (409 reindex_not_needed), but the SAME
// confirm with force=true still flips to reembedding and the worker
// still completes a real re-embed pass — the force path is not merely
// accepted, it actually runs.
func TestEmbedReindexForceReindexWhenNotNeeded(t *testing.T) {
	router := embedReindexRouter(t, "reindex-force-v1")
	e := setupEmbedReindex(t, router)

	// setupEmbedReindex seeds the binding marker at router's OWN identity
	// and this test creates no entity, so the store starts genuinely
	// caught up (reindex_needed=false, 0 pending) — the exact precondition
	// this scenario needs, with no priming pass required.
	status, caughtUp, _ := embedStatus(t, e)
	if status != http.StatusOK || caughtUp.ReindexNeeded || caughtUp.EntitiesPending != 0 {
		t.Fatalf("baseline status = %d %+v, want 200 with reindex_needed=false and 0 pending", status, caughtUp)
	}

	status, _, problem := embedConfirm(t, e, nil)
	if status != http.StatusConflict || problem.Code != "reindex_not_needed" {
		t.Fatalf("bare confirm on a caught-up store -> %d %+v, want 409 reindex_not_needed", status, problem)
	}

	status, forced, _ := embedConfirm(t, e, anyMap{"force": true})
	if status != http.StatusAccepted {
		t.Fatalf("forced confirm -> %d, want 202 (force is the rebuild affordance)", status)
	}
	if forced.Status != "reembedding" {
		t.Fatalf("forced confirm status = %q, want reembedding", forced.Status)
	}

	runner2 := newEmbedReindexRunner(t, e, router)
	sub2, cancelSub2 := runner2.SubscribeCompleted()
	defer cancelSub2()
	startEmbedReindexRunner(t, runner2)
	waitCtx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	awaitKindCompleted(waitCtx2, t, sub2, "embed_reindex")

	status, final, _ := embedStatus(t, e)
	if status != http.StatusOK || final.Status != "idle" || final.ReindexNeeded {
		t.Fatalf("after the forced rebuild, status = %d %+v, want idle/not-needed", status, final)
	}
}
