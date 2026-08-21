// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"testing"
	"time"
)

// A transient failure must NOT settle the address.
//
// The first cut treated `failed` as an answer, so one network blip or one 429
// permanently suppressed every retry: the next attempt read the same address
// hash, saw a status, and gave up before asking. A lookup that did not complete
// has no answer at all.
func TestAFailedLookupIsNotAnAnswer(t *testing.T) {
	for _, status := range []string{GeocodeFailed, GeocodeStale} {
		if settledFor(&status) {
			t.Errorf("%q was treated as a final answer; only a point or a definite no-match settles "+
				"an address, and treating a failure as settled suppresses every retry", status)
		}
	}
	for _, status := range []string{GeocodeOK, GeocodeNoMatch} {
		if !settledFor(&status) {
			t.Errorf("%q was not treated as settled; re-asking changes nothing until the address does", status)
		}
	}
	if settledFor(nil) {
		t.Error("a row that has never been looked up was treated as settled")
	}
}

// The recorded backoff is honoured, not merely written.
//
// next_attempt_at was stamped on every failure and consulted by nothing, so the
// column documented a policy the code did not have.
func TestTheRecordedBackoffIsHonoured(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	if dueForRetry(&future, now) {
		t.Error("a lookup was retried before the backoff it recorded had passed — a provider that said " +
			"'not yet' was ignored, which is how a rate limit becomes a block")
	}
	if !dueForRetry(&past, now) {
		t.Error("a lookup whose backoff has passed was not retried")
	}
	if !dueForRetry(nil, now) {
		t.Error("a company that has never failed was made to wait")
	}
}

// An address naming only a country is not asked about.
//
// A country resolves to the centroid of a nation, so a company placed in the
// middle of Germany would appear in radius answers for a city it is nowhere
// near — a wrong answer that looks like every other answer.
func TestACountryAloneIsNotAPlace(t *testing.T) {
	country := "Germany"
	if got := addressQuery(nil, nil, nil, nil, nil, &country); got != "" {
		t.Errorf("a country alone built the query %q; it must build none", got)
	}
	city := "Stuttgart"
	if got := addressQuery(nil, nil, &city, nil, nil, &country); got == "" {
		t.Error("a city and a country built no query, and that is a place")
	}
}

// line2 is left out: "3rd floor", "c/o Meyer" name a place inside a building,
// which no geocoder resolves and which worsens the match.
func TestTheQueryLeavesOutTheDetailNoGeocoderCanUse(t *testing.T) {
	line1, line2, city := "Hauptstr. 1", "3rd floor, c/o Meyer", "Stuttgart"
	got := addressQuery(&line1, &line2, &city, nil, nil, nil)
	if got == "" {
		t.Fatal("a street and a city built no query")
	}
	if strings.Contains(got, "Meyer") || strings.Contains(got, "3rd floor") {
		t.Errorf("the query %q carries line2 detail that no geocoder can anchor on", got)
	}
}

// The hash ignores changes that do not change the question, so re-typing an
// address in a different case does not spend a lookup.
func TestTheAddressHashIgnoresSpellingThatChangesNothing(t *testing.T) {
	if addressHash("Hauptstr. 1, Stuttgart") != addressHash("hauptstr. 1, STUTTGART") {
		t.Error("two spellings of one address hashed differently, so re-saving a form would spend a lookup")
	}
	if addressHash("Hauptstr. 1, Stuttgart") == addressHash("Hauptstr. 2, Stuttgart") {
		t.Error("two different addresses hashed alike, so a company that moved would never be re-resolved")
	}
}

// The place cache key collapses trivially different spellings, so one place is
// one entry rather than three — a cache that stored them apart would ask the
// provider three times for one place, which is what it exists to prevent.
func TestThePlaceCacheKeyIsOneEntryPerPlace(t *testing.T) {
	want := normalizePlaceQuery("Stuttgart")
	for _, spelling := range []string{" stuttgart ", "STUTTGART", "Stuttgart\n", "Stuttgart"} {
		if got := normalizePlaceQuery(spelling); got != want {
			t.Errorf("%q keyed as %q, want %q", spelling, got, want)
		}
	}
	if normalizePlaceQuery("Munich, Germany") == want {
		t.Error("two different places share a cache key")
	}
}

// Only an address column moving queues a lookup. Re-submitting a form with an
// unchanged address would otherwise spend fifteen seconds of a rate the whole
// installation shares.
func TestOnlyAMovedAddressQueuesALookup(t *testing.T) {
	if movedAddress(map[string]any{"display_name": "Acme GmbH", "industry": "logistics"}) {
		t.Error("an edit touching no address column queued a lookup")
	}
	if !movedAddress(map[string]any{"address_city": "Stuttgart"}) {
		t.Error("a changed city did not queue a lookup")
	}
	if movedAddress(map[string]any{}) {
		t.Error("a patch that changed nothing queued a lookup")
	}
}
