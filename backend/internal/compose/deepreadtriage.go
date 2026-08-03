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
		// Nothing to read — a genuine failure. The domain may still be a real
		// company with a broken site, so the decision falls to the sender's name.
		w.log.WarnContext(ctx, "domain triage: the seed page could not be read",
			"read", args.SiteReadID.String(), "domain", domain, "err", err)
		return w.resolveUnreachable(ctx, args, domain, siteReadWireStatusFailed, triageWarningNothingRead)
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
			people.DomainSourceSiteRead, triageEvidence(verdict), siteReadWireStatusCancelled, triageWarningNotACompany, nil)
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
	return w.resolveUnreachable(ctx, args, domain, siteReadWireStatusCancelled, triageWarningNotAllowed)
}

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
		w.log.WarnContext(ctx, "domain triage: the crawl failed",
			"read", args.SiteReadID.String(), "domain", domain, "err", err)
		return w.resolveUnreachable(ctx, args, domain, siteReadWireStatusFailed, triageWarningNothingRead)
	}
	if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, extraction.err); deferred {
		return deferErr
	}

	// The seed was a guess derived from the mail domain; the crawl's fallback
	// ladder may have reached the site on another host or scheme. Everything
	// this read cites — the dossier's source url, the staged leads' — must name
	// the url that ANSWERED, or a human confirming one would confirm a dead
	// link. The sibling lane adopts it for the same reason.
	if crawl.SeedURL != "" {
		claim.SeedURL = crawl.SeedURL
	}
	fields, _, legalDrops := applyLegalGate(extraction.fields, extraction.merged.entities, pageKindsOf(crawl.Pages), extraction.legalCensusIncomplete)
	extraction.merged.entities = enrichLegalEntitiesFromProfile(extraction.merged.entities, fields)
	w.extract.reportDrops(ctx, laneLegal, legalDrops)

	stated := statedCompanyName(fields, extraction.merged.entities)
	if stated == "" && len(extraction.merged.entities) == 0 {
		// The site read fine and identified nobody. Nothing went wrong, so this
		// is not a failure — it is an answer the crawl could not supply.
		return w.resolveUnreachable(ctx, args, domain, siteReadWireStatusCancelled, triageWarningNoCompany)
	}

	status := siteReadWireStatusDone
	if crawl.Stopped != nil || extraction.err != nil {
		status = siteReadWireStatusPartial
	}
	return w.settleTriage(ctx, args, domain, people.DomainCompany, people.DomainSourceSiteRead,
		triageCompanyEvidence(stated, len(extraction.merged.entities)), status, "",
		&triagePayload{
			DossierName: stated,
			SeedURL:     claim.SeedURL,
			Fields:      deepReadFields(fields),
			Facts:       extraction.merged.facts,
			People:      extraction.merged.people,
			Entities:    siteReadLegalEntities(extraction.merged.entities),
			Crawl:       crawl,
		})
}

// resolveUnreachable settles a domain the site could not answer for. The name
// test itself runs inside the store's verdict transaction, against the very
// people a company answer would employ.
//
// Only ONE of its three callers is a failure. A crawl that ran and named nobody
// worked exactly as designed, and a worker forbidden to look was obeying an
// operator — recording either as `failed` would send somebody investigating a
// read that did what it was told. Each caller therefore names its own terminal
// status and the sentence that goes on the dossier.
func (w *siteDeepReadWorker) resolveUnreachable(ctx context.Context, args SiteDeepReadArgs, domain, status, warning string) error {
	res, err := w.people.ResolveUnreadableDomainTriage(ctx, people.ResolveDomainTriageInput{
		Domain: domain, Evidence: warning, ReadID: args.SiteReadID,
		SeedURL: people.TriageSeedURL(domain),
	})
	if err != nil {
		return w.fail(ctx, args.SiteReadID,
			fmt.Errorf("site deep read %s: settling the unreadable domain %s: %w", args.SiteReadID, domain, err))
	}
	w.log.InfoContext(ctx, "domain triage settled without a site", "domain", domain,
		"organization_created", res.OrgCreated, "employment_edges", res.EdgesPlanted, "why", warning)
	return w.finishTriageRead(ctx, args, status, warning, nil)
}

// triagePayload is what a company verdict has to hand to the resolve: the name
// to use, the findings to apply, and the crawl to report.
type triagePayload struct {
	DossierName string
	SeedURL     string
	Fields      []people.DeepReadField
	Facts       []people.DeepReadFact
	People      []sitePerson
	// Entities is the legal census the read gathered. It has to travel to the
	// terminal write or the finish overwrites it with an empty list, and the
	// dossier loses the very entity the organization was named after.
	Entities []people.SiteReadLegalEntity
	Crawl    siteCrawl
}

// settleTriage writes the verdict, then finishes the dossier. The verdict comes
// first because it is what the ensure ladder reads: a dossier marked done
// beside an unanswered question would leave every later message from the domain
// re-asking it.
func (w *siteDeepReadWorker) settleTriage(ctx context.Context, args SiteDeepReadArgs, domain, status, source, evidence, readStatus, warning string, payload *triagePayload) error {
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
		return w.finishTriageRead(ctx, args, readStatus, warning, payload)
	}
	// Site people stage as leads onto the organization the verdict just made —
	// strangers stay staged (NEVER-8), exactly as on the auto-enrich lane.
	claim := people.SiteReadClaim{OrganizationID: &res.OrganizationID.UUID, SeedURL: payload.SeedURL}
	// The logo, on the same terms as every other company (A55): a 🟢 display
	// asset read off the seed page's own markup. Nothing else would ever give
	// these organizations one — the auto-enrich sweep only offers rows with
	// name_source='domain' and no finished read, and a triage company has
	// neither — so skipping it here means faceless forever.
	w.resolveLogo(ctx, args, claim, payload.Crawl)
	for _, person := range payload.People {
		if _, _, err := w.stageSiteLead(ctx, args.SiteReadID, claim, person); err != nil {
			w.log.WarnContext(ctx, "domain triage: staging a site person failed",
				"read", args.SiteReadID.String(), "err", err)
		}
	}
	return w.finishTriageRead(ctx, args, readStatus, warning, payload)
}

// finishTriageRead records the dossier's terminal state and the sentence that
// explains it, so a human reading the row learns why it ended without going to
// the logs.
func (w *siteDeepReadWorker) finishTriageRead(ctx context.Context, args SiteDeepReadArgs, status, warning string, payload *triagePayload) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	in := people.FinishSiteReadInput{Status: status}
	if warning != "" {
		in.Warnings = []string{warning}
	}
	if payload != nil {
		in.Pages = siteReadPages(payload.Crawl.Pages)
		in.FactCount = len(payload.Fields) + len(payload.Facts)
		in.ProfileFields = payload.Fields
		in.Facts = payload.Facts
		in.People = siteReadPeople(payload.People)
		in.LegalEntities = payload.Entities
		// Why the crawl stopped early, or a `partial` dossier says it was
		// truncated without saying by what — and a page cap reads the same as
		// a deadline to whoever has to judge the read.
		if payload.Crawl.Stopped != nil {
			reason := string(*payload.Crawl.Stopped)
			in.StoppedReason = &reason
		}
		for _, s := range payload.Crawl.Skipped {
			in.Skipped = append(in.Skipped, people.SiteReadSkip{URL: s.URL, Reason: string(s.Reason)})
		}
	}
	if err := w.people.FinishSiteRead(tctx, args.SiteReadID, in); err != nil {
		return fmt.Errorf("site deep read %s: recording the triage outcome: %w", args.SiteReadID, err)
	}
	return nil
}

// The warnings a stopped triage read carries, so the dossier itself says why it
// ended rather than leaving the reason in a log line.
const (
	triageWarningNotACompany = "This site says the domain does not belong to a company, so the read stopped after its landing page."
	triageWarningNothingRead = "No site could be read for this domain, so the company question was answered from what the workspace already knew."
	triageWarningNoCompany   = "The site was read and named no company, so the question was answered from what the workspace already knew."
	triageWarningNotAllowed  = "Automatic enrichment is off, so the company question was answered without reading the site."
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
	// EXACTLY one entity, or none of them. A group publishing several is the
	// case applyLegalGate already abstains on, and picking whichever happens to
	// be first in the extraction result would name the company after array
	// order. With the census ambiguous the profile lane's own display name is
	// the honest fallback.
	if len(entities) == 1 {
		if name := strings.TrimSpace(entities[0].Name); name != "" {
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
