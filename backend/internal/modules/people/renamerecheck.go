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
// pinned never) and it never overrules the rename: the rename is right, the
// duplicate is a separate question, and the human answers it in the queue.
//
// It does run in the renaming transaction, and a fault here therefore fails
// the rename with it. That is not a preference — Postgres aborts a transaction
// on the first failed statement, so "observe without being able to fail the
// write" does not exist in-transaction; the only ways out are a savepoint
// around the detection, which trades the fault for a silently swallowed one,
// or detecting after commit, which gives up the detection-time snapshot the
// queue renders (DH-N-8). Everything this touches is a plain read, an
// ON CONFLICT DO NOTHING insert and an append-only log line, so a fault here
// means the transaction was already lost.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

	// Two organizations converging on ONE name concurrently would, at READ
	// COMMITTED, each read before the other committed, each find nothing, and
	// both land with no pair filed — the duplicate this whole path exists to
	// catch, missed by a race. Taking the normalized names as write identities
	// serializes them, so the second sees the first.
	//
	// BOTH axes are locked, because either can be the one that collides: two
	// records converging on a shared registered name while their display names
	// stay different is the same race, and locking only the display name would
	// leave it open. The keys are taken in a fixed order so two renames naming
	// the same pair in opposite orders cannot deadlock.
	if err := lockOrgNameIdentities(ctx, tx, display, legal); err != nil {
		return err
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
		return recordNearMatch(ctx, tx, entityOrganization, orgID.UUID, rival.OrganizationID.UUID,
			rival.Confidence,
			nearMatchEvidence(rival.MatchedField, rival.CandidateValue, rival.IncumbentValue, rival.Confidence),
			orgRenameRecheckSource, by)
	}
	return nil
}

// lockOrgNameIdentities takes each distinct normalized name as a write
// identity, sorted so the order is the same for every caller.
func lockOrgNameIdentities(ctx context.Context, tx pgx.Tx, names ...string) error {
	keys := make([]string, 0, len(names))
	for _, name := range names {
		if key := NormalizeOrgName(name); key != "" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		if err := storekit.LockWriteIdentity(ctx, tx, "organization_name", key); err != nil {
			return err
		}
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
