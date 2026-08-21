// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Turning a company's address into a point, and keeping the two honest about
// each other.
//
// THE HARD PART IS NOT THE LOOKUP, it is staleness. A company's address can
// change at any time, and its coordinates cannot change in the same
// transaction — the lookup leaves the process and takes seconds. So there is
// always a window where the row holds an address and the coordinates of a
// DIFFERENT address, and a radius query that read lat/lon alone would answer
// with distances from where the company used to be, reporting success.
//
// geocode_status closes it. The writer stamps 'stale' in the same transaction
// as the address change, the worker sets 'ok' when it catches up, and only
// 'ok' is queryable. A company mid-move drops out of radius answers rather
// than appearing in the wrong place — an omission a caller can be told about,
// instead of a wrong answer they cannot see.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The statuses geocode_status carries, and what each says.
const (
	// GeocodeOK — the coordinates match the address in the row. The ONLY
	// status a radius query reads.
	GeocodeOK = "ok"
	// GeocodeFailed — the lookup did not complete. Retryable.
	GeocodeFailed = "failed"
	// GeocodeNoMatch — the geocoder resolved the address to nothing. A fact
	// about the address, not a failure, so it is not retried.
	GeocodeNoMatch = "no_match"
	// GeocodeStale — the address changed and the coordinates have not caught
	// up. Not queryable, deliberately.
	GeocodeStale = "stale"
)

// geocodeMaxAttempts bounds how often one company's address is re-asked after
// a failure. Past it the row waits for its address to change, which is the
// only thing that could make the answer different.
const geocodeMaxAttempts = 3

// GeocodeEnqueue hands the worker job to whatever runs jobs, inside the
// caller's transaction.
//
// Nil-safe by contract: a deployment with no geocoder wired passes nil, the
// address still writes, and no job is queued. Same shape as SiteReadEnqueue,
// and for the same reason — the address is what the caller asked for; the
// coordinates are what this installation can offer.
type GeocodeEnqueue func(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error

// GeocodableAddress is one company's address, ready to be asked about.
type GeocodableAddress struct {
	OrganizationID ids.OrganizationID
	Query          string
	// InputHash identifies the address this query was built from, so the
	// worker can skip one it has already resolved. Reingestion is the backfill
	// in this design, and without the hash every re-read of a website would
	// spend a lookup on an address that has not moved.
	InputHash string
}

// AddressForGeocode reads the address to resolve, or ok=false when there is
// nothing to resolve or nothing worth re-asking.
//
// It answers false for THREE different situations and the caller does not need
// to tell them apart: no address at all, an address already resolved to the
// same coordinates, and an address whose attempts are spent. Each means "do
// not ask the geocoder", which is the only question the worker has.
func (s *Store) AddressForGeocode(ctx context.Context, orgID ids.OrganizationID) (GeocodableAddress, bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return GeocodableAddress{}, false, err
	}
	var (
		line1, line2, city, region, postal, country *string
		currentHash                                 *string
		status                                      *string
		attempts                                    int
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT o.address_line1, o.address_line2, o.address_city, o.address_region,
			       o.address_postal_code, o.address_country, o.geocode_input_hash, o.geocode_status,
			       coalesce(g.attempts, 0)
			  FROM organization o
			  LEFT JOIN organization_geocode_state g ON g.organization_id = o.id
			 WHERE o.id = $1 AND o.archived_at IS NULL`, orgID).
			Scan(&line1, &line2, &city, &region, &postal, &country, &currentHash, &status, &attempts)
	})
	if err != nil {
		return GeocodableAddress{}, false, err
	}

	query := addressQuery(line1, line2, city, region, postal, country)
	if query == "" {
		return GeocodableAddress{}, false, nil
	}
	hash := addressHash(query)
	// Already resolved to THIS address. The hash rather than the status,
	// because a row that failed on a different address is worth asking again.
	if currentHash != nil && *currentHash == hash && status != nil && *status != GeocodeStale {
		return GeocodableAddress{}, false, nil
	}
	if attempts >= geocodeMaxAttempts && currentHash != nil && *currentHash == hash {
		return GeocodableAddress{}, false, nil
	}
	return GeocodableAddress{OrganizationID: orgID, Query: query, InputHash: hash}, true, nil
}

// addressQuery builds the one line a geocoder is asked about.
//
// line2 is LEFT OUT on purpose. It carries "3rd floor", "c/o Meyer", "Building
// B" — detail that names a place inside a building, which no geocoder resolves
// and which actively harms the match by adding tokens nothing can anchor on.
//
// An address with no street and no city answers empty: a country alone
// resolves to the centroid of a nation, and a company placed at the middle of
// Germany would show up in radius answers for a city it is nowhere near.
func addressQuery(line1, _, city, region, postal, country *string) string {
	parts := make([]string, 0, 5)
	for _, part := range []*string{line1, postal, city, region, country} {
		if part != nil && strings.TrimSpace(*part) != "" {
			parts = append(parts, strings.TrimSpace(*part))
		}
	}
	if !locatable(line1, city, postal) {
		return ""
	}
	return strings.Join(parts, ", ")
}

// locatable says whether the address names somewhere smaller than a country.
func locatable(line1, city, postal *string) bool {
	for _, part := range []*string{line1, city, postal} {
		if part != nil && strings.TrimSpace(*part) != "" {
			return true
		}
	}
	return false
}

// addressHash identifies an address by the query built from it, so a change
// that does not change the query — a re-typed line2, a whitespace edit — does
// not spend a lookup.
func addressHash(query string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(query)))
	return hex.EncodeToString(sum[:])
}

// RecordGeocode writes what the geocoder said, whatever it said.
//
// Every outcome is recorded, including the ones with no coordinates: a company
// whose address resolves to nothing must be remembered as such, or the sweep
// asks about it again on every pass. The attempt ledger and the row move
// together in one transaction, so a crash between them cannot leave a company
// looking resolvable forever.
func (s *Store) RecordGeocode(
	ctx context.Context, orgID ids.OrganizationID, status string, lat, lon *float64, provider, inputHash string,
) error {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE organization
			   SET geocode_lat = $2, geocode_lon = $3, geocode_status = $4,
			       geocode_provider = $5, geocode_input_hash = $6, geocoded_at = now()
			 WHERE id = $1`, orgID, lat, lon, status, provider, inputHash); err != nil {
			return fmt.Errorf("recording the geocode: %w", err)
		}
		// A success RESETS the attempts. The next address change starts with a
		// full budget, which is what makes reingestion a real backfill rather
		// than a pass that skips everything that once failed.
		if status == GeocodeOK {
			_, err := tx.Exec(ctx, `
				INSERT INTO organization_geocode_state (organization_id, attempts, last_outcome, updated_at)
				VALUES ($1, 0, $2, now())
				ON CONFLICT (organization_id) DO UPDATE
				   SET attempts = 0, last_outcome = $2, next_attempt_at = NULL, updated_at = now()`,
				orgID, status)
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_geocode_state
			       (organization_id, attempts, last_outcome, next_attempt_at, updated_at)
			VALUES ($1, 1, $2, now() + interval '1 day', now())
			ON CONFLICT (organization_id) DO UPDATE
			   SET attempts = organization_geocode_state.attempts + 1,
			       last_outcome = $2,
			       next_attempt_at = now() + interval '1 day',
			       updated_at = now()`, orgID, status)
		return err
	})
}

// invalidateGeocodeInTx marks a company's coordinates stale, in the SAME
// transaction as the address change that made them stale.
//
// This is the load-bearing half of the whole design, and it must not be
// separated from the address write. Enqueueing a job without it leaves the old
// coordinates queryable until the worker runs — seconds at best, forever if
// the worker is down — and a radius query would keep answering from the
// previous address the entire time.
//
// It is a no-op for a company that has no coordinates yet, which is most of
// them: there is nothing to invalidate, and stamping 'stale' on a row that was
// never resolved would say the coordinates are out of date rather than absent.
func invalidateGeocodeInTx(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error {
	_, err := tx.Exec(ctx, `
		UPDATE organization
		   SET geocode_status = $2
		 WHERE id = $1 AND geocode_status IS NOT NULL AND geocode_status <> $2`,
		orgID, GeocodeStale)
	return err
}

// addressChanged is what an address writer calls: invalidate what is there,
// then queue the lookup that will replace it — both in the caller's own
// transaction, so a rollback takes the job with it and a commit cannot leave a
// company holding coordinates for an address it no longer has.
//
// The two halves are ONE call because they must not be separated. Invalidating
// without enqueueing leaves a company permanently unqueryable by distance;
// enqueueing without invalidating leaves the old point answering until the
// worker catches up.
func (s *Store) addressChanged(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error {
	if err := invalidateGeocodeInTx(ctx, tx, orgID); err != nil {
		return err
	}
	if s.geocodeEnqueue == nil {
		return nil
	}
	return s.geocodeEnqueue(ctx, tx, orgID)
}

// GeocodedPoint is one company's resolved position, for the query executor.
type GeocodedPoint struct {
	OrganizationID ids.OrganizationID
	Lat, Lon       float64
	GeocodedAt     time.Time
}
