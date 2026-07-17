// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read end-to-end (R2/A102, category extraction per R4): a
// human's start queues a durable crawl job and answers 202; the worker
// role crawls the organization's site under the bounded siteCrawler and
// extracts every page through the shared evidence gate — the 11
// cold-start company fields on every page, plus at most one per-page-kind
// category call (company contact basics, offerings, market signals). The
// merged findings are staged as ONE "deepread" proposal whose acceptance
// lands both halves in one transaction: profile fields fill-empty exactly
// like a quick scrape, category facts land in organization_fact. The
// dossier (people's site_read row) is the transparency surface the SPA
// polls: what was read, what was skipped and why, and the proposal the
// findings staged.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
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

// deepReadQueue isolates deep reads from the default queue: a crawl holds a
// worker for minutes (crawl wall + model calls), so a burst of them on the
// shared queue would starve the short maintenance jobs. Its own bounded pool
// (deepReadMaxWorkers) caps how much of the fleet crawling can occupy.
const (
	deepReadQueue      = "deep_read"
	deepReadMaxWorkers = 2
)

// siteDeepReadInsertOpts routes the job to its own queue and deduplicates by
// args: the dossier id is unique per read, so a re-submitted enqueue of the
// SAME read collapses while a fresh read (new dossier) always queues.
func siteDeepReadInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:      deepReadQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
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

// Timeout overrides River's 1-minute default, which cannot hold a deep read:
// the crawl wall (≤90s) plus up to 24 model calls (12 pages × 2 category
// calls) at a slow tier plus the staging write. Eight minutes is that budget
// with headroom.
func (w *siteDeepReadWorker) Timeout(*river.Job[SiteDeepReadArgs]) time.Duration {
	return 8 * time.Minute
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
// deepReadWorkerCtx attaches the worker's principal, workspace and correlation
// onto the job context — the values every store write (and terminalCtx) needs.
func deepReadWorkerCtx(ctx context.Context, args SiteDeepReadArgs) context.Context {
	requester := requestedByUserID(args.RequestedBy)
	ctx = principal.WithWorkspaceID(ctx, args.WorkspaceID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         "agent:deepread",
		UserID:     requester,
		OnBehalfOf: requester,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

func (w *siteDeepReadWorker) run(ctx context.Context, args SiteDeepReadArgs) error {
	ctx = deepReadWorkerCtx(ctx, args)

	claim, err := w.people.BeginSiteRead(ctx, args.SiteReadID)
	if err != nil {
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
	crawl, err := w.crawler.Crawl(ctx, claim.SeedURL)
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
	}

	perPage := make([]pageFields, 0, len(crawl.Pages))
	var modelErr error
	for _, page := range crawl.Pages {
		extracted, err := w.extractPage(ctx, page)
		if err != nil {
			modelErr = fmt.Errorf("extracting %s: %w", page.URL, err)
			break
		}
		perPage = append(perPage, extracted)
	}
	mergedFields := mergeCrawlFields(perPage)
	mergedFacts := mergeCategoryFacts(perPage)
	mergedPeople := mergeTeamPeople(perPage)
	factCount := len(mergedFields) + len(mergedFacts)
	if modelErr != nil && factCount == 0 && len(mergedPeople) == 0 {
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

	proposalIDs, err := w.stageProposals(ctx, args.SiteReadID, claim, mergedFields, mergedFacts, mergedPeople, len(readPages))
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
	}
	// Zero surviving findings is an honest empty read — done, fact_count 0,
	// no proposal — not an error: the site simply evidenced nothing.
	return w.finish(ctx, args.SiteReadID, status, readPages, crawl, factCount, proposalIDs)
}

// extractPage runs one page's model passes: the shared 11-field company
// extraction, plus the page kind's ONE extra call when it has one — a
// category call for the fact-bearing kinds, the people call for team
// pages (R5). At most two calls per page, so a full 12-page crawl stays
// within budget.
func (w *siteDeepReadWorker) extractPage(ctx context.Context, page crawlPage) (pageFields, error) {
	fields, err := w.extract.extractFields(ctx, "Page "+page.URL, page.Text, page.URL, coldStartFieldValid)
	if err != nil {
		return pageFields{}, err
	}
	var facts []people.DeepReadFact
	if category, ok := factCategoryForPageKind(page.Kind); ok {
		facts, err = w.extract.extractCategory(ctx, category, "Page "+page.URL, page.Text, page.URL)
		if err != nil {
			return pageFields{}, err
		}
	}
	var published []sitePerson
	if page.Kind == crmcontracts.SiteReadPageKindTeam {
		published, err = w.extract.extractPeople(ctx, "Page "+page.URL, page.Text, page.URL)
		if err != nil {
			return pageFields{}, err
		}
	}
	return pageFields{kind: page.Kind, fields: fields, facts: facts, people: published}, nil
}

// stageProposals stages everything the read evidenced: the ONE deepread
// bundle first (when any field or fact survived), then one thin
// site_lead per published person (R5) — the dossier's proposal_ids keep
// that order.
func (w *siteDeepReadWorker) stageProposals(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, mergedFields []evidencedField, mergedFacts []people.DeepReadFact, mergedPeople []sitePerson, pagesRead int) ([]ids.UUID, error) {
	var proposalIDs []ids.UUID
	if len(mergedFields)+len(mergedFacts) > 0 {
		approvalID, err := w.stage(ctx, readID, claim, mergedFields, mergedFacts, pagesRead)
		if err != nil {
			return nil, fmt.Errorf("staging the proposal: %w", err)
		}
		proposalIDs = []ids.UUID{approvalID.UUID}
	}
	for _, person := range mergedPeople {
		approvalID, err := w.stageSiteLead(ctx, readID, claim, person)
		if err != nil {
			return nil, fmt.Errorf("staging the %s lead: %w", person.Name, err)
		}
		proposalIDs = append(proposalIDs, approvalID.UUID)
	}
	return proposalIDs, nil
}

// stage records the merged findings as ONE "deepread" proposal carrying
// both halves of the read — the profile fields the existing fill-empty
// machinery applies and the category facts bound for organization_fact —
// plus the dossier id, so the accept effect links the landed facts back
// to the read that evidenced them.
func (w *siteDeepReadWorker) stage(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, mergedFields []evidencedField, mergedFacts []people.DeepReadFact, pagesRead int) (ids.ApprovalID, error) {
	fields := make([]people.DeepReadField, len(mergedFields))
	for i, f := range mergedFields {
		fields[i] = people.DeepReadField{
			Field:           f.Field,
			Value:           f.Value,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceURL:       f.SourceURL,
			Confidence:      f.Confidence,
		}
	}
	proposedChange, err := json.Marshal(people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](claim.OrganizationID),
		SourceURL:      claim.SeedURL,
		SiteReadID:     readID,
		Fields:         fields,
		Facts:          mergedFacts,
	})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	digest := sha256.Sum256(proposedChange)
	return w.approvals.Stage(ctx, approvals.StageInput{
		Kind:           deepReadProposalKind,
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     enrichTargetType,
		TargetID:       claim.OrganizationID,
		Summary:        fmt.Sprintf("Deep site read of %s: %d fields, %d facts from %d pages", claim.SeedURL, len(mergedFields), len(mergedFacts), pagesRead),
	})
}

// stageSiteLead records ONE published person as a thin "site_lead"
// proposal (R5): exactly what the site printed, nothing enriched. Each
// person is decided on their own — accepting the CTO does not accept the
// whole roster.
func (w *siteDeepReadWorker) stageSiteLead(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, person sitePerson) (ids.ApprovalID, error) {
	proposedChange, err := json.Marshal(siteLeadProposal{
		OrganizationID:  claim.OrganizationID,
		SiteReadID:      readID,
		Name:            person.Name,
		Role:            person.Role,
		PublishedEmail:  person.PublishedEmail,
		LinkedinURL:     person.LinkedinURL,
		EvidenceSnippet: person.EvidenceSnippet,
		SourceURL:       person.SourceURL,
	})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	digest := sha256.Sum256(proposedChange)
	return w.approvals.Stage(ctx, approvals.StageInput{
		Kind:           siteLeadProposalKind,
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     enrichTargetType,
		TargetID:       claim.OrganizationID,
		Summary:        fmt.Sprintf("Lead from %s: %s — %s", claim.SeedURL, person.Name, person.Role),
	})
}

// finish records the crawl report on the dossier in one terminal write.
// terminalCtx derives the context for a terminal dossier write: the work
// context's VALUES (principal, workspace — WithWorkspaceTx reads the tenant
// GUC from them) with a fresh deadline of its own, NEVER the work context's
// deadline or cancellation. Closing the dossier must not be starved by the
// crawl+extract work it reports on — otherwise a read whose model calls
// exhausted the job budget is left running forever, squatting the org's one
// in-flight slot. Fifteen seconds bounds the single FinishSiteRead tx.
func terminalCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

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
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.FinishSiteRead(tctx, readID, in); err != nil {
		return fmt.Errorf("site deep read %s: finish: %w", readID, err)
	}
	return nil
}

// fail records the terminal failure on the dossier and returns the cause
// so River logs it on the job. A retry after a recorded failure is safe
// by construction — BeginSiteRead CAS-misses and the attempt no-ops.
func (w *siteDeepReadWorker) fail(ctx context.Context, readID ids.UUID, cause error) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.FinishSiteRead(tctx, readID, people.FinishSiteReadInput{Status: "failed"}); err != nil {
		return errors.Join(cause, fmt.Errorf("recording the failure on the dossier: %w", err))
	}
	return cause
}

// deepReadProposalKind is the staged proposal's wire identity — one
// spelling for the staging worker and the accept executor
// (deepreadaccept.go). Distinct from the quick scrape's "enrich": a deep
// read's acceptance also lands category facts.
const deepReadProposalKind = "deepread"

// pageFields pairs one crawled page's kind with its gate-surviving
// findings: the shared company fields, the page kind's category facts,
// and — on team pages — the published people.
type pageFields struct {
	kind   crmcontracts.SiteReadPageKind
	fields []evidencedField
	facts  []people.DeepReadFact
	people []sitePerson
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
