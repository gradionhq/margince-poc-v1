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
	id       ids.UUID
	company  *string
	stored   *string
	status   string
	decided  bool
	dupGroup string
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
		all, err := readGhostKeys(ctx, tx)
		if err != nil {
			return err
		}
		groups, wanted := groupByCurrentKey(all)
		for _, group := range groups {
			merged, rekeyed, err := collapseGroup(ctx, tx, group, wanted)
			if err != nil {
				return err
			}
			out.Merged += merged
			out.Rekeyed += rekeyed
		}
		return nil
	})
	return out, err
}

// readGhostKeys loads every CSV-sourced ghost with the parts of its natural
// key that the normalizer does not touch, so grouping can be done in Go where
// the normalizer lives.
func readGhostKeys(ctx context.Context, tx pgx.Tx) ([]ghostKeyRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, company_name, normalized_company, match_status,
		       owner_user_id::text || '|' || normalized_name || '|' ||
		         coalesce(connected_on::text, 'epoch')
		  FROM linkedin_connection
		 WHERE provider_member_ref IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("people: reading LinkedIn keys to re-normalize: %w", err)
	}
	defer rows.Close()
	var all []ghostKeyRow
	for rows.Next() {
		var r ghostKeyRow
		if err := rows.Scan(&r.id, &r.company, &r.stored, &r.status, &r.dupGroup); err != nil {
			return nil, err
		}
		r.decided = r.status != "unmatched"
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

// collapseGroup keeps one row from a duplicate set and re-keys it. Deleting the
// others rather than merging them is right: they hold the same facts about the
// same connection, and the only thing worth preserving is a human's decision,
// which survivor() already keeps.
func collapseGroup(ctx context.Context, tx pgx.Tx, group []ghostKeyRow, wanted map[ids.UUID]string) (merged, rekeyed int, err error) {
	keep := survivor(group)
	for _, r := range group {
		if r.id == keep.id {
			continue
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

// survivor picks which of a duplicate set to keep: a row carrying a human's
// decision outranks one that does not, and the oldest id breaks the tie so the
// choice is deterministic across replicas and re-runs.
func survivor(group []ghostKeyRow) ghostKeyRow {
	best := group[0]
	for _, r := range group[1:] {
		switch {
		case r.decided && !best.decided:
			best = r
		case r.decided == best.decided && r.id.String() < best.id.String():
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
