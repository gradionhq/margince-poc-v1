// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The geocode store's three statements, run against a real database.
//
// Every one of them named `organization.workspace_id` when the feature shipped,
// three days after ADR-0091 §8 phase D dropped that column — so all three
// failed at the first query, and geocoding an organization could never have
// worked. Nothing caught it because geocode_test.go is a unit suite with no
// database: it exercises the address-hashing and backoff arithmetic, which is
// the half that needs no Postgres, and the SQL had never been executed at all.
//
// So this suite is deliberately shallow and deliberately WIDE. It asserts no
// geocoding behaviour that the unit tests already cover; it exists so that each
// statement is issued once against the schema it will meet in production, which
// is the only thing that would have caught a column that is not there.

import (
	"context"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEveryGeocodeStatementRunsAgainstTheRealSchema(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Geocodable Gmbh", Source: "manual",
		Address: &crmcontracts.Address{
			Line1:      strPtr("Rosenthaler Str. 40"),
			City:       strPtr("Berlin"),
			PostalCode: strPtr("10178"),
			Country:    strPtr("DE"),
		},
	})
	if err != nil {
		t.Fatalf("seeding the organization to geocode: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	// 1. The read. It joins organization_geocode_state, which is the statement
	//    the shipped bug failed on first.
	addr, ok, err := e.store.AddressForGeocode(ctx, orgID)
	if err != nil {
		t.Fatalf("AddressForGeocode: %v", err)
	}
	if !ok {
		t.Fatal("a company with a street address is not geocodable, so nothing downstream will ever ask a provider for its point")
	}
	if addr.Query == "" {
		t.Error("the geocodable address carries an empty query; a provider cannot be asked for nothing")
	}

	// 2. The success write, which is also the re-read of the address hash: a
	//    geocode that landed against a CHANGED address must not be recorded,
	//    so RecordGeocode reads the hash back inside its own transaction.
	lat, lon := 52.5244, 13.4105
	if err := e.store.RecordGeocode(ctx, orgID, "ok", &lat, &lon, "fake", addr.InputHash); err != nil {
		t.Fatalf("RecordGeocode: %v", err)
	}

	// The point is readable back, which is what makes the write above a write
	// rather than a statement that merely did not error.
	if status, lat, lon := readGeocode(t, e, orgID); status != "ok" || lat == nil || lon == nil {
		t.Fatalf("after an ok geocode the organization reads status %q with point (%v, %v); want ok and a point", status, lat, lon)
	}

	// 3. The backoff write, on the same row, so the state table's upsert path
	//    runs too. It records a FAILURE, so it is asserted as one rather than
	//    with the success above — a backoff that left the point standing would
	//    report a resolved address the provider never resolved.
	if err := e.store.RecordGeocodeBackoff(ctx, orgID, addr.InputHash, 0); err != nil {
		t.Fatalf("RecordGeocodeBackoff: %v", err)
	}
	if status, _, _ := readGeocode(t, e, orgID); status != "failed" {
		t.Errorf("after a backoff the organization reads status %q, want failed", status)
	}
}

// readGeocode reads the three columns the writes above are about, on the owner
// pool, so the assertion does not depend on the store it is checking.
func readGeocode(t *testing.T, e *dedupeEnv, orgID ids.OrganizationID) (status string, lat, lon *float64) {
	t.Helper()
	if err := e.store.db.Pool().QueryRow(context.Background(),
		`SELECT coalesce(geocode_status, ''), geocode_lat, geocode_lon FROM organization WHERE id = $1`,
		orgID).Scan(&status, &lat, &lon); err != nil {
		t.Fatalf("reading the recorded geocode: %v", err)
	}
	return status, lat, lon
}
