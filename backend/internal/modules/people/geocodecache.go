// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The place cache: an address or a place name resolved to a point, remembered.
//
// IT IS MANDATORY, not an optimisation. Nominatim's usage policy requires that
// a client which runs regularly caches its results, so a deployment that asks
// the public service the same question twice is out of compliance regardless
// of how well it paces itself. It is also what makes the rate survivable:
// several companies share one industrial park, one street, one small town, and
// each repeat lookup would otherwise spend fifteen seconds of a budget the
// whole installation shares.
//
// The cache is installation-global rather than per workspace. A place is a
// place: Stuttgart's coordinates do not depend on who is asking, and keying
// this by workspace would multiply every lookup by the number of tenants for
// no gain and a policy cost.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CachedPlace is a remembered lookup.
type CachedPlace struct {
	Lat, Lon float64
	Provider string
}

// LookupPlace answers a remembered point for this query, if there is one.
//
// The key is the NORMALIZED query, so "Stuttgart", " stuttgart " and
// "STUTTGART" are one entry rather than three — a cache that stored them
// separately would ask the provider three times for one place, which is the
// thing it exists to prevent.
func (s *Store) LookupPlace(ctx context.Context, query string) (CachedPlace, bool, error) {
	key := normalizePlaceQuery(query)
	if key == "" {
		return CachedPlace{}, false, nil
	}
	var place CachedPlace
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT lat, lon, provider FROM geocode_cache WHERE query = $1`, key).
			Scan(&place.Lat, &place.Lon, &place.Provider)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CachedPlace{}, false, nil
	}
	if err != nil {
		return CachedPlace{}, false, fmt.Errorf("reading the place cache: %w", err)
	}
	return place, true, nil
}

// RememberPlace records a resolved point.
//
// A repeat write is a no-op rather than a conflict: two workers resolving the
// same place concurrently is the normal case, and the second one arriving is
// not an error to report. The FIRST answer is kept — they are the same place,
// and rewriting would churn the row for nothing.
func (s *Store) RememberPlace(ctx context.Context, query string, place CachedPlace) error {
	key := normalizePlaceQuery(query)
	if key == "" {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO geocode_cache (query, lat, lon, provider)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (query) DO NOTHING`,
			key, place.Lat, place.Lon, place.Provider)
		return err
	})
}

// normalizePlaceQuery is the cache key: lowercased, with runs of whitespace
// collapsed, so trivially different spellings of one place are one entry.
func normalizePlaceQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}
