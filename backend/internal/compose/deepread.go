// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read end-to-end (R2/A102): a human's start queues a durable
// crawl job and answers 202; the worker role crawls the organization's
// site under the bounded siteCrawler, extracts every page through the
// shared evidence gate, and stages the merged findings as ONE ordinary
// "enrich" proposal — the same kind and accept executor scrapeCompany
// uses (deliberate v1: a deep read is a richer way to produce the same
// staged enrichment, so acceptance fills only empty fields exactly like a
// quick scrape). The dossier (people's site_read row) is the transparency
// surface the SPA polls: what was read, what was skipped and why, and the
// proposal the findings staged.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SiteDeepReadArgs is one queued deep read. The args carry everything the
// worker role needs to run without a request context: the tenant, the
// target, the dossier to advance, and the requesting human for the staged
// proposal's provenance.
type SiteDeepReadArgs struct {
	WorkspaceID    ids.UUID `json:"workspace_id"`
	OrganizationID ids.UUID `json:"organization_id"`
	SiteReadID     ids.UUID `json:"site_read_id"`
	SeedURL        string   `json:"seed_url"`
	RequestedBy    string   `json:"requested_by"`
}

// Kind is the stable job identifier River persists in river_job.
func (SiteDeepReadArgs) Kind() string { return "site_deep_read" }

// siteDeepReadInsertOpts deduplicates by args: the dossier id is unique
// per read, so a re-submitted enqueue of the SAME read collapses while a
// fresh read (new dossier) always queues.
func siteDeepReadInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// siteDeepReadWorker runs one queued deep read: claim the dossier, crawl,
// extract, stage, report. It is always registered on the worker role —
// with no model path it fails the read honestly instead of leaving it
// queued forever.
type siteDeepReadWorker struct {
	river.WorkerDefaults[SiteDeepReadArgs]
	people    *people.Store
	crawler   *siteCrawler
	extract   evidenceExtractor
	approvals *approvals.Service
	log       *slog.Logger
}

// newSiteDeepReadWorker assembles the worker-role deep read over one
// shared egress fetcher: the crawler walks pages through it and the
// extractor carries the same seam. brain may be nil — a picked-up read
// then finishes failed with an actionable log rather than sitting queued
// behind a worker that cannot extract.
func newSiteDeepReadWorker(pool *pgxpool.Pool, brain runner.Brain, log *slog.Logger) *siteDeepReadWorker {
	fetcher := webread.New()
	return &siteDeepReadWorker{
		people:    people.NewStore(pool),
		crawler:   newSiteCrawler(fetcher),
		extract:   evidenceExtractor{fetch: fetcher, brain: brain},
		approvals: approvals.NewService(pool),
		log:       log,
	}
}

func (w *siteDeepReadWorker) Work(ctx context.Context, job *river.Job[SiteDeepReadArgs]) error {
	return w.run(ctx, job.Args)
}

// run is the whole deep read, River-agnostic so tests drive it directly.
// Retry semantics rest on BeginSiteRead's CAS: any terminal outcome
// (done, partial, failed) leaves the dossier past "queued", so a River
// retry — including one after a recorded failure — CAS-misses and
// no-ops. One honest outcome per dossier, no zombie re-crawls; reading
// the site again is a human's next start, never an automatic retry.
func (w *siteDeepReadWorker) run(ctx context.Context, args SiteDeepReadArgs) error {
	requester := requestedByUserID(args.RequestedBy)
	ctx = principal.WithWorkspaceID(ctx, args.WorkspaceID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         "agent:deepread",
		UserID:     requester,
		OnBehalfOf: requester,
	})
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())

	if err := w.people.BeginSiteRead(ctx, args.SiteReadID); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// The CAS miss: the read is no longer queued — a rival replica
			// claimed it, or a prior attempt already recorded its outcome.
			return nil
		}
		return fmt.Errorf("site deep read %s: begin: %w", args.SiteReadID, err)
	}
	if w.extract.brain == nil {
		return w.fail(ctx, args.SiteReadID,
			errors.New("site deep read: worker has no model path — configure --ai-routing (or --ai-fake) on the worker role"))
	}

	// The crawler owns the 90 s wall (crawlWall) inside Crawl; a seed page
	// that cannot be read at all is a failed read, not an empty one.
	crawl, err := w.crawler.Crawl(ctx, args.SeedURL)
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
	}

	perPage := make([]pageFields, 0, len(crawl.Pages))
	var modelErr error
	for _, page := range crawl.Pages {
		fields, err := w.extract.extractFields(ctx, "Page "+page.URL, page.Text, page.URL, coldStartFieldValid)
		if err != nil {
			modelErr = fmt.Errorf("extracting %s: %w", page.URL, err)
			break
		}
		perPage = append(perPage, pageFields{kind: page.Kind, fields: fields})
	}
	merged := mergeCrawlFields(perPage)
	if modelErr != nil && len(merged) == 0 {
		// The model lane died before anything was evidenced: nothing honest
		// to report but the failure itself.
		return w.fail(ctx, args.SiteReadID, modelErr)
	}

	readPages := crawl.Pages
	status := "done"
	if crawl.Stopped != nil {
		status = "partial"
	}
	if modelErr != nil {
		// The model lane died midway with evidence already in hand: the
		// pages that got a model pass are the read, staged below like any
		// other — a partial that keeps what was honestly read, never a
		// failure that discards it. The terminal status makes the returned-
		// error retry churn pointless, so the cause is logged instead.
		status = "partial"
		readPages = crawl.Pages[:len(perPage)]
		w.log.ErrorContext(ctx, "site deep read degraded to partial: model lane failed midway",
			"read", args.SiteReadID.String(), "err", modelErr)
	}

	var proposalIDs []ids.UUID
	if len(merged) > 0 {
		approvalID, err := w.stage(ctx, args, merged, len(readPages))
		if err != nil {
			return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: staging the proposal: %w", args.SiteReadID, err))
		}
		proposalIDs = []ids.UUID{approvalID.UUID}
	}
	// Zero surviving fields is an honest empty read — done, fact_count 0,
	// no proposal — not an error: the site simply evidenced nothing.
	return w.finish(ctx, args.SiteReadID, status, readPages, crawl, len(merged), proposalIDs)
}

// stage records the merged findings as the ordinary "enrich" proposal —
// the exact payload shape scrapeaccept.go's executor unmarshals, so the
// existing accept effect applies a deep read with zero new machinery.
func (w *siteDeepReadWorker) stage(ctx context.Context, args SiteDeepReadArgs, merged []evidencedField, pagesRead int) (ids.ApprovalID, error) {
	fields := make([]crmcontracts.ColdStartField, len(merged))
	for i, f := range merged {
		fields[i] = crmcontracts.ColdStartField{
			Field:           crmcontracts.ColdStartFieldField(f.Field),
			Value:           f.Value,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceKind:      crmcontracts.ColdStartFieldSourceKindUrl,
			SourceUrl:       &f.SourceURL,
			Confidence:      f.Confidence,
		}
	}
	proposal := crmcontracts.EnrichmentProposal{
		OrganizationId: openapi_types.UUID(args.OrganizationID),
		SourceUrl:      args.SeedURL,
		Status:         crmcontracts.EnrichmentProposalStatusStaged,
		Fields:         fields,
	}
	proposedChange, err := json.Marshal(proposal)
	if err != nil {
		return ids.ApprovalID{}, err
	}
	digest := sha256.Sum256(proposedChange)
	return w.approvals.Stage(ctx, approvals.StageInput{
		Kind:           "enrich",
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     "organization",
		TargetID:       args.OrganizationID,
		Summary:        fmt.Sprintf("Deep site read of %s: %d evidenced fields from %d pages", args.SeedURL, len(merged), pagesRead),
	})
}

// finish records the crawl report on the dossier in one terminal write.
func (w *siteDeepReadWorker) finish(ctx context.Context, readID ids.UUID, status string, readPages []crawlPage, crawl siteCrawl, factCount int, proposalIDs []ids.UUID) error {
	in := people.FinishSiteReadInput{
		Status:      status,
		Pages:       make([]people.SiteReadPage, 0, len(readPages)),
		Skipped:     make([]people.SiteReadSkip, 0, len(crawl.Skipped)),
		FactCount:   factCount,
		ProposalIDs: proposalIDs,
	}
	for _, p := range readPages {
		in.Pages = append(in.Pages, people.SiteReadPage{URL: p.URL, Kind: string(p.Kind)})
	}
	for _, s := range crawl.Skipped {
		in.Skipped = append(in.Skipped, people.SiteReadSkip{URL: s.URL, Reason: string(s.Reason)})
	}
	if crawl.Stopped != nil {
		reason := string(*crawl.Stopped)
		in.StoppedReason = &reason
	}
	if err := w.people.FinishSiteRead(ctx, readID, in); err != nil {
		return fmt.Errorf("site deep read %s: finish: %w", readID, err)
	}
	return nil
}

// fail records the terminal failure on the dossier and returns the cause
// so River logs it on the job. A retry after a recorded failure is safe
// by construction — BeginSiteRead CAS-misses and the attempt no-ops.
func (w *siteDeepReadWorker) fail(ctx context.Context, readID ids.UUID, cause error) error {
	if err := w.people.FinishSiteRead(ctx, readID, people.FinishSiteReadInput{Status: "failed"}); err != nil {
		return errors.Join(cause, fmt.Errorf("recording the failure on the dossier: %w", err))
	}
	return cause
}

// pageFields pairs one crawled page's kind with its gate-surviving fields.
type pageFields struct {
	kind   crmcontracts.SiteReadPageKind
	fields []evidencedField
}

// mergeCrawlFields folds the per-page extractions into one answer per
// field with the same pairwise rule the quick read applies
// (mergeSiteFields): impressum-kind pages accumulate as the legal side,
// every other page in crawl order as the seed side, and the final merge
// lets the legal side win exactly the legal trio. Deterministic: crawl
// order is deterministic and each fold step is.
func mergeCrawlFields(pages []pageFields) []evidencedField {
	var seed, legal []evidencedField
	for _, p := range pages {
		if p.kind == crmcontracts.SiteReadPageKindImpressum {
			legal = mergeSiteFields(legal, p.fields)
		} else {
			seed = mergeSiteFields(seed, p.fields)
		}
	}
	return mergeSiteFields(seed, legal)
}

// requestedByUserID recovers the human uuid behind a "human:<uuid>"
// requested_by so the staged proposal carries OnBehalfOf. A requester
// without a recoverable uuid yields the zero uuid — the approval's
// on_behalf_of is then honestly NULL rather than the read failing over
// provenance.
func requestedByUserID(requestedBy string) ids.UUID {
	_, raw, found := strings.Cut(requestedBy, ":")
	if !found {
		return ids.UUID{}
	}
	id, err := ids.Parse(raw)
	if err != nil {
		return ids.UUID{}
	}
	return id
}

// deepReadEnqueuer is the slice of *jobs.Runner the start handler needs;
// tests fake it to count inserts.
type deepReadEnqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error
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
	var override string
	if r.ContentLength != 0 {
		var req crmcontracts.EnrichCompanyRequest
		if !httperr.Decode(w, r, &req) {
			return
		}
		if req.Url != nil {
			override = *req.Url
		}
	}
	if override != "" {
		parsed, err := url.Parse(override)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
			return
		}
	}
	seedURL := override
	if seedURL == "" {
		resolved, err := e.people.EnrichTargetURL(r.Context(), orgID)
		if errors.Is(err, people.ErrNoEnrichTarget) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   "company_unreadable",
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
	if err := e.people.BeginSiteRead(ctx, readID); err != nil {
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
