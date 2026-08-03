// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The embed-reindex transport (ADR-0068 design §5.6-swap, Task 15): the
// three /embeddings/reindex* ops discharge the rbacgate_test.go waiver
// on search.Store's binding-marker READS and its claim — every one of
// them is reached ONLY through a handler below that gates first
// (auth.Require(ctx, "embedding_reindex", <action>)), which is the whole
// premise those store methods were allowed to skip their own object
// RBAC check. The rest of the marker's lifecycle belongs to the run the
// claim starts (jobs_embedreindex.go), where there is no human principal
// to admit and the claim is the authority.
//
// Confirm is the CAS+enqueue-in-one-tx shape (mirrors
// deepreadtransport.go's start): search.Store.ClaimAndEnqueueReembedding
// owns the transaction, the callback enqueues the River dispatcher inside
// it — a rolled-back enqueue always undoes the claim. The CAS itself is
// what tells a fresh claim (202) apart from a run already holding the
// marker (409 reindex_running): the run's target identity IS the claim,
// and it outlives the dispatcher row, which completes as soon as it has
// fanned the fleet out (jobs_embedreindex.go).

import (
	"context"
	"errors"
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

// reembedStaleAfter is how long a run may leave the marker untouched before a
// FORCED confirm is allowed to take it back. Every child bumps updated_at as it
// finishes, so a run that has not moved this long is a run nothing is finishing.
//
// It matches River's own RescueAfter, which is what makes the number principled
// rather than picked: past that window River has itself already rescued or
// discarded whatever was running, so a marker still unmoved is being held by
// nothing. Stealing is safe by construction rather than by timing — the stolen
// run's stragglers carry a run id the marker no longer names, so they act on
// nothing — and this bound only decides how long a human waits.
const reembedStaleAfter = time.Hour

// embedReindexEnqueuer is the slice of *jobs.Runner the confirm handler
// needs: the insert rides the claim's own transaction, so a claim that
// could not queue its dispatcher rolls back whole.
type embedReindexEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
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
// signal. Read is admin/ops-only (migration 0115) — the RBAC gate runs
// first so the grant is enforced here, not assumed from the contract text.
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

// decodeEmbedReindexStart reads the optional confirm body (the contract's
// EmbedReindexStartRequest: previewed_identity compared to what's
// configured NOW, catching an operator who changed the embed binding
// between preview and confirm; force, the v6 B2 "rebuild index"
// affordance). An empty body is the zero request (no drift check, no
// force) — it writes the problem response itself and reports whether the
// caller may proceed.
func decodeEmbedReindexStart(w http.ResponseWriter, r *http.Request) (crmcontracts.EmbedReindexStartRequest, bool) {
	if r.ContentLength == 0 {
		return crmcontracts.EmbedReindexStartRequest{}, true
	}
	var req crmcontracts.EmbedReindexStartRequest
	if !httperr.Decode(w, r, &req) {
		return crmcontracts.EmbedReindexStartRequest{}, false
	}
	return req, true
}

// embedReindexDriftError answers the 409 when the caller's previewed
// identity no longer matches what's configured now — nil previewedIdentity
// (absent body field) or an empty string both mean "no prior preview to
// compare against," so no check runs.
func embedReindexDriftError(previewedIdentity *string, configured string) error {
	if previewedIdentity == nil || *previewedIdentity == "" || *previewedIdentity == configured {
		return nil
	}
	return &httperr.DetailedError{
		Status: http.StatusConflict,
		Code:   "reindex_identity_drift",
		Detail: "the embed binding changed since this reindex was previewed; preview again before confirming",
	}
}

// embedReindexNotNeededError answers the 409 that stops a no-op confirm: no
// pending work, no reindex already in flight, and the caller didn't pass
// force.
func embedReindexNotNeededError(needed bool, jobStatus string, force bool) error {
	if needed || jobStatus == reembeddingStatus || force {
		return nil
	}
	return &httperr.DetailedError{
		Status: http.StatusConflict,
		Code:   "reindex_not_needed",
		Detail: "the store is already current under the configured embed binding; pass force to rebuild anyway",
	}
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
	if driftErr := embedReindexDriftError(req.PreviewedIdentity, configured); driftErr != nil {
		httperr.Write(w, r, driftErr)
		return
	}
	force := req.Force != nil && *req.Force

	needed, err := e.store.ReindexNeeded(ctx, configured)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	_, jobStatus, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if notNeededErr := embedReindexNotNeededError(needed, jobStatus, force); notNeededErr != nil {
		httperr.Write(w, r, notNeededErr)
		return
	}

	// A config change while a prior run still holds the marker is refused
	// here rather than queueing a second, differently-identitied run over
	// the first. It still heals without a human: the in-flight run's own
	// children cancel on the drift they now see (search.ErrIdentityDrift →
	// river.JobCancel), which releases the marker for this confirm to retake.
	//
	// force carries a second meaning here, and it is the recovery path: it takes
	// the marker off a run that has stopped moving. A run whose last child was
	// killed outright never reaches the code that would release it — River
	// discards a job rescued past its attempts without running it — and without
	// this the marker would be held forever with no job left to explain it.
	claim := search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: configured}
	if force {
		claim.StealAfter = reembedStaleAfter
	}
	err = e.store.ClaimAndEnqueueReembedding(ctx, claim, func(tx pgx.Tx) error {
		return e.enqueue.EnqueueTx(ctx, tx, EmbedReindexArgs{Run: claim.Run, Identity: configured},
			&river.InsertOpts{MaxAttempts: embedReindexMaxAttempts})
	})
	if errors.Is(err, search.ErrReembeddingInFlight) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "reindex_running",
			Detail: "a fleet-wide reindex is already running; pass force to take over one that has made no progress for an hour",
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
	populated, jobStatus, updatedAt, err := e.store.PopulatedIdentity(ctx)
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
		UpdatedAt:          updatedAt,
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
// role), OR with a router whose EmbedIdentity() is "" (--ai-fake, or any
// routing config that never bound an embeddings model — brain.go's
// seedEmbedBinding never plants a marker for this shape either), there is
// no embed lane to report on or trigger, so the three ops stay their
// generated 501 — the same declared-by-omission posture as
// WithColdStart/WithScrape. Without this second self-gating nil, an
// unbound router would still wire the handlers, and every one of them
// would 500 reading a marker that was never seeded.
func WithEmbedReindex(router *ai.Router, inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if router == nil || inserter == nil {
			return
		}
		if identity, _ := router.EmbedIdentity(); identity == "" {
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
