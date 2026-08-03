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
	"unicode"

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

	// Two organizations converging on names that score against each other would,
	// at READ COMMITTED, each read before the other committed, each find
	// nothing, and both land with no pair filed — the duplicate this whole path
	// exists to catch, missed by a race. Both axes are locked, because either
	// can be the colliding one: two records converging on a shared registered
	// name while their display names stay different is the same race.
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

// lockOrgNameIdentities serializes writers whose names are likely to score
// against each other. Within one call the keys are sorted and deduped, so two
// calls naming the same set take them in the same order.
//
// It is BEST EFFORT, and the limit is structural rather than an oversight: the
// tier it guards matches on similarity, and an advisory lock keys on equality.
// No point key can cover a similarity neighbourhood, so some pairs that would
// be filed are not serialized and can still race. orgNameLockKey picks the key
// that covers the most, and renamerecheck_test.go carries the inventory of what
// each choice does and does not reach.
//
// A pair that races through is not re-detected today — only a later rename of
// either row looks again (recheckOrgNameForDuplicates is the sole re-scorer,
// and it runs per renamed row, not over the workspace). A periodic re-scan is
// what would close it; there is none yet.
//
// Deadlock: sorting fixes the order WITHIN one call, not across two. A
// transaction that locks twice with different key sets — applyColdStartTx does,
// via DedupeOrganizationForCreate and then the re-check after legal_name fills —
// can still acquire keys in the opposite order to a mirrored transaction and
// deadlock. Postgres detects it and kills one, so it surfaces as a retryable
// failure rather than a lost write; closing it properly means locking the union
// of both name sets once, before the create, which the cold-start path cannot
// do until it knows which organization it resolved onto.
func lockOrgNameIdentities(ctx context.Context, tx pgx.Tx, names ...string) error {
	keys := make([]string, 0, len(names))
	for _, name := range names {
		if key := orgNameLockKey(name); key != "" {
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

// orgNameLockKey is the first jaroWinklerMaxPrefix letters or digits of the
// normalized name, separators dropped, or "" when the name carries none.
//
// It mirrors the metric deliberately: nameSimilarity boosts a shared prefix of
// exactly that many RUNES, and it does not care where a space or hyphen falls.
// Keying on the first token instead splits the pairs that boost hardest —
// "Microsoft Corp" against "Micro Soft Corp" scores 0.98, "Acme Ltd" against
// "ACME-Group Ltd" scores 0.88, and a token key sends each half elsewhere.
// Dropping separators puts both halves of those pairs on one key.
//
// The trade is real and runs the other way too: "The Boring Company" and "The
// Home Depot" score 0.78 and a token key would have grouped them, while this
// one splits them at the fourth rune. Neither choice covers everything; this
// one covers the pairs the metric is most confident about.
func orgNameLockKey(name string) string {
	key := make([]rune, 0, jaroWinklerMaxPrefix)
	for _, r := range NormalizeOrgName(name) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		if key = append(key, r); len(key) == jaroWinklerMaxPrefix {
			break
		}
	}
	return string(key)
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
