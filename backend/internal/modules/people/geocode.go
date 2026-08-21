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

// defaultGeocodeBackoff is how long a failure with no provider instruction
// waits. Long, because a lookup that failed for a reason this code cannot read
// is not worth spending the installation's shared rate on again soon — and an
// address edit resets the ledger regardless.
const defaultGeocodeBackoff = 24 * time.Hour

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
// EVERY query here carries an explicit workspace predicate. Migration 0217
// retired row-level security, so a store query that names only an id reaches
// any workspace's row — and this path runs under the SYSTEM principal from a
// job whose args name their own workspace, which means the args would
// otherwise be the authority on which tenant's data is touched. A job carrying
// workspace A and an organization id from workspace B would read and write B.
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
		nextAttempt                                 *time.Time
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT o.address_line1, o.address_line2, o.address_city, o.address_region,
			       o.address_postal_code, o.address_country, o.geocode_input_hash, o.geocode_status,
			       coalesce(g.attempts, 0), g.next_attempt_at
			  FROM organization o
			  LEFT JOIN organization_geocode_state g ON g.organization_id = o.id
			 WHERE o.id = $1 AND o.archived_at IS NULL
			   AND o.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`, orgID).
			Scan(&line1, &line2, &city, &region, &postal, &country, &currentHash, &status,
				&attempts, &nextAttempt)
	})
	if err != nil {
		return GeocodableAddress{}, false, err
	}

	query := addressQuery(line1, line2, city, region, postal, country)
	if query == "" {
		return GeocodableAddress{}, false, nil
	}
	hash := addressHash(query)
	sameAddress := currentHash != nil && *currentHash == hash
	if sameAddress && settledFor(status) {
		// This address has an answer already — a point, or a definite "not a
		// place". Asking again changes nothing until the address does.
		return GeocodableAddress{}, false, nil
	}
	if sameAddress && attempts >= geocodeMaxAttempts {
		// Tried and failed enough times. The row waits for its address to
		// change, which is the only thing that could make the answer different.
		return GeocodableAddress{}, false, nil
	}
	if sameAddress && !dueForRetry(nextAttempt, time.Now()) {
		// Failed recently. River's own schedule is not the provider's: a rate
		// limit wants the backoff this ledger recorded, not an immediate retry.
		return GeocodableAddress{}, false, nil
	}
	return GeocodableAddress{OrganizationID: orgID, Query: query, InputHash: hash}, true, nil
}

// settledFor reports whether this address already has a final answer.
//
// `failed` is NOT settled, and that distinction was the bug: treating it as
// settled meant one network blip or one 429 permanently suppressed every
// retry, because the next attempt read the same hash and gave up before
// asking. A lookup that did not complete has no answer at all.
//
// `stale` is not settled either — it is the marker saying the coordinates
// belong to a previous address.
func settledFor(status *string) bool {
	if status == nil {
		return false
	}
	return *status == GeocodeOK || *status == GeocodeNoMatch
}

// dueForRetry honours the backoff the ledger recorded.
//
// It is read HERE rather than only written, which is what the first cut got
// wrong: next_attempt_at was stamped on every failure and consulted by
// nothing, so the column documented a policy the code did not have.
func dueForRetry(nextAttempt *time.Time, now time.Time) bool {
	return nextAttempt == nil || !now.Before(*nextAttempt)
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

// addressHashInTx identifies the address the row holds RIGHT NOW.
//
// Separate from AddressForGeocode because the two ask different questions at
// different times: that one asks "is this worth looking up", before the
// lookup; this one asks "is this still the address I looked up", after it. One
// query serving both would have to answer the second with data read for the
// first, which is the staleness this whole file is about.
func addressHashInTx(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (string, error) {
	var line1, line2, city, region, postal, country *string
	if err := tx.QueryRow(ctx, `
		SELECT address_line1, address_line2, address_city, address_region,
		       address_postal_code, address_country
		  FROM organization
		 WHERE id = $1 AND archived_at IS NULL
		   AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`, orgID).
		Scan(&line1, &line2, &city, &region, &postal, &country); err != nil {
		return "", fmt.Errorf("re-reading the address before recording its point: %w", err)
	}
	query := addressQuery(line1, line2, city, region, postal, country)
	if query == "" {
		return "", nil
	}
	return addressHash(query), nil
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
	return s.recordGeocodeAfter(ctx, orgID, status, lat, lon, provider, inputHash, 0)
}

// RecordGeocodeBackoff is RecordGeocode for a failure the PROVIDER put a clock
// on: a 429 carrying Retry-After.
//
// The wait is honoured rather than noted. Retrying on the job runner's own
// schedule when a provider has said "not for ten minutes" is how a rate limit
// becomes a block, and this installation shares one budget across every
// company it will ever geocode.
func (s *Store) RecordGeocodeBackoff(
	ctx context.Context, orgID ids.OrganizationID, inputHash string, wait time.Duration,
) error {
	return s.recordGeocodeAfter(ctx, orgID, GeocodeFailed, nil, nil, "", inputHash, wait)
}

func (s *Store) recordGeocodeAfter(
	ctx context.Context, orgID ids.OrganizationID, status string, lat, lon *float64,
	provider, inputHash string, wait time.Duration,
) error {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// CONDITIONAL ON THE ADDRESS NOT HAVING MOVED, and this is the whole
		// point of the input hash.
		//
		// The worker read the address, left the process for seconds, and came
		// back. An editor can have committed a new address in between. An
		// unconditional write would then stamp status='ok' with the point of
		// the PREVIOUS address onto a row that now holds a different one, and a
		// radius query would answer from the old place while reporting success
		// — the exact defect geocode_status exists to prevent, reintroduced by
		// the code that clears it.
		//
		// The row is re-read and re-hashed rather than compared against
		// geocode_input_hash: that column records what was last RESOLVED, so
		// matching it would accept a write for an address the row no longer
		// has. The live columns are the only authority on what the address IS.
		current, err := addressHashInTx(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if current != inputHash {
			// The address moved while we were asking. Nothing is written: the
			// trigger has already marked the row stale, and a successor job for
			// the new address is what resolves it.
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE organization
			   SET geocode_lat = $2, geocode_lon = $3, geocode_status = $4,
			       geocode_provider = $5, geocode_input_hash = $6, geocoded_at = now()
			 WHERE id = $1
			   AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
			orgID, lat, lon, status, provider, inputHash); err != nil {
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
		// The provider's own wait when it gave one, and a day otherwise. A day
		// is deliberately long: a lookup that failed for any reason this code
		// cannot read is not worth spending the shared rate on again soon, and
		// an address edit resets the ledger anyway.
		next := defaultGeocodeBackoff
		if wait > 0 {
			next = wait
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO organization_geocode_state
			       (organization_id, attempts, last_outcome, next_attempt_at, updated_at)
			VALUES ($1, 1, $2, now() + $3::interval, now())
			ON CONFLICT (organization_id) DO UPDATE
			   SET attempts = organization_geocode_state.attempts + 1,
			       last_outcome = $2,
			       next_attempt_at = now() + $3::interval,
			       updated_at = now()`, orgID, status, next.String())
		return err
	})
}

// organizationAddressColumns is the set an address change touches. Named once
// so a caller asking "did the address move" cannot ask about five of six.
var organizationAddressColumns = []string{
	"address_line1", "address_line2", "address_city",
	"address_region", "address_postal_code", "address_country",
}

// movedAddress reports whether the patch actually assigned an address column.
//
// The patch records a column only when the value CHANGED (storekit's
// recordAssignment), so this asks the question that matters — did the company
// move — rather than "did the request mention an address". Re-submitting a form
// with an unchanged address would otherwise spend a lookup, and every lookup is
// fifteen seconds of a rate the whole installation shares.
func movedAddress(after map[string]any) bool {
	for _, column := range organizationAddressColumns {
		if _, assigned := after[column]; assigned {
			return true
		}
	}
	return false
}

// enqueueGeocode queues the lookup that will replace a company's coordinates,
// in the CALLER's transaction — so a rollback takes the job with it and a
// commit cannot leave a company marked stale with nothing coming to fix it.
//
// It does NOT invalidate. The trigger installed with the geocode columns does
// that, on any address column that actually changed, which is the only place
// the rule cannot be forgotten: six writers in this package touch an address,
// several through table-driven SQL with no seam to carry, and the seventh will
// be written by somebody who never read this file.
func (s *Store) enqueueGeocode(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error {
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
