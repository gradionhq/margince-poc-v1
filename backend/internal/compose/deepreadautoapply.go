// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's auto-enrich lane (CAP-PARAM-7, ADR-0072/A118): a read the
// captured-organization sweep triggered applies its findings DIRECTLY instead
// of staging a confirm-first proposal — the system chose to enrich the company,
// so there is no human to confirm. The org fields + facts land through the same
// fill-empty + human-precedence machinery a human accept uses (so a human value
// is never overwritten and a re-run after a worker death is idempotent); site
// people still stage as leads (strangers stay staged, NEVER-8). The sweep cursor
// records the terminal outcome for observability, never gating the read.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// systemAutoEnrichActor is the requested-by sentinel the auto-enrich sweep
// stamps on the deep reads it triggers. A read carrying it takes the auto-apply
// lane below; every other read stages as before.
const systemAutoEnrichActor = "system:capture_auto_enrich"

// The terminal outcomes the auto-apply lane records on the sweep cursor
// (capture_auto_enrich_state.last_outcome — its CHECK carries the same set).
const (
	autoEnrichOutcomeApplied = "applied"
	autoEnrichOutcomeEmpty   = "empty"
	autoEnrichOutcomeFailed  = "failed"
)

// isAutoEnrichRequest reports whether a deep read was triggered by the
// auto-enrich sweep rather than a human.
func isAutoEnrichRequest(requestedBy string) bool { return requestedBy == systemAutoEnrichActor }

// autoApply is the auto-enrich lane's terminal step: apply the org fields +
// facts directly, stage site people as leads, and record the cursor outcome.
func (w *siteDeepReadWorker) autoApply(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, mergedFields []evidencedField, mergedFacts []people.DeepReadFact, mergedPeople []sitePerson) ([]ids.UUID, error) {
	orgID := ids.From[ids.OrganizationKind](*claim.OrganizationID)
	fields := deepReadFields(mergedFields)
	outcome := autoEnrichOutcomeEmpty
	if len(fields) > 0 || len(mergedFacts) > 0 {
		if err := w.people.ApplyDeepRead(ctx, people.DeepReadProposal{
			OrganizationID: orgID,
			SourceURL:      claim.SeedURL,
			SiteReadID:     args.SiteReadID,
			Fields:         fields,
			Facts:          mergedFacts,
		}); err != nil {
			if markErr := w.autoEnrich.MarkResolved(ctx, orgID, autoEnrichOutcomeFailed); markErr != nil {
				w.log.WarnContext(ctx, "auto-enrich cursor (failed) not recorded", "org", orgID.String(), "err", markErr)
			}
			return nil, fmt.Errorf("auto-applying the deep read: %w", err)
		}
		outcome = autoEnrichOutcomeApplied
	}
	var proposalIDs []ids.UUID
	for _, person := range mergedPeople {
		approvalID, err := w.stageSiteLead(ctx, args.SiteReadID, claim, person)
		if err != nil {
			return nil, fmt.Errorf("staging the %s lead: %w", person.Name, err)
		}
		proposalIDs = append(proposalIDs, approvalID.UUID)
	}
	if err := w.autoEnrich.MarkResolved(ctx, orgID, outcome); err != nil {
		// The findings already applied; a missed terminal write at worst lets
		// the next sweep reconsider a now-enriched org, which the dossier-exists
		// gate then filters out.
		w.log.WarnContext(ctx, "auto-enrich cursor not recorded", "org", orgID.String(), "outcome", outcome, "err", err)
	}
	return proposalIDs, nil
}
