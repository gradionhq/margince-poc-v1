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

// ListNeverGeocoded answers which companies have an address and no answer.
//
// NEVER-ASKED only, deliberately. A row with any geocode_status has been
// through the worker and carries its own retry ledger — `failed` waits for its
// backoff, `no_match` waits for its address to change — and a sweep that
// re-nominated those would spend the installation's rate on questions already
// answered, and would defeat the backoff by asking again every pass.
//
// `stale` is not swept either: the trigger sets it in the same transaction as
// the address write that also queues the lookup, so a stale row already has a
// job coming.
func (s *Store) ListNeverGeocoded(ctx context.Context, limit int) ([]ids.OrganizationID, error) {
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
			 WHERE o.archived_at IS NULL
			   AND o.geocode_status IS NULL
			   -- The same bar locatable() holds a written address to: a country
			   -- on its own is not a place a distance can be measured from, and
			   -- nominating one spends a lookup to learn nothing.
			   AND (coalesce(o.address_line1, '') <> ''
			     OR coalesce(o.address_city, '') <> ''
			     OR coalesce(o.address_postal_code, '') <> '')
			 ORDER BY o.created_at
			 LIMIT $1`, limit)
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
