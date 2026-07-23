// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The rate-refresh transport: the two admin-only propose-refresh endpoints
// enqueue an async River job through the api role's insert-only runner (the api
// never crawls in-request — the worker does) and return 202 immediately. The
// unique window (ByArgs + activeSweepStates) makes a double-click a no-op rather
// than a second crawl. Without WithRateRefresh wired, both ops stay 501.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rateRefreshQueue isolates the rate refreshes (FX fetch + pricing-page
// crawl+LLM extract) from the default queue on their own bounded pool: each job
// is long, so a multi-workspace burst on the shared queue would starve the
// short maintenance jobs. Mirrors the deep-read precedent.
const (
	rateRefreshQueue      = "rate_refresh"
	rateRefreshMaxWorkers = 2
)

// rateRefreshEnqueuer is the enqueue seam (jobs.Runner.Enqueue); tests fake it.
type rateRefreshEnqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error
}

type rateRefreshHandlers struct {
	enqueue rateRefreshEnqueuer
}

func (h rateRefreshHandlers) ProposeFxRateRefresh(w http.ResponseWriter, r *http.Request) {
	h.enqueueRefresh(w, r, "fx_rate", func(ws ids.UUID, by string) river.JobArgs {
		return FxRateRefreshArgs{WorkspaceID: ws, RequestedBy: by}
	})
}

func (h rateRefreshHandlers) ProposeAiModelRateRefresh(w http.ResponseWriter, r *http.Request) {
	h.enqueueRefresh(w, r, "ai_model_rate", func(ws ids.UUID, by string) river.JobArgs {
		return AiModelRateRefreshArgs{WorkspaceID: ws, RequestedBy: by}
	})
}

func (h rateRefreshHandlers) enqueueRefresh(w http.ResponseWriter, r *http.Request, object string, mkArgs func(ids.UUID, string) river.JobArgs) {
	if h.enqueue == nil {
		httperr.NotImplemented(w, r, "rate refresh")
		return
	}
	ctx := r.Context()
	// Gate on Create — the same authority word the staged effect's write
	// (SetFxRate/SetModelRate) and its decision grant use; admin/ops hold it.
	if err := auth.Require(ctx, object, principal.ActionCreate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	args := mkArgs(storekit.MustWorkspace(ctx), requestedBy(ctx))
	// ByArgs uniqueness now hashes only the river:"unique"-tagged WorkspaceID
	// (RequestedBy is provenance, untagged), so two admins refreshing the same
	// workspace collapse to one in-flight crawl rather than racing two.
	opts := &river.InsertOpts{
		Queue:      rateRefreshQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
	}
	if err := h.enqueue.Enqueue(ctx, args, opts); err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, crmcontracts.RefreshAccepted{Status: crmcontracts.Enqueued})
}

// WithRateRefresh wires the api role's insert-only runner into the two
// propose-refresh handlers. Without it, both ops stay their explicit 501.
func WithRateRefresh(inserter rateRefreshEnqueuer) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.rateRefreshHandlers = rateRefreshHandlers{enqueue: inserter}
	}
}
