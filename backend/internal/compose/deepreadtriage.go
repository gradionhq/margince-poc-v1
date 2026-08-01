// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The domain-triage lane of the deep-read worker: decide whether a mail domain
// deserves an organization, and create one only if it does.
//
// It is the same worker, the same crawler and the same extraction spine as an
// organization read — what differs is that it starts with no organization and
// may finish without creating one. Two things happen here that happen nowhere
// else: the seed page is classified BEFORE the crawl, so a personal or parked
// domain costs one page instead of twelve; and when no site can be read at all,
// the sender-name heuristic gets the last word before a company is created by
// default.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// systemDomainTriageActor is the requested-by sentinel a triage read carries.
// The dossier row is the authority on which lane a claimed read belongs to, so
// this is what the worker branches on.
const systemDomainTriageActor = "system:domain_triage"

// isDomainTriageRequest reports whether a read is the domain-triage lane.
func isDomainTriageRequest(requestedBy string) bool { return requestedBy == systemDomainTriageActor }

// isSystemRead covers both automatic lanes. Neither was asked for by a human,
// so both run under the narrow page ceiling.
func isSystemRead(requestedBy string) bool {
	return isAutoEnrichRequest(requestedBy) || isDomainTriageRequest(requestedBy)
}

// runTriage answers one domain's organization question.
//
// The order is the cost order: the cheapest answer that can be trusted wins,
// and only a domain that survives every cheap refusal pays for a full crawl.
func (w *siteDeepReadWorker) runTriage(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim) error {
	domain := triageDomainOf(claim.SeedURL)
	if domain == "" {
		return w.fail(ctx, args.SiteReadID,
			fmt.Errorf("site deep read %s: %q is not a triageable seed", args.SiteReadID, claim.SeedURL))
	}

	seed, err := w.crawler.ReadSeed(ctx, claim.SeedURL)
	if err != nil {
		// Nothing to read. The domain may still be a real company with a broken
		// site, so the decision falls to the sender's own name.
		return w.resolveUnreachable(ctx, args, domain, err)
	}

	verdict, err := w.classifySeed(ctx, seed)
	if err != nil {
		if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, err); deferred {
			return deferErr
		}
		// The classifier is an optimization, not the authority. Losing it costs
		// the early exit, never the read.
		w.log.WarnContext(ctx, "domain triage: the seed classification failed; reading the whole site",
			"read", args.SiteReadID.String(), "domain", domain, "err", err)
		verdict = siteTriageVerdict{Kind: siteKindUnclear}
	}
	if verdict.Aborts() {
		// The whole point of classifying first: one page read, no crawl, no
		// extraction, no company invented.
		return w.settleTriage(ctx, args, domain, triageStatusFor(verdict.Kind),
			people.DomainSourceSiteRead, triageEvidence(verdict), siteReadStatusCancelled, nil)
	}

	return w.readAndResolveTriage(ctx, args, claim, domain)
}

// triageWithoutLooking answers a domain when this worker may not read its site
// at all — the operator turned automatic enrichment off, or the role has no
// model path. The question still gets closed, from what the workspace already
// knows, which is exactly the behaviour that predated triage: a company unless
// the sender's own name explains the domain.
func (w *siteDeepReadWorker) triageWithoutLooking(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim) error {
	domain := triageDomainOf(claim.SeedURL)
	if domain == "" {
		return w.fail(ctx, args.SiteReadID,
			fmt.Errorf("site deep read %s: %q is not a triageable seed", args.SiteReadID, claim.SeedURL))
	}
	return w.resolveUnreachable(ctx, args, domain, errNoSiteReadAllowed)
}

// errNoSiteReadAllowed marks the not-permitted-to-look case, distinct from a
// site that was tried and could not be reached.
var errNoSiteReadAllowed = errors.New("this worker may not read sites")

// readAndResolveTriage runs the full read for a domain the seed page did not
// rule out, and decides on what it actually found.
func (w *siteDeepReadWorker) readAndResolveTriage(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, domain string) error {
	if err := w.people.UpdateSiteReadProgress(ctx, args.SiteReadID, "crawling", nil); err != nil {
		w.log.WarnContext(ctx, "site read progress update failed", "read", args.SiteReadID.String(), "err", err)
	}
	progress, publishDraft := w.progressiveCallbacks(ctx, args.SiteReadID)
	crawler := w.crawler.withPageCeiling(w.pageCeiling(claim.RequestedBy, args.MaxPages))
	crawl, extraction, err := crawlAndExtract(ctx, crawler, w.extract, claim.SeedURL, progress, publishDraft)
	if err != nil {
		if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, err); deferred {
			return deferErr
		}
		return w.resolveUnreachable(ctx, args, domain, err)
	}
	if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, extraction.err); deferred {
		return deferErr
	}

	fields, _, legalDrops := applyLegalGate(extraction.fields, extraction.merged.entities, pageKindsOf(crawl.Pages), extraction.legalCensusIncomplete)
	extraction.merged.entities = enrichLegalEntitiesFromProfile(extraction.merged.entities, fields)
	w.extract.reportDrops(ctx, laneLegal, legalDrops)

	stated := statedCompanyName(fields, extraction.merged.entities)
	if stated == "" && len(extraction.merged.entities) == 0 {
		// The site read, and said nothing that identifies a company. That is
		// not evidence of a company; fall back to the name test.
		return w.resolveUnreachable(ctx, args, domain, nil)
	}

	status := siteReadWireStatusDone
	if crawl.Stopped != nil || extraction.err != nil {
		status = siteReadWireStatusPartial
	}
	return w.settleTriage(ctx, args, domain, people.DomainCompany, people.DomainSourceSiteRead,
		triageCompanyEvidence(stated, len(extraction.merged.entities)), status,
		&triagePayload{
			DossierName: stated,
			SeedURL:     claim.SeedURL,
			Fields:      deepReadFields(fields),
			Facts:       extraction.merged.facts,
			People:      extraction.merged.people,
			Crawl:       crawl,
		})
}

// resolveUnreachable decides a domain whose site could not be read, or which
// read and identified nobody. The name test itself runs inside the store's
// verdict transaction, against the very people a company answer would employ.
func (w *siteDeepReadWorker) resolveUnreachable(ctx context.Context, args SiteDeepReadArgs, domain string, cause error) error {
	evidence := "no site could be read, and the verdict fell to the sender's own name"
	if cause == nil {
		evidence = "the site named no company, and the verdict fell to the sender's own name"
	}
	res, err := w.people.ResolveUnreadableDomainTriage(ctx, people.ResolveDomainTriageInput{
		Domain: domain, Evidence: evidence, ReadID: args.SiteReadID,
		SeedURL: people.TriageSeedURL(domain),
	})
	if err != nil {
		return w.fail(ctx, args.SiteReadID,
			fmt.Errorf("site deep read %s: settling the unreadable domain %s: %w", args.SiteReadID, domain, err))
	}
	w.log.InfoContext(ctx, "domain triage settled without a site", "domain", domain,
		"organization_created", res.OrgCreated, "employment_edges", res.EdgesPlanted, "cause", cause)
	return w.finishTriageRead(ctx, args, siteReadStatusFailed, nil)
}

// triagePayload is what a company verdict has to hand to the resolve: the name
// to use, the findings to apply, and the crawl to report.
type triagePayload struct {
	DossierName string
	SeedURL     string
	Fields      []people.DeepReadField
	Facts       []people.DeepReadFact
	People      []sitePerson
	Crawl       siteCrawl
}

// settleTriage writes the verdict, then finishes the dossier. The verdict comes
// first because it is what the ensure ladder reads: a dossier marked done
// beside an unanswered question would leave every later message from the domain
// re-asking it.
func (w *siteDeepReadWorker) settleTriage(ctx context.Context, args SiteDeepReadArgs, domain, status, source, evidence, readStatus string, payload *triagePayload) error {
	in := people.ResolveDomainTriageInput{
		Domain: domain, Status: status, Source: source, Evidence: evidence, ReadID: args.SiteReadID,
	}
	if payload != nil {
		in.DossierName, in.SeedURL, in.Fields, in.Facts = payload.DossierName, payload.SeedURL, payload.Fields, payload.Facts
	}
	res, err := w.people.ResolveDomainTriage(ctx, in)
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: settling the verdict for %s: %w", args.SiteReadID, domain, err))
	}
	w.log.InfoContext(ctx, "domain triage settled", "domain", domain, "verdict", status,
		"source", source, "organization_created", res.OrgCreated, "employment_edges", res.EdgesPlanted)

	if payload == nil || res.OrganizationID == nil {
		// Nothing was created, so there is nothing to stage people onto and no
		// dossier to report against a company.
		return w.finishTriageRead(ctx, args, readStatus, payload)
	}
	// Site people stage as leads onto the organization the verdict just made —
	// strangers stay staged (NEVER-8), exactly as on the auto-enrich lane.
	claim := people.SiteReadClaim{OrganizationID: &res.OrganizationID.UUID, SeedURL: payload.SeedURL}
	for _, person := range payload.People {
		if _, _, err := w.stageSiteLead(ctx, args.SiteReadID, claim, person); err != nil {
			w.log.WarnContext(ctx, "domain triage: staging a site person failed",
				"read", args.SiteReadID.String(), "err", err)
		}
	}
	return w.finishTriageRead(ctx, args, readStatus, payload)
}

// finishTriageRead records the dossier's terminal state. An aborted read is
// 'cancelled' with a status code naming why: nothing went wrong with it, the
// site simply answered the question before the crawl was worth running, and a
// failure is something to investigate while this is not.
func (w *siteDeepReadWorker) finishTriageRead(ctx context.Context, args SiteDeepReadArgs, status string, payload *triagePayload) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	in := people.FinishSiteReadInput{Status: status}
	if status == siteReadStatusCancelled {
		in.Warnings = []string{triageWarningNotACompany}
	}
	if payload != nil {
		in.Pages = siteReadPages(payload.Crawl.Pages)
		in.FactCount = len(payload.Fields) + len(payload.Facts)
		in.ProfileFields = payload.Fields
		in.Facts = payload.Facts
		in.People = siteReadPeople(payload.People)
		for _, s := range payload.Crawl.Skipped {
			in.Skipped = append(in.Skipped, people.SiteReadSkip{URL: s.URL, Reason: string(s.Reason)})
		}
	}
	if err := w.people.FinishSiteRead(tctx, args.SiteReadID, in); err != nil {
		return fmt.Errorf("site deep read %s: recording the triage outcome: %w", args.SiteReadID, err)
	}
	return nil
}

// The terminal statuses a triage read reports, and the warning a stopped one
// carries. 'cancelled' is right for an aborted read: nothing went wrong with
// it, the site answered the question before the crawl was worth running, and a
// failure is something to investigate while this is not.
const (
	siteReadStatusCancelled  = "cancelled"
	siteReadStatusFailed     = "failed"
	triageWarningNotACompany = "This site says the domain does not belong to a company, so the read stopped after its landing page."
)

// classifySeed runs the one classification call over the landing page.
func (w *siteDeepReadWorker) classifySeed(ctx context.Context, seed crawlPage) (siteTriageVerdict, error) {
	if strings.TrimSpace(seed.Text) == "" {
		// A page with no readable text identifies nobody. That IS the parked
		// answer, and it costs no model call to say so.
		return siteTriageVerdict{
			Kind: siteKindParked, Confidence: 1,
			Reason: "the landing page carries no readable text",
		}, nil
	}
	req := triageRequest(seed)
	var resp model.Response
	var err error
	if structured, ok := w.triageBrain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, triageShapeValid)
	} else {
		resp, err = w.triageBrain.Complete(ctx, req)
	}
	if err != nil {
		return siteTriageVerdict{}, err
	}
	return gateTriageVerdict(resp.Text), nil
}

// statedCompanyName is the name the site itself gave, preferring a legal notice
// (a registered entity is the strongest identity a site can print) over the
// profile lane's display name. Empty when the site named nobody, which is what
// distinguishes "read a company's site" from "read a site".
func statedCompanyName(fields []evidencedField, entities []corpusLegalEntity) string {
	for _, e := range entities {
		if name := strings.TrimSpace(e.Name); name != "" {
			return name
		}
	}
	for _, f := range fields {
		if f.Field == string(crmcontracts.ColdStartFieldFieldDisplayName) {
			if name := strings.TrimSpace(f.Value); name != "" {
				return name
			}
		}
	}
	return ""
}

// triageStatusFor maps a classifier answer onto the ledger's vocabulary. Parked
// is recorded as no_site: nothing identified anybody, which is what that
// verdict means, and inventing a fourth word for the same fact would only
// give operators two things to learn.
func triageStatusFor(kind string) string {
	switch kind {
	case siteKindPersonal:
		return people.DomainPersonal
	case siteKindProvider:
		return people.DomainProvider
	default:
		return people.DomainNoSite
	}
}

// triageEvidence is the one sentence the ledger carries for a classifier
// verdict — what a human reads when they ask why a company was refused.
func triageEvidence(v siteTriageVerdict) string {
	if v.Reason == "" {
		return fmt.Sprintf("the site classifies as %s", v.Kind)
	}
	return fmt.Sprintf("the site classifies as %s: %s", v.Kind, v.Reason)
}

// triageCompanyEvidence says what made the company answer stick.
func triageCompanyEvidence(stated string, entities int) string {
	if entities > 0 {
		return fmt.Sprintf("the site's legal notice names %q", stated)
	}
	return fmt.Sprintf("the site states the company name %q", stated)
}

// triageDomainOf recovers the registrable domain a triage read is about from
// its seed url. The seed was derived from the domain, so this is the inverse of
// people.TriageSeedURL and not a parse of anything a human typed.
func triageDomainOf(seedURL string) string {
	return freemail.Registrable(strings.TrimPrefix(seedURL, people.TriageSeedScheme))
}
