// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The rename re-check (PO-F-2 applied to an EXISTING row).
//
// A create asks "does this company already exist?" once, with whatever it
// knows at that moment — and for a captured organization that is a name
// derived from its mail domain. The real name arrives later: a corroborated
// signature promotes it, a site dossier fills its legal name. That is the
// moment a duplicate becomes visible, and until now nothing looked. One
// workspace held a company minted as "Speedkit" from speedkit.com, renamed to
// "Baqend GmbH" by the signature sweep while "Baqend" already sat on
// baqend.com — a perfect name match nobody was watching for.
//
// So every non-human write of display_name or legal_name re-runs the fuzzy
// tier and files what it finds. It never merges (DEDUPE_FUZZY_AUTOMERGE is
// pinned never) and it never blocks the rename: the rename is right, the
// duplicate is a separate question, and the human answers it in the queue.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// orgRenameRecheckSource names this detector on the rows it files. The queue's
// `source` column says which detector found a pair, and a rename-found pair is
// worth telling apart from one found at create time — they answer different
// questions about how the duplicate got in.
const orgRenameRecheckSource = "org_rename_recheck"

// recheckOrgNameForDuplicates scores an organization against the rest of the
// workspace after its name changed, and files the best pair the queue has not
// already been asked about.
//
// The organization excludes itself from both tiers: it holds its own domains
// and its own name, so without that it matches itself perfectly and hides
// every real rival behind the self-score.
func recheckOrgNameForDuplicates(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, by string) error {
	var display, legal string
	err := tx.QueryRow(ctx, `
		SELECT display_name, coalesce(legal_name, '')
		  FROM organization
		 WHERE id = $1 AND archived_at IS NULL`, orgID).Scan(&display, &legal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Archived or erased between the rename and here: there is no
			// record left to find a twin for, and that is not a fault.
			return nil
		}
		return fmt.Errorf("people: reading the renamed organization: %w", err)
	}

	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{
		DisplayName: display,
		LegalName:   legal,
		ExcludeID:   &orgID,
	})
	if err != nil {
		return err
	}
	if match.Decision != DecisionFuzzyReview {
		return nil
	}

	// Walk the ranked rivals rather than taking only the winner. A pair the
	// queue has already seen — merged, or dismissed as not-a-duplicate — is
	// answered, and `ON CONFLICT DO NOTHING` would silently drop this filing
	// against it. Left as the winner alone, one dismissal would mask every
	// genuine duplicate behind it for good.
	for _, rival := range match.Ranked {
		filed, err := orgPairAlreadyFiled(ctx, tx, orgID, rival.OrganizationID)
		if err != nil {
			return err
		}
		if filed {
			continue
		}
		var incumbent string
		if err := tx.QueryRow(ctx,
			`SELECT display_name FROM organization WHERE id = $1`, rival.OrganizationID).Scan(&incumbent); err != nil {
			return fmt.Errorf("people: reading the renamed organization's near match: %w", err)
		}
		return recordNearMatch(ctx, tx, entityOrganization, orgID.UUID, rival.OrganizationID.UUID,
			rival.Confidence, nearMatchEvidence(fieldDisplayName, display, incumbent, rival.Confidence),
			orgRenameRecheckSource, by)
	}
	return nil
}

// orgPairAlreadyFiled reports whether the queue already holds this pair, in
// any disposition. The pair is stored canonically (lower id left, DH-DDL-1),
// so the lookup orders the two ids the same way the insert does.
func orgPairAlreadyFiled(ctx context.Context, tx pgx.Tx, a, b ids.OrganizationID) (bool, error) {
	left, right := a.UUID, b.UUID
	if right.String() < left.String() {
		left, right = right, left
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM dedupe_candidate
		   WHERE entity_type = $1 AND left_org_id = $2 AND right_org_id = $3)`,
		entityOrganization, left, right).Scan(&exists); err != nil {
		return false, fmt.Errorf("people: reading the dedupe queue for this pair: %w", err)
	}
	return exists, nil
}
