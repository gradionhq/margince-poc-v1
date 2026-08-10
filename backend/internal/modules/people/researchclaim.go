// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What a human accepted from a research run (ADR-0096 D4).
//
// This is the ONLY write in the whole research surface. A run stages and
// touches nothing; the record changes here, once somebody has read the claims
// and chosen which of them are true.
//
// It lands in person_profile_field, the same table the signature-enrichment
// pass writes: a fact about who this person IS, with the evidence it was read
// from. That table's shape is what makes the guarantee enforceable rather than
// promised — evidence_snippet and source_ref are both NOT NULL, so a claim
// that lost its quote or its source cannot be stored at all.
//
// captured_by names the HUMAN who accepted it, not the provider that proposed
// it. The provider's URL rides source_ref, so the chain from record to document
// stays intact; but the decision was a person's, and the record says so.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// researchSource marks these rows as a human's acceptance of a provider's
// claim, distinguishing them from the signature pass that shares the table.
const researchSource = "person_research"

// ResearchClaimInput is one accepted claim.
type ResearchClaimInput struct {
	// Field is which profile field this claim fills. The table constrains the
	// set, so a claim naming anything else is refused by the database rather
	// than silently stored under a name no reader looks for.
	Field string
	Value string
	// Quote and SourceURL are the evidence. Both are required: a fact a reader
	// cannot trace to a document is what the review step exists to stop.
	Quote     string
	SourceURL string
}

// SaveResearchClaims writes the accepted claims and returns how many landed.
//
// The whole set commits in ONE transaction. A partial save would leave the
// reader with no way to tell which half of their decision took effect, and
// re-running the drawer would then re-offer claims they had already accepted.
func (s *Store) SaveResearchClaims(ctx context.Context, personID ids.PersonID, claims []ResearchClaimInput) (int, error) {
	// Malformed input is named before authority is asked about: a caller who
	// sent a claim with no evidence made a mistake the server can describe.
	for i, claim := range claims {
		if claim.Value == "" || claim.Quote == "" || claim.SourceURL == "" {
			return 0, httperr.Validation(fmt.Sprintf("claims[%d]", i), "required",
				"a saved research claim carries its value, the words it was read from, and the document it came from — one that lost any of the three cannot be checked, and is refused rather than stored")
		}
	}
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return 0, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return 0, err
	}

	saved := 0
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleLive(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		for _, claim := range claims {
			// ON CONFLICT because the table holds one row per (person, field):
			// accepting a newer claim about the same field REPLACES what was
			// there, which is what a reader who just chose it expects.
			if _, err := tx.Exec(ctx, `
				INSERT INTO person_profile_field
					(workspace_id, person_id, field, value, evidence_snippet, source_ref, source, captured_by)
				VALUES ($1, $2, $3, $4, $5, $6, $8, $7)
				ON CONFLICT (person_id, field) DO UPDATE
				SET value = EXCLUDED.value,
				    evidence_snippet = EXCLUDED.evidence_snippet,
				    source_ref = EXCLUDED.source_ref,
				    source = EXCLUDED.source,
				    captured_by = EXCLUDED.captured_by`,
				storekit.MustWorkspace(ctx), personID, claim.Field, claim.Value,
				claim.Quote, claim.SourceURL, by, researchSource); err != nil {
				return fmt.Errorf("save the research claim %q: %w", claim.Field, err)
			}
			saved++
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "person", personID.UUID, nil,
			map[string]any{"research_claims_saved": saved})
		if err != nil {
			return fmt.Errorf("audit the saved research: %w", err)
		}
		// person.updated is the open envelope its own doc names for exactly
		// this: a signature-enrichment fill is the same shape as a research
		// fill, and both are a change-set rather than a fixed field list.
		return storekit.EmitEvent(ctx, tx, auditID, personID.UUID,
			crmcontracts.PublicEventPersonUpdated{
				ChangedFields: map[string]any{
					"profile_fields": map[string]any{"saved": saved, auditKeySource: researchSource},
				},
			})
	})
	if err != nil {
		return 0, err
	}
	return saved, nil
}
