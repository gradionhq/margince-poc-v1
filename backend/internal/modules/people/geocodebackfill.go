// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The backfill: companies whose address was written before this installation
// had a geocoder, or before geocoding existed at all.
//
// Geocoding fires on an address WRITE, which is the right trigger and a
// complete answer only for a company written after the feature was configured.
// Everything already in the database is invisible to it — a seeded workspace,
// an import that ran last month, or the ordinary case of an operator setting
// MARGINCE_GEOCODE_BASE_URL on a system that already holds its customers. None
// of those rows will ever be written again, so none of them will ever be
// located, and `within_radius` answers from an empty set while looking exactly
// like a working query.
//
// This does not decide anything. It finds rows that have never been asked
// about and hands each to the same per-company job an address write queues;
// AddressForGeocode still makes the real judgement one row at a time, so a
// company the sweep nominates but that turns out settled costs a read and no
// lookup.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GeocodeBackfillBatch is how many companies one sweep pass nominates.
//
// Small on purpose. The provider's terms hold this installation to four
// lookups a minute, so a pass of 50 is already twelve minutes of work — and
// the pass runs again. A larger batch would not geocode anything sooner; it
// would only pile rows into a queue that drains at a fixed rate, where they
// would sit behind an address a person edited and is waiting on.
const GeocodeBackfillBatch = 50

// ListGeocodeOrphans answers which companies have an address and no coordinates
// coming.
//
// NEVER-ASKED only, deliberately. A row with any geocode_status has been
// through the worker and carries its own retry ledger — `failed` waits for its
// backoff, `no_match` waits for its address to change — and a sweep that
// re-nominated those would spend the installation's rate on questions already
// answered, and would defeat the backoff by asking again every pass.
//
// `stale` IS swept, and that is not symmetry — it closes an orphan. The
// trigger marks a row stale on any address column that changes, but only the
// update path pairs that with an enqueue. The site-read profile apply writes
// address_line1 through table-driven SQL with no seam to carry a callback
// (company.go), so a company whose address arrives from its own website is
// marked stale with nothing coming to resolve it, and its old coordinates are
// gone. Without this it would sit that way forever.
//
// Re-nominating a stale row costs nothing when a job IS already coming: the
// insert deduplicates by args across every active state, so the sweep's
// nomination collapses into the one the write queued.
func (s *Store) ListGeocodeOrphans(ctx context.Context, limit int) ([]ids.OrganizationID, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = GeocodeBackfillBatch
	}
	var out []ids.OrganizationID
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id
			  FROM organization o
			  LEFT JOIN organization_geocode_state g ON g.organization_id = o.id
			 WHERE o.archived_at IS NULL
			   -- NULL: never asked. 'stale': the trigger cleared the point and
			   -- some writers cannot queue (see above). 'failed': the lookup did
			   -- not complete, which is not an answer — and a failure whose
			   -- backoff has expired has no River job left to carry it, so
			   -- nothing but this would ever ask again. A deployment that
			   -- records 'failed' because it had no provider AT ALL is the same
			   -- shape: configure one later and these are the rows it needs.
			   --
			   -- 'ok' and 'no_match' are answers and are never swept.
			   AND (o.geocode_status IS NULL
			     OR o.geocode_status IN ('stale', 'failed'))
			   -- A failure still inside its backoff is left alone: the ledger
			   -- asked for that wait, and re-nominating every pass is how a
			   -- rate limit becomes a block. AddressForGeocode re-checks this
			   -- too; asking here keeps the sweep from queueing work it knows
			   -- will decline.
			   AND (g.next_attempt_at IS NULL OR g.next_attempt_at <= now())
			   AND coalesce(g.attempts, 0) < $2
			   -- The same bar locatable() holds a written address to: a country
			   -- on its own is not a place a distance can be measured from, and
			   -- nominating one spends a lookup to learn nothing.
			   AND (coalesce(o.address_line1, '') <> ''
			     OR coalesce(o.address_city, '') <> ''
			     OR coalesce(o.address_postal_code, '') <> '')
			 ORDER BY o.created_at
			 LIMIT $1`, limit, geocodeMaxAttempts)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.OrganizationID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
