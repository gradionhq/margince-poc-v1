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
	"log/slog"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// deepReadEnqueuer is the slice of *jobs.Runner the start handler needs;
// tests fake it to count inserts.
type deepReadEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

type runTransparencyReader interface {
	Get(ctx context.Context, correlationID ids.UUID) (ai.RunSummary, error)
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
	people    *people.Store
	approvals *approvals.Service
	runtime   runTransparencyReader
	brain     completer
	enqueue   deepReadEnqueuer
	log       *slog.Logger
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

	read, joined, err := e.people.StartSiteReadQueued(r.Context(), orgID, seedURL, requestedBy(r.Context()),
		func(ctx context.Context, tx pgx.Tx, read people.SiteRead) error {
			return e.enqueue.EnqueueTx(ctx, tx, SiteDeepReadArgs{
				WorkspaceID:    storekit.MustWorkspace(ctx),
				OrganizationID: orgID.UUID,
				SiteReadID:     read.ID,
				SeedURL:        read.SeedURL,
				RequestedBy:    read.RequestedBy,
			}, siteDeepReadInsertOpts())
		})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	status := crmcontracts.SiteReadStartedStatusQueued
	if joined {
		status = crmcontracts.SiteReadStartedStatusRunning
		if read.Status == siteReadStatusDeferred {
			status = crmcontracts.SiteReadStartedStatusDeferred
		}
	}
	httperr.WriteJSON(w, http.StatusAccepted, crmcontracts.SiteReadStarted{
		ReadId: openapi_types.UUID(read.ID),
		Status: status,
	})
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
	if read.OrganizationID == nil {
		panic("siteReadReport called for an unbound onboarding dossier")
	}
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
		StatusDetail:   read.StatusDetail,
		NextAttemptAt:  read.NextAttemptAt,
	}
	if read.StatusCode != nil {
		code := crmcontracts.SiteReadReportStatusCode(*read.StatusCode)
		report.StatusCode = &code
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
	report.PagesRead = &read.PagesRead
	if read.Phase != nil {
		phase := crmcontracts.SiteReadReportPhase(*read.Phase)
		report.Phase = &phase
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
func WithDeepRead(inserter *jobs.Runner, brain completer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		engine := &deepReadEngine{
			people: people.NewStore(pool), approvals: approvals.NewService(pool),
			runtime: ai.NewRunTransparency(pool), brain: brain, enqueue: inserter, log: s.log,
		}
		rollout := s.companyContextRollout
		s.siteReadHandlers = siteReadHandlers{engine: engine, start: engine.start, report: engine.report, companyContextRollout: rollout}
		s.assistant = &onboardingCompanyAssistant{
			state: s.state, people: people.NewStore(pool),
			brain: brain, runtime: ai.NewRunTransparency(pool),
			rollout: &s.companyContextRollout,
		}
	}
}

// siteReadHandlers shadows the generated DeepReadCompany / GetSiteRead stubs.
// Both fields nil until WithDeepRead wires the engine.
type siteReadHandlers struct {
	engine                *deepReadEngine
	start                 func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
	report                func(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID)
	companyContextRollout string
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
