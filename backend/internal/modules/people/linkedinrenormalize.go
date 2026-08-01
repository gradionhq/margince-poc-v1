// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Re-normalizing stored LinkedIn company keys (ADR-0078 §2.1b).
//
// `linkedin_connection.normalized_company` is a DERIVED column and it is part
// of the natural dedupe key (uq_linkedin_connection_natural). That pairing has
// a consequence which is easy to miss and expensive to discover: changing the
// normalizer changes the key, so the same connection stops matching the row it
// already has and a re-import inserts a second copy of it.
//
// That is not hypothetical — it happened. Cleaning LinkedIn's headline company
// field ("najahak.io | نجاحك" → "najahak.io") altered the key for every row
// whose company carried a tagline, and re-importing the same export produced
// 209 duplicate connections on a real workspace. Every org-level reach count
// those rows feed was then double-counted.
//
// So a normalizer change owes a backfill, and this is it: recompute every
// stored key from the CURRENT normalizer, and collapse the rows that then
// collide. It is idempotent — a workspace already re-normalized costs one scan
// and writes nothing — so it can run on every boot without a version flag to
// forget to bump.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// LinkedInRenormalizeResult reports what one backfill pass changed.
type LinkedInRenormalizeResult struct {
	Rekeyed int
	Merged  int
}

type ghostKeyRow struct {
	id      ids.UUID
	company *string
	stored  *string
	status  string
	// syncedAt breaks a tie between two rows carrying the same strength of
	// decision. The NEWER one wins: it is the one the latest export described,
	// and its profile URL is the address the connection is reachable at today.
	syncedAt time.Time
	dupGroup string
}

// matchRankOrder ranks the match states by how much human judgement they carry,
// weakest first, so a merge keeps the strongest rather than whichever row
// happened to be chosen as the survivor. A rejection outranks a suggestion:
// somebody looked and said no, and losing that would re-ask a question they
// already answered. Passed to SQL as an array so the ordering is spelled once.
var matchRankOrder = []string{"unmatched", "suggested", "rejected", "confirmed"}

// matchRank is a status's position in matchRankOrder, so Go and SQL rank the
// four states by the one list.
func matchRank(status string) int {
	for i, s := range matchRankOrder {
		if s == status {
			return i
		}
	}
	return 0
}

// RenormalizeLinkedInCompanyKeys recomputes every stored company key and
// collapses the duplicates a previous normalizer left behind.
//
// It runs under the system principal from a worker, so it takes no auth gate
// of its own: there is no human actor, and the pass is a maintenance rewrite of
// a derived column rather than a read of anybody's records.
func (s *Store) RenormalizeLinkedInCompanyKeys(ctx context.Context) (LinkedInRenormalizeResult, error) {
	var out LinkedInRenormalizeResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = renormalizeGhostKeysTx(ctx, tx, ids.Nil)
		return err
	})
	return out, err
}

// renormalizeGhostKeysTx is the pass itself, inside a caller's transaction.
//
// The import calls it BEFORE upserting, and that is not belt-and-braces: the
// import computes its keys with today's normalizer while stored rows may still
// carry yesterday's, so an import running between two sweeps would create the
// very duplicates the sweep exists to clean up. Repairing first makes the
// import self-healing rather than dependent on a background pass having
// happened to run.
func renormalizeGhostKeysTx(ctx context.Context, tx pgx.Tx, onlyOwner ids.UUID) (LinkedInRenormalizeResult, error) {
	var out LinkedInRenormalizeResult
	all, err := readGhostKeys(ctx, tx, onlyOwner)
	if err != nil {
		return out, err
	}
	groups, wanted := groupByCurrentKey(all)
	for _, group := range groups {
		merged, rekeyed, err := collapseGroup(ctx, tx, group, wanted)
		if err != nil {
			return out, err
		}
		out.Merged += merged
		out.Rekeyed += rekeyed
	}
	return out, nil
}

// readGhostKeys loads every CSV-sourced ghost with the parts of its natural
// key that the normalizer does not touch, so grouping can be done in Go where
// the normalizer lives.
// onlyOwner zero means every member's rows — the worker's sweep. A non-zero
// value narrows to one member, which is what the INTERACTIVE import passes: an
// upload must not rewrite and delete a colleague's rows inside the uploader's
// request, both because it is a write on rows the caller has no read path to
// and because it puts every member's export behind one member's transaction.
func readGhostKeys(ctx context.Context, tx pgx.Tx, onlyOwner ids.UUID) ([]ghostKeyRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, company_name, normalized_company, match_status, synced_at,
		       owner_user_id::text || '|' || normalized_name || '|' ||
		         coalesce(connected_on::text, 'epoch')
		  FROM linkedin_connection
		 WHERE provider_member_ref IS NULL
		   AND ($1::uuid IS NULL OR owner_user_id = $1)`, nullableOwner(onlyOwner))
	if err != nil {
		return nil, fmt.Errorf("people: reading LinkedIn keys to re-normalize: %w", err)
	}
	defer rows.Close()
	var all []ghostKeyRow
	for rows.Next() {
		var r ghostKeyRow
		if err := rows.Scan(&r.id, &r.company, &r.stored, &r.status, &r.syncedAt, &r.dupGroup); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, rows.Err()
}

// groupByCurrentKey buckets rows by the key TODAY's normalizer produces.
// Everything in one bucket is the same connection, however it happened to be
// spelled when it was stored.
func groupByCurrentKey(all []ghostKeyRow) (map[string][]ghostKeyRow, map[ids.UUID]string) {
	groups := map[string][]ghostKeyRow{}
	wanted := map[ids.UUID]string{}
	for _, r := range all {
		key := ""
		if r.company != nil {
			key = NormalizeOrgName(cleanLinkedInCompany(*r.company))
		}
		wanted[r.id] = key
		groups[r.dupGroup+"|"+key] = append(groups[r.dupGroup+"|"+key], r)
	}
	return groups, wanted
}

// collapseGroup folds a duplicate set into one row.
//
// The other rows are NOT simply discarded. Two copies of one connection were
// written at different times and each may hold something the other lacks — most
// of all an EMAIL, which is the only field that can confirm a match rather than
// suggest one, and which LinkedIn only supplies for connections who allowed it.
// Dropping the copy that carried the address would quietly downgrade a
// confirmable match to a guess. So every field is folded into the survivor
// first, and only then are the others deleted.
func collapseGroup(ctx context.Context, tx pgx.Tx, group []ghostKeyRow, wanted map[ids.UUID]string) (merged, rekeyed int, err error) {
	keep := survivor(group)
	for _, r := range group {
		if r.id == keep.id {
			continue
		}
		if err := foldGhostInto(ctx, tx, keep.id, r.id); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM linkedin_connection WHERE id = $1`, r.id); err != nil {
			return 0, 0, fmt.Errorf("people: collapsing a duplicate LinkedIn connection: %w", err)
		}
		merged++
	}
	if stored(keep.stored) != wanted[keep.id] {
		if _, err := tx.Exec(ctx, `
			UPDATE linkedin_connection
			   SET normalized_company = NULLIF($2, ''), updated_at = now()
			 WHERE id = $1`, keep.id, wanted[keep.id]); err != nil {
			return 0, 0, fmt.Errorf("people: re-keying a LinkedIn connection: %w", err)
		}
		rekeyed++
	}
	return merged, rekeyed, nil
}

// foldGhostInto copies whatever the doomed row knows and the survivor does not
// into the survivor, before the doomed row is deleted.
//
// coalesce in the survivor's favour for every field EXCEPT the match state and
// the timestamps, where the strongest and the newest win: a re-import refreshes
// position and company, and a human's decision must outlive whichever copy
// happened to be kept.
func foldGhostInto(ctx context.Context, tx pgx.Tx, keep, doomed ids.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE linkedin_connection k
		   SET email               = coalesce(k.email, d.email),
		       position            = coalesce(k.position, d.position),
		       company_name        = coalesce(k.company_name, d.company_name),
		       connected_on        = coalesce(k.connected_on, d.connected_on),
		       -- The NEWER export's URL wins. A member who changed their vanity
		       -- address is reachable at the new one and not the old, so
		       -- coalescing in the survivor's favour would keep a dead link
		       -- alive purely because its row happened to be kept.
		       profile_url = CASE
		         WHEN d.profile_url IS NOT NULL AND d.synced_at > k.synced_at
		           THEN d.profile_url ELSE coalesce(k.profile_url, d.profile_url) END,
		       provider_member_ref = coalesce(k.provider_member_ref, d.provider_member_ref),
		       -- The person travels WITH the winning decision. Coalescing it
		       -- independently would let the survivor keep its own guess while
		       -- inheriting the other copy's confirmed status, reporting a
		       -- human's decision over a link they never made.
		       matched_person_id = CASE
		         WHEN array_position($3::text[], d.match_status) > array_position($3::text[], k.match_status)
		           THEN d.matched_person_id ELSE coalesce(k.matched_person_id, d.matched_person_id) END,
		       matched_org_id      = coalesce(k.matched_org_id, d.matched_org_id),
		       -- The stronger decision wins, and it must be spelled here as
		       -- well as in survivor(): the survivor is chosen before the fold,
		       -- so a rejected copy of a kept unmatched row would otherwise be
		       -- thrown away with its judgement.
		       match_status = CASE
		         WHEN array_position($3::text[], d.match_status) > array_position($3::text[], k.match_status)
		           THEN d.match_status ELSE k.match_status END,
		       -- A row is tombstoned only when EVERY copy was: one live copy
		       -- means the connection is still in somebody's export.
		       tombstoned_at = CASE
		         WHEN k.tombstoned_at IS NULL OR d.tombstoned_at IS NULL THEN NULL
		         ELSE greatest(k.tombstoned_at, d.tombstoned_at) END,
		       synced_at  = greatest(k.synced_at, d.synced_at),
		       updated_at = now()
		  FROM linkedin_connection d
		 WHERE k.id = $1 AND d.id = $2`,
		keep, doomed, matchRankOrder)
	if err != nil {
		return fmt.Errorf("people: folding a duplicate LinkedIn connection into its survivor: %w", err)
	}
	return nil
}

// survivor picks which of a duplicate set to keep.
//
// Ranked by the STRENGTH of the decision on the row: confirmed outranks
// rejected outranks suggested outranks unmatched, the same order the fold uses,
// so the row that is kept is the row whose matched_person_id the merged record
// will carry. A decided/undecided boolean would let a machine suggestion tie
// with a human confirmation and win on age.
//
// The newer sync breaks a tie, and the id breaks that, so the choice is
// deterministic across replicas and re-runs.
func survivor(group []ghostKeyRow) ghostKeyRow {
	best := group[0]
	for _, r := range group[1:] {
		switch {
		case matchRank(r.status) != matchRank(best.status):
			if matchRank(r.status) > matchRank(best.status) {
				best = r
			}
		case !r.syncedAt.Equal(best.syncedAt):
			if r.syncedAt.After(best.syncedAt) {
				best = r
			}
		case r.id.String() < best.id.String():
			best = r
		}
	}
	return best
}

func stored(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
