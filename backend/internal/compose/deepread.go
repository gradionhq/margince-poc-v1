// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read end-to-end: a
// human's start queues a durable crawl job and answers 202; the worker
// role crawls the organization's site under the bounded siteCrawler,
// folds the pages into a labeled corpus, and extracts it in ONE model
// call (chunked only for outsized sites) through the no-guess evidence
// gate — company fields, category facts, published people, and the
// site's legal-entity census. The gated findings are staged as ONE
// "deepread" proposal whose acceptance lands both halves in one
// transaction: profile fields fill-empty exactly like a quick scrape,
// category facts land in organization_fact. The dossier (people's
// site_read row) is the transparency surface the SPA polls: live phase
// and page counts while running, then what was read, what was skipped
// and why, and the proposals the findings staged.

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

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
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
	people     *people.Store
	crawler    *siteCrawler
	extract    evidenceExtractor
	approvals  *approvals.Service
	autoEnrich *capture.AutoEnrichStore
	log        *slog.Logger
	caps       CrawlCaps
	now        func() time.Time
}

// newSiteDeepReadWorker assembles the worker-role deep read over one
// shared egress fetcher: the crawler walks pages through it and the
// extractor carries the same seam. brain may be nil — a picked-up read
// then finishes failed with an actionable log rather than sitting queued
// behind a worker that cannot extract.
func newSiteDeepReadWorker(pool *pgxpool.Pool, brain, factBrain completer, log *slog.Logger, caps CrawlCaps) *siteDeepReadWorker {
	fetcher := webread.New()
	caps = caps.withDefaults()
	return &siteDeepReadWorker{
		people:     people.NewStore(pool),
		crawler:    newSiteCrawler(fetcher, caps),
		extract:    evidenceExtractor{fetch: fetcher, brain: brain, factBrain: factBrain},
		approvals:  approvals.NewService(pool),
		autoEnrich: capture.NewAutoEnrichStore(pool),
		log:        log,
		caps:       caps,
		now:        time.Now,
	}
}

// extractLaneBudget is the parallel extraction's allowance in the
// job-timeout arithmetic: the page fan-out and the profile call run
// concurrently, each a small fast call plus the validator's retry-and-
// escalate headroom.
const extractLaneBudget = 90 * time.Second

// Timeout overrides River's 1-minute default: the crawl wall plus the
// parallel extraction budget plus a minute for the staging and dossier
// writes — floored at eight minutes so a tightened cap never squeezes
// the terminal writes.
func (w *siteDeepReadWorker) Timeout(*river.Job[SiteDeepReadArgs]) time.Duration {
	budget := w.caps.Wall + extractLaneBudget + time.Minute
	if floor := 8 * time.Minute; budget < floor {
		return floor
	}
	return budget
}

// reclaimAfter leaves a terminal-write grace beyond River's work timeout.
// A replacement worker may reclaim only after the prior worker has exceeded
// both its configured crawl budget and the time reserved to close the dossier.
func (w *siteDeepReadWorker) reclaimAfter() time.Duration {
	return w.Timeout(nil) + time.Minute
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
	if args.SiteReadID.IsZero() {
		return principal.WithCorrelationID(ctx, ids.NewV7())
	}
	return principal.WithCorrelationID(ctx, args.SiteReadID)
}

func (w *siteDeepReadWorker) run(ctx context.Context, args SiteDeepReadArgs) error {
	ctx = deepReadWorkerCtx(ctx, args)

	claim, err := w.people.BeginSiteRead(ctx, args.SiteReadID, w.reclaimAfter())
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

	if err := w.people.UpdateSiteReadProgress(ctx, args.SiteReadID, "crawling", nil); err != nil {
		w.log.WarnContext(ctx, "site read progress update failed", "read", args.SiteReadID.String(), "err", err)
	}
	// Crawl and extraction OVERLAP (crawlAndExtract): page calls launch
	// as pages commit, so the crawl's slow tail hides behind extraction.
	// The crawler owns the wall clock (caps.Wall); a seed page that
	// cannot be read at all is a failed read, not an empty one.
	progress, publishDraft := w.progressiveCallbacks(ctx, args.SiteReadID)
	crawl, extraction, err := crawlAndExtract(ctx, w.crawler, w.extract, claim.SeedURL, progress, publishDraft)
	if err != nil {
		if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, err); deferred {
			return deferErr
		}
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
	}
	if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, extraction.err); deferred {
		return deferErr
	}
	mergedFields, legalConflict, legalDrops := applyLegalGate(extraction.fields, extraction.merged.entities, pageKindsOf(crawl.Pages), extraction.legalCensusIncomplete)
	extraction.merged.entities = enrichLegalEntitiesFromProfile(extraction.merged.entities, mergedFields)
	w.extract.reportDrops(ctx, laneLegal, legalDrops)
	if legalConflict {
		w.log.WarnContext(ctx, legalWarningMultipleEntities,
			"read", args.SiteReadID.String(), "seed", claim.SeedURL)
	}
	factCount := len(mergedFields) + len(extraction.merged.facts)
	if extraction.err != nil && factCount == 0 && len(extraction.merged.people) == 0 && len(extraction.merged.entities) == 0 {
		// Every lane died before anything was evidenced: nothing honest
		// to report but the failure itself.
		return w.fail(ctx, args.SiteReadID, extraction.err)
	}

	readPages := crawl.Pages
	status := "done"
	if crawl.Stopped != nil {
		status = "partial"
	}
	if extraction.err != nil {
		// Part of the fan-out died with evidence already in hand: what
		// completed is the read, staged below like any other — a partial
		// that keeps what was honestly read, never a failure that discards
		// it. The terminal status makes returned-error retry churn
		// pointless, so the cause is logged instead.
		status = "partial"
		w.log.ErrorContext(ctx, "site deep read degraded to partial: extraction failed in part",
			"read", args.SiteReadID.String(), "err", extraction.err)
	}

	var proposalIDs []ids.UUID
	if claim.OrganizationID != nil {
		if isAutoEnrichRequest(args.RequestedBy) {
			// The auto-enrich lane applies the org's fields + facts directly
			// (fill-empty, human-precedence) instead of staging a confirm-first
			// proposal — the system chose to enrich this company, so there is no
			// human to confirm. Site people still stage as leads (strangers stay
			// staged, NEVER-8). Applied under the worker's PrincipalSystem ctx,
			// which ApplyDeepReadTx's auth.Require accepts.
			proposalIDs, err = w.autoApply(ctx, args, claim, mergedFields, extraction.merged.facts, extraction.merged.people)
		} else {
			proposalIDs, err = w.stageProposals(ctx, args.SiteReadID, claim, mergedFields, extraction.merged.facts, extraction.merged.people, len(readPages))
		}
		if err != nil {
			return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
		}
	}
	warnings := make([]string, 0, 2)
	if legalConflict {
		warnings = append(warnings, legalWarningMultipleEntities)
	}
	if extraction.err != nil {
		warnings = append(warnings, "Some pages could not be extracted; the grounded findings that completed are still available.")
	}
	draftFields := deepReadFields(mergedFields)
	draftPeople := siteReadPeople(extraction.merged.people)
	draftEntities := siteReadLegalEntities(extraction.merged.entities)
	proposalHash, err := siteReadProposalHash(draftFields, extraction.merged.facts, draftPeople, draftEntities)
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: hashing the draft: %w", args.SiteReadID, err))
	}
	// Zero surviving findings is an honest empty read — done, fact_count 0,
	// no proposal — not an error: the site simply evidenced nothing.
	return w.finish(ctx, args.SiteReadID, status, readPages, crawl, factCount, proposalIDs,
		draftFields, extraction.merged.facts, draftPeople, draftEntities,
		warnings, proposalHash)
}

func (w *siteDeepReadWorker) progressiveCallbacks(ctx context.Context, readID ids.UUID) (func(string, []crawlPage), func(pageFactsResult)) {
	progress := func(phase string, pages []crawlPage) {
		if err := w.people.UpdateSiteReadProgress(ctx, readID, phase, siteReadPages(pages)); err != nil {
			w.log.WarnContext(ctx, "site read progress update failed", "read", readID.String(), "err", err)
		}
	}
	publishDraft := func(partial pageFactsResult) {
		found := siteReadPeople(partial.people)
		entities := siteReadLegalEntities(partial.entities)
		hash, err := siteReadProposalHash(nil, partial.facts, found, entities)
		if err != nil {
			w.log.WarnContext(ctx, "site read progressive draft hash failed", "read", readID.String(), "err", err)
			return
		}
		if err := w.people.UpdateSiteReadDraft(ctx, readID, partial.facts, found, entities, hash); err != nil {
			w.log.WarnContext(ctx, "site read progressive draft update failed", "read", readID.String(), "err", err)
		}
	}
	return progress, publishDraft
}

// stageProposals stages everything the read evidenced: the ONE deepread
// bundle first (when any field or fact survived), then one thin
// site_lead per published person — the dossier's proposal_ids keep
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
	if claim.OrganizationID == nil {
		return ids.ApprovalID{}, errors.New("site deep read: an unbound onboarding draft cannot stage an organization approval")
	}
	fields := deepReadFields(mergedFields)
	proposedChange, err := json.Marshal(people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](*claim.OrganizationID),
		SourceURL:      claim.SeedURL,
		SiteReadID:     readID,
		Fields:         fields,
		Facts:          mergedFacts,
	})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	digest := sha256.Sum256(proposedChange)
	approvalID, err := w.approvals.Stage(ctx, approvals.StageInput{
		Kind:           deepReadProposalKind,
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     enrichTargetType,
		TargetID:       *claim.OrganizationID,
		Summary:        fmt.Sprintf("Deep site read of %s: %d fields, %d facts from %d pages", claim.SeedURL, len(mergedFields), len(mergedFacts), pagesRead),
		JoinPending:    true,
	})
	return approvalID, err
}

// stageSiteLead records ONE published person as a thin "site_lead"
// proposal: exactly what the site printed, nothing enriched. Each
// person is decided on their own — accepting the CTO does not accept the
// whole roster.
func (w *siteDeepReadWorker) stageSiteLead(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, person sitePerson) (ids.ApprovalID, error) {
	if claim.OrganizationID == nil {
		return ids.ApprovalID{}, errors.New("site deep read: an unbound onboarding draft cannot stage a lead proposal")
	}
	in, err := siteLeadStageInput(readID, *claim.OrganizationID, claim.SeedURL, person)
	if err != nil {
		return ids.ApprovalID{}, err
	}
	in.JoinPending = true
	approvalID, err := w.approvals.Stage(ctx, in)
	return approvalID, err
}

func siteLeadStageInput(readID, organizationID ids.UUID, seedURL string, person sitePerson) (approvals.StageInput, error) {
	proposedChange, err := json.Marshal(siteLeadProposal{
		OrganizationID:  organizationID,
		SiteReadID:      readID,
		Name:            person.Name,
		Role:            person.Role,
		PublishedEmail:  person.PublishedEmail,
		LinkedinURL:     person.LinkedinURL,
		EvidenceSnippet: person.EvidenceSnippet,
		SourceURL:       person.SourceURL,
	})
	if err != nil {
		return approvals.StageInput{}, err
	}
	digest := sha256.Sum256(proposedChange)
	return approvals.StageInput{
		Kind:           siteLeadProposalKind,
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     enrichTargetType,
		TargetID:       organizationID,
		Summary:        fmt.Sprintf("Lead from %s: %s — %s", seedURL, person.Name, person.Role),
	}, nil
}

// siteReadLegalEntities projects the gated census onto the dossier shape.
// The abstention refuses to APPLY a legal identity it cannot attribute; it
// never had a reason to forget the identities it read, and the confirm
// step turns them into the choice only a human can make.
func siteReadLegalEntities(entities []corpusLegalEntity) []people.SiteReadLegalEntity {
	out := make([]people.SiteReadLegalEntity, 0, len(entities))
	for _, e := range entities {
		out = append(out, people.SiteReadLegalEntity{
			Name:              e.Name,
			RegisteredAddress: e.RegisteredAddress,
			RegisterNumber:    e.RegisterNumber,
			EvidenceSnippet:   e.EvidenceSnippet,
			SourceURL:         e.SourceURL,
		})
	}
	return out
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

func (w *siteDeepReadWorker) finish(ctx context.Context, readID ids.UUID, status string, readPages []crawlPage, crawl siteCrawl, factCount int, proposalIDs []ids.UUID, fields []people.DeepReadField, facts []people.DeepReadFact, found []people.SiteReadPerson, entities []people.SiteReadLegalEntity, warnings []string, proposalHash string) error {
	in := people.FinishSiteReadInput{
		Status:        status,
		Pages:         make([]people.SiteReadPage, 0, len(readPages)),
		Skipped:       make([]people.SiteReadSkip, 0, len(crawl.Skipped)),
		FactCount:     factCount,
		ProposalIDs:   proposalIDs,
		ProfileFields: fields,
		Facts:         facts,
		People:        found,
		LegalEntities: entities,
		Warnings:      warnings,
		ProposalHash:  proposalHash,
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
