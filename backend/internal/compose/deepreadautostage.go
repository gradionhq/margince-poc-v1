// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's auto-enrich lane (CAP-PARAM-7, ADR-0072/A118): a read the
// captured-organization sweep triggered STAGES what it found, exactly like a
// human-requested read does.
//
// A company's own website is text an outsider controls, and it reaches the
// model that writes these findings. So the findings carry no more authority
// than any other model output: the org fields + facts stage as the ONE
// confirm-first "deepread" proposal, and site people stage as site_leads
// (strangers stay staged, NEVER-8). The sweep chooses WHICH company to read;
// a human still decides what the read is allowed to write.
//
// One direct write remains on this lane: ApplySitePersonFields fills the empty
// columns of a person the workspace ALREADY records at this company, on an
// unmistakable match only, and never overwrites a value (the safety argument
// is spelled out in people/sitepersonfields.go).
//
// The sweep cursor records the terminal outcome for observability, never
// gating the read.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// systemAutoEnrichActor is the requested-by sentinel the auto-enrich sweep
// stamps on the deep reads it triggers. A read carrying it takes the lane
// below; every other read goes straight to stageProposals.
const systemAutoEnrichActor = "system:capture_auto_enrich"

// isAutoEnrichRequest reports whether a deep read was triggered by the
// auto-enrich sweep rather than a human.
func isAutoEnrichRequest(requestedBy string) bool { return requestedBy == systemAutoEnrichActor }

// autoStage is the auto-enrich lane's terminal step: fill the people the
// workspace can already identify, stage everything else the read evidenced,
// and record the cursor outcome.
func (w *siteDeepReadWorker) autoStage(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, mergedFields []evidencedField, mergedFacts []people.DeepReadFact, mergedPeople []sitePerson, pagesRead int) ([]ids.UUID, error) {
	orgID := ids.From[ids.OrganizationKind](*claim.OrganizationID)
	strangers := w.fillKnownSitePeople(ctx, orgID, mergedPeople)
	proposalIDs, stageErr := w.stageProposals(ctx, args.SiteReadID, claim, mergedFields, mergedFacts, strangers, pagesRead)

	outcome := capture.AutoEnrichOutcomeStaged
	switch {
	case stageErr != nil:
		outcome = capture.AutoEnrichOutcomeFailed
	case len(proposalIDs) == 0:
		// The site evidenced nothing that needs a decision — an honest empty
		// read, terminal like a staged one.
		outcome = capture.AutoEnrichOutcomeEmpty
	}
	if err := w.autoEnrich.MarkResolved(ctx, orgID, outcome); err != nil {
		// A missed terminal write at worst lets the next sweep reconsider the
		// org, which the dossier-exists gate then filters out (or, on a failed
		// stage, retries it) — never the read's success or failure.
		w.log.WarnContext(ctx, "auto-enrich cursor not recorded", "org", orgID.String(), "outcome", outcome, "err", err)
	}
	return proposalIDs, stageErr
}

// fillKnownSitePeople fills the empty fields of every published person the
// workspace already records at this company, and returns the ones it did not
// match — the strangers, who stage as leads like everyone else.
//
// The match is deliberately narrow (exact email, or exactly one confident name
// among the org's own employees). Zero matches and every ambiguity fall
// through to staging.
func (w *siteDeepReadWorker) fillKnownSitePeople(ctx context.Context, orgID ids.OrganizationID, found []sitePerson) []sitePerson {
	strangers := make([]sitePerson, 0, len(found))
	for _, person := range found {
		matched, err := w.people.ApplySitePersonFields(ctx, orgID, people.SitePersonFields{
			Name:            person.Name,
			Role:            person.Role,
			PublishedEmail:  person.PublishedEmail,
			LinkedinURL:     person.LinkedinURL,
			EvidenceSnippet: person.EvidenceSnippet,
			SourceURL:       person.SourceURL,
		})
		if err != nil {
			// A fill that failed must not cost the lead: fall through to
			// staging so the person still reaches a human, and say why in the
			// log.
			w.log.WarnContext(ctx, "auto-enrich: filling a matched site person failed",
				"org", orgID.String(), "person", person.Name, "err", err)
		}
		if matched {
			continue
		}
		strangers = append(strangers, person)
	}
	return strangers
}
