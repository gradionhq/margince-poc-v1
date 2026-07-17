// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's api-role half: start creates-or-joins the dossier and
// queues the crawl job through the insert-only runner (the api never
// crawls in-request — the worker role does, deepread.go), and report
// answers the SPA's poll with the dossier as it stands.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// deepReadEnqueuer is the slice of *jobs.Runner the start handler needs;
// tests fake it to count inserts.
type deepReadEnqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error
}

// decodeSeedOverride reads the optional body override and validates it; it
// writes the problem response itself and reports whether the caller may
// proceed (an empty override with ok=true means "use the org's own domain").
func decodeSeedOverride(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.ContentLength == 0 {
		return "", true
	}
	var req crmcontracts.EnrichCompanyRequest
	if !httperr.Decode(w, r, &req) {
		return "", false
	}
	if req.Url == nil {
		return "", true
	}
	parsed, err := url.Parse(*req.Url)
	if err != nil || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) || parsed.Host == "" {
		httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
		return "", false
	}
	return *req.Url, true
}

// deepReadEngine backs the transport: start creates-or-joins the dossier
// and queues the crawl job; report is the SPA's poll.
type deepReadEngine struct {
	people  *people.Store
	enqueue deepReadEnqueuer
	log     *slog.Logger
}

// start resolves the seed URL (body override, else the org's own domain),
// creates or joins the dossier, and — only for a fresh dossier — enqueues
// the crawl job. 202 either way: the read to poll is the answer.
func (e *deepReadEngine) start(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	orgID := ids.From[ids.OrganizationKind](ids.UUID(id))
	override, ok := decodeSeedOverride(w, r)
	if !ok {
		return
	}
	seedURL := override
	if seedURL == "" {
		resolved, err := e.people.EnrichTargetURL(r.Context(), orgID)
		if errors.Is(err, people.ErrNoEnrichTarget) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   companyUnreadable,
				Detail: "This company has no website on file. Add a URL to read from.",
			})
			return
		}
		if err != nil {
			// EnsureVisible's existence-hiding 404 and the rest ride the
			// sentinel mapping.
			httperr.Write(w, r, err)
			return
		}
		seedURL = resolved
	}

	read, joined, err := e.people.StartSiteRead(r.Context(), orgID, seedURL, requestedBy(r.Context()))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if !joined {
		wsID, _ := principal.WorkspaceID(r.Context())
		err := e.enqueue.Enqueue(r.Context(), SiteDeepReadArgs{
			WorkspaceID:    wsID,
			OrganizationID: orgID.UUID,
			SiteReadID:     read.ID,
			SeedURL:        read.SeedURL,
			RequestedBy:    read.RequestedBy,
		}, siteDeepReadInsertOpts())
		if err != nil {
			// A queued dossier with no job behind it would hold the org's
			// in-flight slot forever (a re-click JOINS it, never re-queues),
			// so close it as failed before reporting the error; the caller's
			// next start then mints a fresh read.
			e.closeUnqueued(r.Context(), read.ID)
			httperr.Write(w, r, fmt.Errorf("queueing the site read: %w", err))
			return
		}
	}
	status := crmcontracts.SiteReadStartedStatusQueued
	if joined {
		status = crmcontracts.SiteReadStartedStatusRunning
	}
	httperr.WriteJSON(w, http.StatusAccepted, crmcontracts.SiteReadStarted{
		ReadId: openapi_types.UUID(read.ID),
		Status: status,
	})
}

// closeUnqueued flips a dossier whose job never made the queue to failed
// through the worker's own CAS pair. Best-effort: the request already
// reports the enqueue failure; a dossier this cannot close is logged so
// the stuck in-flight slot is findable.
func (e *deepReadEngine) closeUnqueued(ctx context.Context, readID ids.UUID) {
	if _, err := e.people.BeginSiteRead(ctx, readID); err != nil {
		e.log.ErrorContext(ctx, "site read stuck queued: could not claim it to record the enqueue failure",
			"read", readID.String(), "err", err)
		return
	}
	if err := e.people.FinishSiteRead(ctx, readID, people.FinishSiteReadInput{Status: "failed"}); err != nil {
		e.log.ErrorContext(ctx, "site read stuck running: could not record the enqueue failure",
			"read", readID.String(), "err", err)
	}
}

// report answers the SPA's poll with the dossier as it stands.
func (e *deepReadEngine) report(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID) {
	read, err := e.people.GetSiteRead(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), ids.UUID(readID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, siteReadReport(read))
}

// siteReadReport maps the dossier onto the contract report. Lists are
// always concrete (empty, never null): the report's whole point is an
// explicit account.
func siteReadReport(read people.SiteRead) crmcontracts.SiteReadReport {
	report := crmcontracts.SiteReadReport{
		ReadId:         openapi_types.UUID(read.ID),
		OrganizationId: openapi_types.UUID(read.OrganizationID.UUID),
		SeedUrl:        read.SeedURL,
		Status:         crmcontracts.SiteReadReportStatus(read.Status),
		Pages:          make([]crmcontracts.SiteReadPage, 0, len(read.Pages)),
		Skipped:        make([]crmcontracts.SiteReadSkip, 0, len(read.Skipped)),
		ProposalIds:    make([]openapi_types.UUID, 0, len(read.ProposalIDs)),
		FactCount:      &read.FactCount,
		CreatedAt:      read.CreatedAt,
		FinishedAt:     read.FinishedAt,
	}
	for _, p := range read.Pages {
		report.Pages = append(report.Pages, crmcontracts.SiteReadPage{Url: p.URL, Kind: crmcontracts.SiteReadPageKind(p.Kind)})
	}
	for _, s := range read.Skipped {
		report.Skipped = append(report.Skipped, crmcontracts.SiteReadSkip{Url: s.URL, Reason: crmcontracts.SiteReadSkipReason(s.Reason)})
	}
	for _, id := range read.ProposalIDs {
		report.ProposalIds = append(report.ProposalIds, openapi_types.UUID(id))
	}
	if read.StoppedReason != nil {
		reason := crmcontracts.SiteReadReportStoppedReason(*read.StoppedReason)
		report.StoppedReason = &reason
	}
	return report
}

// requestedBy names the requesting principal on the dossier
// ("human:<uuid>"); an unauthenticated context yields "" and
// StartSiteRead's own gate refuses the call before anything is written.
func requestedBy(ctx context.Context) string {
	if p, ok := principal.Actor(ctx); ok {
		return p.ID
	}
	return ""
}

// WithDeepRead enables the deep-read transport on the api role: start
// queues the crawl job through the insert-only runner (the api never
// crawls in-request — the worker role does), report serves the poll.
// Without it both operations stay their explicit 501.
func WithDeepRead(inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		engine := &deepReadEngine{people: people.NewStore(pool), enqueue: inserter, log: s.log}
		s.siteReadHandlers = siteReadHandlers{start: engine.start, report: engine.report}
	}
}

// siteReadHandlers shadows the generated DeepReadCompany / GetSiteRead stubs.
// Both fields nil until WithDeepRead wires the engine.
type siteReadHandlers struct {
	start  func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
	report func(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID)
}

func (h siteReadHandlers) DeepReadCompany(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.start == nil {
		httperr.NotImplemented(w, r, "deepReadCompany (no crawl runner configured)")
		return
	}
	h.start(w, r, id)
}

func (h siteReadHandlers) GetSiteRead(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID) {
	if h.report == nil {
		httperr.NotImplemented(w, r, "getSiteRead (no crawl runner configured)")
		return
	}
	h.report(w, r, id, readID)
}
