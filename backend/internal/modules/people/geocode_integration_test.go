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
	"slices"
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

// The sweep finds the rows an address write never will, and only those.
//
// This is the backfill's whole reason: a company written before the
// installation had a geocoder is invisible to the trigger, because nothing
// will ever write its address again. A seeded workspace is exactly that case,
// and without this pass within_radius answers from an empty set while looking
// like a query that works.
func TestTheSweepFindsCompaniesNoWriteWillEverReach(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	located, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Findable GmbH", Source: "manual",
		Address: &crmcontracts.Address{City: strPtr("Stuttgart"), Country: strPtr("DE")},
	})
	if err != nil {
		t.Fatalf("seeding a company with an address: %v", err)
	}
	// A country alone is not a place; the sweep must not spend a lookup on it.
	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Nowhere GmbH", Source: "manual",
		Address: &crmcontracts.Address{Country: strPtr("DE")},
	}); err != nil {
		t.Fatalf("seeding a company with no usable address: %v", err)
	}
	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Addressless GmbH", Source: "manual",
	}); err != nil {
		t.Fatalf("seeding a company with no address: %v", err)
	}

	due, err := e.store.ListNeverGeocoded(ctx, GeocodeBackfillBatch)
	if err != nil {
		t.Fatalf("ListNeverGeocoded: %v", err)
	}
	wantID := ids.From[ids.OrganizationKind](ids.UUID(located.Id))
	if !slices.Contains(due, wantID) {
		t.Errorf("the sweep listed %v, want the company with a city among them — "+
			"a seeded database is the case this exists for", due)
	}
	for _, id := range due {
		if id == wantID {
			continue
		}
		t.Errorf("the sweep also nominated %v; a company without a usable address "+
			"costs a lookup to learn nothing", id)
	}

	// Answered rows are not re-swept. A company that has been through the
	// worker carries its own retry ledger — `failed` waits for its backoff,
	// `no_match` waits for its address to change — and re-nominating those
	// would spend the installation's rate re-asking settled questions and
	// defeat the backoff by asking again every pass.
	// Through the real read, because RecordGeocode only writes when the input
	// hash still matches the row's address — a guard against a worker landing
	// an answer for an address that was edited while it was away. A made-up
	// hash writes nothing and the assertion below would pass on an empty
	// update, proving only that the test was wrong.
	answered, ok, err := e.store.AddressForGeocode(ctx, wantID)
	if err != nil || !ok {
		t.Fatalf("AddressForGeocode before recording: %v (ok=%v)", err, ok)
	}
	if err := e.store.RecordGeocode(ctx, wantID, GeocodeNoMatch, nil, nil, "test", answered.InputHash); err != nil {
		t.Fatalf("recording an answer: %v", err)
	}
	after, err := e.store.ListNeverGeocoded(ctx, GeocodeBackfillBatch)
	if err != nil {
		t.Fatalf("ListNeverGeocoded after an answer: %v", err)
	}
	if slices.Contains(after, wantID) {
		t.Error("an answered company is still swept, so every pass re-asks a settled question")
	}
}
