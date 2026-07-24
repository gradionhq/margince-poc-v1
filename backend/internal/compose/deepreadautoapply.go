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

	// Site people stage as leads regardless of the field/fact apply outcome —
	// the leads were evidenced independently of the org columns, and dropping
	// them on an apply failure would break the strangers-stay-staged invariant
	// (NEVER-8). Staged first so a later apply error cannot skip them.
	var proposalIDs []ids.UUID
	for _, person := range mergedPeople {
		approvalID, err := w.stageSiteLead(ctx, args.SiteReadID, claim, person)
		if err != nil {
			return nil, fmt.Errorf("staging the %s lead: %w", person.Name, err)
		}
		proposalIDs = append(proposalIDs, approvalID.UUID)
	}

	fields := deepReadFields(mergedFields)
	outcome := autoEnrichOutcomeEmpty
	var applyErr error
	if len(fields) > 0 || len(mergedFacts) > 0 {
		if err := w.people.ApplyDeepRead(ctx, people.DeepReadProposal{
			OrganizationID: orgID,
			SourceURL:      claim.SeedURL,
			SiteReadID:     args.SiteReadID,
			Fields:         fields,
			Facts:          mergedFacts,
		}); err != nil {
			applyErr, outcome = err, autoEnrichOutcomeFailed
		} else {
			outcome = autoEnrichOutcomeApplied
		}
	}
	if err := w.autoEnrich.MarkResolved(ctx, orgID, outcome); err != nil {
		// A missed terminal write at worst lets the next sweep reconsider the
		// org, which the dossier-exists gate then filters out (or, on a failed
		// apply, retries it) — never the read's success or failure.
		w.log.WarnContext(ctx, "auto-enrich cursor not recorded", "org", orgID.String(), "outcome", outcome, "err", err)
	}
	if applyErr != nil {
		// The people are staged; surface the apply failure so the read finishes
		// failed and the sweep retries the org.
		return proposalIDs, fmt.Errorf("auto-applying the deep read: %w", applyErr)
	}
	return proposalIDs, nil
}
