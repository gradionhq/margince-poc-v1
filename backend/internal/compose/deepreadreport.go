// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a finished deep read becomes: the legal-census gate over what the model
// lanes evidenced, the findings that land (auto-enrich) or stage (confirm-first),
// and the one terminal dossier write the SPA reads.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// reportRead records the outcome of a crawl that succeeded. A read whose lanes
// all died before evidencing anything has nothing honest to report but the
// failure; everything else — down to zero surviving findings — is a read.
func (w *siteDeepReadWorker) reportRead(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, crawl siteCrawl, extraction siteExtraction) error {
	if deferred, deferErr := w.deferForBudget(ctx, args.SiteReadID, extraction.err); deferred {
		return deferErr
	}
	mergedFields, legalConflict := w.gateLegalCensus(ctx, args, claim, crawl, &extraction)
	factCount := len(mergedFields) + len(extraction.merged.facts)
	if extraction.err != nil && factCount == 0 && len(extraction.merged.people) == 0 && len(extraction.merged.entities) == 0 {
		// Every lane died before anything was evidenced: nothing honest
		// to report but the failure itself.
		return w.fail(ctx, args.SiteReadID, extraction.err)
	}

	readPages := crawl.Pages
	status := w.dossierStatus(ctx, args.SiteReadID, crawl, extraction.err)

	proposalIDs, err := w.landFindings(ctx, args, claim, mergedFields, extraction.merged, len(readPages))
	if err != nil {
		return w.fail(ctx, args.SiteReadID, fmt.Errorf("site deep read %s: %w", args.SiteReadID, err))
	}
	warnings := readWarnings(legalConflict, extraction.err)
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

// gateLegalCensus runs the no-guess legal gate over the extraction: it answers
// the profile fields that survived, folds those fields back into the census the
// legal pages voted on, and reports what the gate dropped on the legal lane. A
// multi-entity conflict is both logged and carried to the dossier as a warning,
// because the read cannot decide which entity the company is.
func (w *siteDeepReadWorker) gateLegalCensus(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, crawl siteCrawl, extraction *siteExtraction) ([]evidencedField, bool) {
	mergedFields, legalConflict, legalDrops := applyLegalGate(extraction.fields, extraction.merged.entities, pageKindsOf(crawl.Pages), extraction.legalCensusIncomplete)
	extraction.merged.entities = enrichLegalEntitiesFromProfile(extraction.merged.entities, mergedFields)
	w.extract.reportDrops(ctx, laneLegal, legalDrops)
	if legalConflict {
		w.log.WarnContext(ctx, legalWarningMultipleEntities,
			"read", args.SiteReadID.String(), "seed", claim.SeedURL)
	}
	return mergedFields, legalConflict
}

// dossierStatus is the terminal status of a read that evidenced something: a
// crawl that stopped short of the site, or a fan-out that died in part, is
// honestly partial rather than done.
func (w *siteDeepReadWorker) dossierStatus(ctx context.Context, readID ids.UUID, crawl siteCrawl, extractErr error) string {
	status := "done"
	if crawl.Stopped != nil {
		status = "partial"
	}
	if extractErr != nil {
		// Part of the fan-out died with evidence already in hand: what
		// completed is the read, staged like any other — a partial that keeps
		// what was honestly read, never a failure that discards it. The
		// terminal status makes returned-error retry churn pointless, so the
		// cause is logged instead.
		status = "partial"
		w.log.ErrorContext(ctx, "site deep read degraded to partial: extraction failed in part",
			"read", readID.String(), "err", extractErr)
	}
	return status
}

// landFindings puts the gated findings where the requesting lane says they
// belong, and answers the proposal ids the dossier records. An onboarding draft
// still unbound to an organization has nothing to land them against.
func (w *siteDeepReadWorker) landFindings(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, mergedFields []evidencedField, merged pageFactsResult, pagesRead int) ([]ids.UUID, error) {
	if claim.OrganizationID == nil {
		return nil, nil
	}
	if isAutoEnrichRequest(claim.RequestedBy) {
		// The auto-enrich lane applies the org's fields + facts directly
		// (fill-empty, human-precedence) instead of staging a confirm-first
		// proposal — the system chose to enrich this company, so there is no
		// human to confirm. Site people still stage as leads (strangers stay
		// staged, NEVER-8). Applied under the worker's PrincipalSystem ctx,
		// which ApplyDeepReadTx's auth.Require accepts.
		return w.autoApply(ctx, args, claim, mergedFields, merged.facts, merged.people)
	}
	return w.stageProposals(ctx, args.SiteReadID, claim, mergedFields, merged.facts, merged.people, pagesRead)
}

// readWarnings are the caveats the dossier shows a human: what this read could
// not settle, so a confirmation is never made on an unstated assumption.
func readWarnings(legalConflict bool, extractErr error) []string {
	warnings := make([]string, 0, 2)
	if legalConflict {
		warnings = append(warnings, legalWarningMultipleEntities)
	}
	if extractErr != nil {
		warnings = append(warnings, "Some pages could not be extracted; the grounded findings that completed are still available.")
	}
	return warnings
}
