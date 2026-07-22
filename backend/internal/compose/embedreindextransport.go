// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The embed-reindex transport (ADR-0068 design §5.6-swap, Task 15): the
// three /embeddings/reindex* ops discharge the rbacgate_test.go waiver
// on search.Store's six binding-marker methods — every one of them is
// reached ONLY through a handler below that gates first
// (auth.Require(ctx, "embedding_reindex", <action>)), which is the whole
// premise those store methods were allowed to skip their own object
// RBAC check.
//
// Confirm is the CAS+enqueue-in-one-tx shape (mirrors
// deepreadtransport.go's start): search.Store.ClaimAndEnqueueReembedding
// owns the transaction, the callback enqueues the River job inside it —
// a rolled-back enqueue always undoes the claim. The enqueue's OWN
// unique-skip outcome (jobs.Runner.EnqueueTxUnique, Task 11), never the
// CAS's row count, is what tells a fresh claim (202) apart from an
// already-running job (409 reindex_running) — the CAS alone cannot tell
// "moved from idle" from "was already reembedding" (binding.go's own
// doc).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/costestimate"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reembeddingStatus is the embed_store_binding.status value the marker
// carries while a fleet-wide re-embed is in flight (binding.go's own CAS).
const reembeddingStatus = "reembedding"

// embedReindexArgs is the River job the confirm handler enqueues: the
// identity in force at enqueue time, so a mid-flight config change is
// detectable as drift downstream (search.ErrIdentityDrift) rather than
// the worker silently completing under whatever it now reports.
type embedReindexArgs struct {
	Identity string `json:"identity"`
}

// Kind is the stable job identifier River persists in river_job.
func (embedReindexArgs) Kind() string { return "embed_reindex" }

// errReindexAlreadyRunning signals the confirm callback's own unique-skip
// outcome (EnqueueTxUnique inserted=false, Task 11) up through the store-
// owned ClaimAndEnqueueReembedding transaction, so the handler answers
// 409 reindex_running instead of committing a claim over a job that was
// never actually queued.
var errReindexAlreadyRunning = errors.New("compose: a fleet-wide reindex is already running")

// embedReindexEnqueuer is the slice of *jobs.Runner the confirm handler
// needs — EnqueueTxUnique's dedupe outcome is what tells 202 apart from
// 409 (Task 11); a test fakes it to drive both outcomes without a live
// worker.
type embedReindexEnqueuer interface {
	EnqueueTxUnique(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (bool, error)
}

// embedReindexEstimator is costestimate.EmbedReindexEstimator's narrow
// seam — an interface so a handler-level test can inject a fault-
// returning fake without a live Postgres rate/budget read.
type embedReindexEstimator interface {
	EstimateEmbedReindex(ctx context.Context, currentIdentity string) ([]costestimate.Row, costestimate.Row, error)
}

// embedReindexEngine backs the three handlers over the search module's
// binding-marker store, the resolved embed lane, the priced preview, and
// the insert-only job enqueuer.
type embedReindexEngine struct {
	store     *search.Store
	embedder  search.Embedder
	estimator embedReindexEstimator
	enqueue   embedReindexEnqueuer
	clock     costestimate.Clock
}

// currentIdentity is the embedder's cheap, no-API-call stamp — the value
// every read and the confirm's drift check compares against.
func (e *embedReindexEngine) currentIdentity() string {
	identity, _ := e.embedder.EmbedIdentity()
	return identity
}

// status answers the binding marker plus the derived reindex-needed
// signal. Read is granted to every role (migration 0114) — the RBAC gate
// still runs first so a de-permissioned role (or a future tightening of
// the read grant) is enforced here, not assumed from the contract text.
func (e *embedReindexEngine) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.Require(ctx, "embedding_reindex", principal.ActionRead); err != nil {
		httperr.Write(w, r, err)
		return
	}
	resp, err := e.statusBody(ctx)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// preview answers the scope-before-the-spend estimate (ADR-0020): the
// same fleet-wide pending set status reports, priced at the current
// embed binding's rate. Read-gated, same posture as status.
func (e *embedReindexEngine) preview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.Require(ctx, "embedding_reindex", principal.ActionRead); err != nil {
		httperr.Write(w, r, err)
		return
	}
	configured := e.currentIdentity()
	perWorkspace, total, err := e.estimator.EstimateEmbedReindex(ctx, configured)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, embedReindexPreviewWire(perWorkspace, total, e.clock.Now()))
}

// embedReindexStartRequest is the confirm body: the identity the SPA
// previewed against (compared to what's configured NOW, catching an
// operator who changed the embed binding between preview and confirm —
// empty skips the check, e.g. a force-reindex issued with no prior
// preview) and the explicit force flag, the v6 B2 "rebuild index"
// affordance that lets an operator re-run even when nothing is derived
// as pending.
type embedReindexStartRequest struct {
	PreviewedIdentity string `json:"previewed_identity"`
	Force             bool   `json:"force"`
}

// decodeEmbedReindexStart reads the optional confirm body; an empty body
// is the zero request (no drift check, no force) — it writes the
// problem response itself and reports whether the caller may proceed.
func decodeEmbedReindexStart(w http.ResponseWriter, r *http.Request) (embedReindexStartRequest, bool) {
	if r.ContentLength == 0 {
		return embedReindexStartRequest{}, true
	}
	var req embedReindexStartRequest
	if !httperr.Decode(w, r, &req) {
		return embedReindexStartRequest{}, false
	}
	return req, true
}

// confirm claims the binding marker and enqueues the fleet-wide re-embed
// job in ONE transaction (ClaimAndEnqueueReembedding), admin/ops-gated
// (the embedding_reindex object's update grant) and human-only at the
// contract (x-agent-access: human-only) — a passport/agent principal
// never reaches this handler's write.
func (e *embedReindexEngine) confirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.Require(ctx, "embedding_reindex", principal.ActionUpdate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	req, ok := decodeEmbedReindexStart(w, r)
	if !ok {
		return
	}

	configured := e.currentIdentity()
	if req.PreviewedIdentity != "" && req.PreviewedIdentity != configured {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "reindex_identity_drift",
			Detail: "the embed binding changed since this reindex was previewed; preview again before confirming",
		})
		return
	}

	needed, err := e.store.ReindexNeeded(ctx, configured)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	_, jobStatus, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if !needed && jobStatus != reembeddingStatus && !req.Force {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "reindex_not_needed",
			Detail: "the store is already current under the configured embed binding; pass force to rebuild anyway",
		})
		return
	}

	err = e.store.ClaimAndEnqueueReembedding(ctx, func(tx pgx.Tx) error {
		inserted, enqErr := e.enqueue.EnqueueTxUnique(ctx, tx, embedReindexArgs{Identity: configured},
			&river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}})
		if enqErr != nil {
			return enqErr
		}
		if !inserted {
			return errReindexAlreadyRunning
		}
		return nil
	})
	if errors.Is(err, errReindexAlreadyRunning) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "reindex_running",
			Detail: "a fleet-wide reindex is already running",
		})
		return
	}
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	resp, err := e.statusBody(ctx)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, resp)
}

// statusBody assembles the wire status from the store's own reads —
// shared by the status handler and the confirm handler's 202 body (the
// SAME read, so the client sees exactly what it would GET-poll next).
func (e *embedReindexEngine) statusBody(ctx context.Context) (crmcontracts.EmbedReindexStatus, error) {
	configured := e.currentIdentity()
	populated, jobStatus, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		return crmcontracts.EmbedReindexStatus{}, err
	}
	needed, err := e.store.ReindexNeeded(ctx, configured)
	if err != nil {
		return crmcontracts.EmbedReindexStatus{}, err
	}
	counts, err := e.store.PendingByWorkspace(ctx, configured)
	if err != nil {
		return crmcontracts.EmbedReindexStatus{}, err
	}

	total := 0
	perWorkspace := make([]struct {
		EntitiesPending int                `json:"entities_pending"`
		WorkspaceId     openapi_types.UUID `json:"workspace_id"` //nolint:staticcheck // matches the generated EmbedReindexStatus.PerWorkspace item shape
	}, 0, len(counts))
	for _, wsID := range sortedEmbedWorkspaceIDs(counts) {
		c := counts[wsID]
		total += c
		perWorkspace = append(perWorkspace, struct {
			EntitiesPending int                `json:"entities_pending"`
			WorkspaceId     openapi_types.UUID `json:"workspace_id"` //nolint:staticcheck // matches the generated EmbedReindexStatus.PerWorkspace item shape
		}{EntitiesPending: c, WorkspaceId: openapi_types.UUID(wsID.UUID)})
	}

	return crmcontracts.EmbedReindexStatus{
		ConfiguredIdentity: configured,
		PopulatedIdentity:  populated,
		Status:             crmcontracts.EmbedReindexStatusStatus(jobStatus),
		ReindexNeeded:      needed,
		EntitiesPending:    total,
		PerWorkspace:       perWorkspace,
	}, nil
}

// embedReindexPreviewWire maps the priced per-workspace rows plus the
// fleet total onto the contract's preview shape. now is the estimate's
// computed_at stamp (the engine's injected clock — never time.Now() here,
// T11).
func embedReindexPreviewWire(rows []costestimate.Row, total costestimate.Row, now time.Time) crmcontracts.EmbedReindexPreview {
	currency := total.Currency
	tokens := int(total.Tokens)
	resp := crmcontracts.EmbedReindexPreview{
		ComputedAt:        now,
		Currency:          &currency,
		EntitiesPending:   total.Entities,
		EstimateQuality:   crmcontracts.EmbedReindexPreviewEstimateQuality(total.Quality),
		EstimatedAiTokens: &tokens,
	}
	if total.CostMinor != nil {
		minor := int(*total.CostMinor)
		resp.EstimatedCostMinor = &minor
	}

	resp.PerWorkspace = make([]struct {
		EntitiesPending int `json:"entities_pending"`

		EstimatedAiTokens *int `json:"estimated_ai_tokens,omitempty"`

		UtilizationImpact crmcontracts.EmbedReindexPreviewPerWorkspaceUtilizationImpact `json:"utilization_impact"`
		WorkspaceId       openapi_types.UUID                                            `json:"workspace_id"` //nolint:staticcheck // matches the generated EmbedReindexPreview.PerWorkspace item shape
	}, 0, len(rows))
	for _, row := range rows {
		rowTokens := int(row.Tokens)
		resp.PerWorkspace = append(resp.PerWorkspace, struct {
			EntitiesPending int `json:"entities_pending"`

			EstimatedAiTokens *int `json:"estimated_ai_tokens,omitempty"`

			UtilizationImpact crmcontracts.EmbedReindexPreviewPerWorkspaceUtilizationImpact `json:"utilization_impact"`
			WorkspaceId       openapi_types.UUID                                            `json:"workspace_id"` //nolint:staticcheck // matches the generated EmbedReindexPreview.PerWorkspace item shape
		}{
			EntitiesPending:   row.Entities,
			EstimatedAiTokens: &rowTokens,
			UtilizationImpact: crmcontracts.EmbedReindexPreviewPerWorkspaceUtilizationImpact(row.UtilizationImpact),
			WorkspaceId:       openapi_types.UUID(row.WorkspaceID.UUID),
		})
	}
	return resp
}

// sortedEmbedWorkspaceIDs orders a pending-count map's keys
// deterministically — counts arrive from a Go map (no fleet-enumeration
// order survives it), and a stable per-workspace ordering is what makes
// the status/preview wire output reproducible run to run (mirrors
// costestimate's own sortedWorkspaceIDs, a different package's private
// helper over the same shape).
func sortedEmbedWorkspaceIDs(counts map[ids.WorkspaceID]int) []ids.WorkspaceID {
	workspaceIDs := make([]ids.WorkspaceID, 0, len(counts))
	for wsID := range counts {
		workspaceIDs = append(workspaceIDs, wsID)
	}
	sort.Slice(workspaceIDs, func(i, j int) bool {
		return workspaceIDs[i].String() < workspaceIDs[j].String()
	})
	return workspaceIDs
}

// embedReindexHandlers shadows the generated EmbedReindexStatus /
// EmbedReindexPreview / EmbedReindexStart stubs. engine nil means no
// model path is configured on this role (WithEmbedReindex never ran) —
// every op stays its explicit 501, never a silent 404 or a nil-deref.
type embedReindexHandlers struct {
	engine *embedReindexEngine
}

func (h embedReindexHandlers) EmbedReindexStatus(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "EmbedReindexStatus")
		return
	}
	h.engine.status(w, r)
}

func (h embedReindexHandlers) EmbedReindexPreview(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "EmbedReindexPreview")
		return
	}
	h.engine.preview(w, r)
}

func (h embedReindexHandlers) EmbedReindexStart(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "EmbedReindexStart")
		return
	}
	h.engine.confirm(w, r)
}

// WithEmbedReindex wires the /embeddings/reindex* ops over the resolved
// embed lane's identity/estimator and an insert-only River client (the
// api enqueues, the worker re-embeds — WithDeepRead's own split, this
// module's own confirm/worker pair). Without a router (an AI-unconfigured
// role) there is no embed lane to report on or trigger, so the three ops
// stay their generated 501 — the same declared-by-omission posture as
// WithColdStart/WithScrape.
func WithEmbedReindex(router *ai.Router, inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if router == nil || inserter == nil {
			return
		}
		store := search.NewStore(pool)
		estimator := costestimate.NewEmbedReindexEstimator(
			store, ai.NewRateStore(pool), router, NewSeatBudget(pool), ai.NewMeter(pool), systemClock{},
		)
		s.embedReindexHandlers = embedReindexHandlers{engine: &embedReindexEngine{
			store:     store,
			embedder:  router,
			estimator: estimator,
			enqueue:   inserter,
			clock:     systemClock{},
		}}
	}
}

// embedReindexWorker delegates a River job to the search module's fleet-
// wide re-embed (search.Store.ReembedCorpus). An identity drift — the
// operator changed the embed binding after this job was enqueued —
// cancels rather than retries: retrying would burn attempts against an
// identity nothing serves anymore, when what the fleet needs is a NEW
// confirm under the current config.
type embedReindexWorker struct {
	river.WorkerDefaults[embedReindexArgs]
	store    *search.Store
	embedder search.Embedder
}

func (w *embedReindexWorker) Work(ctx context.Context, job *river.Job[embedReindexArgs]) error {
	if w.embedder == nil {
		// This worker role has no embed lane configured (JobRunnerConfig.
		// Embedder is nil) — registered regardless (jobs.go's own doc), so
		// a picked-up job fails clearly here rather than sitting queued
		// forever behind a job no worker role can ever complete.
		return fmt.Errorf("embed_reindex: no embed lane configured on this worker role")
	}
	err := w.store.ReembedCorpus(ctx, w.embedder, job.Args.Identity)
	if errors.Is(err, search.ErrIdentityDrift) {
		return river.JobCancel(err)
	}
	return err
}
