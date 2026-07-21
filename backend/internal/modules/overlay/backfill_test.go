// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
)

// fakeMirrorSink is an in-memory overlay.MirrorSink: it tracks every
// ingested record by external id (last write wins, the same
// upsert-by-key shape the real MirrorStore's ingestSQL enforces in
// Postgres), every upserted association, and one persisted backfill
// cursor per object class — enough to prove Backfill's paging, resume,
// and association-fetch behavior without a real Postgres.
type fakeMirrorSink struct {
	ingested         map[string]overlay.Record
	ingestCountByKey map[string]int // successful Ingest calls per external id — >1 means this key was re-ingested (safe, since Ingest is an idempotent upsert; the resume test below asserts this happens under a mid-page crash, and that the FINAL map still has exactly one entry per key)
	ingestCalls      int            // successful calls, total
	ingestAttempts   int            // successful + failed, for locating failAfter
	assocs           []overlay.Assoc
	cursors          map[string]string
	done             map[string]bool

	// failAfter, when non-zero, makes the failAfter'th ATTEMPTED call to
	// Ingest return an error instead of succeeding — the seam this test
	// uses to simulate a mid-backfill crash. It does not count toward
	// ingestCalls, which counts only successful ingests.
	failAfter int
}

func newFakeMirrorSink() *fakeMirrorSink {
	return &fakeMirrorSink{
		ingested:         make(map[string]overlay.Record),
		ingestCountByKey: make(map[string]int),
		cursors:          make(map[string]string),
		done:             make(map[string]bool),
	}
}

func (s *fakeMirrorSink) Ingest(_ context.Context, rec overlay.Record) error {
	s.ingestAttempts++
	if s.failAfter != 0 && s.ingestAttempts == s.failAfter {
		return fmt.Errorf("fakeMirrorSink: forced ingest failure at attempt %d", s.ingestAttempts)
	}
	s.ingestCalls++
	s.ingestCountByKey[rec.ExternalID]++
	s.ingested[rec.ExternalID] = rec
	return nil
}

func (s *fakeMirrorSink) UpsertAssoc(_ context.Context, a overlay.Assoc) error {
	s.assocs = append(s.assocs, a)
	return nil
}

func (s *fakeMirrorSink) LoadBackfillCursor(_ context.Context, objectClass string) (string, bool, error) {
	return s.cursors[objectClass], s.done[objectClass], nil
}

func (s *fakeMirrorSink) SaveBackfillCursor(_ context.Context, objectClass, cursor string, done bool) error {
	s.cursors[objectClass] = cursor
	s.done[objectClass] = done
	return nil
}

// seedOrganizations seeds n fake companies records under the incumbent
// class "companies", each stamped with the CANONICAL ObjectClass
// "organization" — simulating what a real adapter's mapRecord already
// translated (the SEAM RULE backfill.go's doc comment states: Backfill
// drives the seam with the incumbent class name, but the Records it
// gets back already carry the canonical type).
func seedOrganizations(f *fake.Adapter, n int) {
	for i := 0; i < n; i++ {
		rec := fake.Rec(fmt.Sprint(i), map[string]any{"display_name": fmt.Sprintf("Org %d", i)})
		rec.ObjectClass = "organization"
		f.Seed("companies", rec)
	}
}

// TestBackfillHydratesAllRecords proves Backfill pages through every
// record the fake incumbent holds (250 across 3 pages of the fake's
// fixed 100-record page size) and ingests each one, canonical-keyed.
func TestBackfillHydratesAllRecords(t *testing.T) {
	f := fake.New()
	seedOrganizations(f, 250)
	sink := newFakeMirrorSink()

	if err := overlay.Backfill(context.Background(), f, sink, "companies"); err != nil {
		t.Fatalf("Backfill returned an error: %v", err)
	}

	if len(sink.ingested) != 250 {
		t.Fatalf("ingested %d/250 records", len(sink.ingested))
	}
	for i := 0; i < 250; i++ {
		rec, ok := sink.ingested[fmt.Sprint(i)]
		if !ok {
			t.Fatalf("record %d never ingested", i)
		}
		if rec.ObjectClass != "organization" {
			t.Errorf("record %d ObjectClass = %q, want the canonical %q (the SEAM RULE)", i, rec.ObjectClass, "organization")
		}
	}
	if cursor, done := sink.cursors["companies"], sink.done["companies"]; cursor != "" || !done {
		t.Errorf("final cursor = (%q, done=%v), want (\"\", true) — a converged backfill", cursor, done)
	}
}

// TestBackfillResumesFromMidPageCrashConvergesWithoutDuplicates proves
// the resumability contract in its real worst case: checkpointing is
// PAGE-grained (Backfill saves the cursor only after a whole page's
// records have landed), so a crash MID-PAGE — after some, but not all,
// of a page's records already landed — means the resume re-lists and
// re-ingests that ENTIRE page, including the records that already
// succeeded before the crash. That re-ingestion is the case the design
// relies on the mirror's idempotent upsert to absorb: a boundary-only
// crash (failing on exactly the first record of a page) would never
// exercise it, since a fresh page has nothing yet to re-ingest.
//
// failAfter=150 lands the forced failure on the 50th record of page 2
// (page 1 is attempts 1-100; page 2 is attempts 101-200) — genuinely
// mid-page, not at either edge. The proof: after the crash + resume,
// (a) the final ingested set converges to exactly the 250 seeded
// records, each with correct content, and (b) some external ids were
// ingested MORE THAN ONCE in net Ingest-call terms (proving the overlap
// actually happened, not just that this test got lucky with page-aligned
// numbers) — yet the final map still holds exactly one entry per id, the
// same "last write wins" idempotency the real MirrorStore's ingestSQL
// upsert enforces in Postgres.
func TestBackfillResumesFromMidPageCrashConvergesWithoutDuplicates(t *testing.T) {
	f := fake.New()
	seedOrganizations(f, 250)
	sink := newFakeMirrorSink()
	sink.failAfter = 150

	err := overlay.Backfill(context.Background(), f, sink, "companies")
	if err == nil {
		t.Fatal("want an error from the forced mid-backfill failure, got nil")
	}
	if got := len(sink.ingested); got != 149 {
		t.Fatalf("after the failed run: ingested %d records, want exactly 149 (page 1's 100 + page 2's first 49)", got)
	}
	cursor, done := sink.cursors["companies"], sink.done["companies"]
	if cursor != "100" || done {
		t.Fatalf("after the failed run: cursor = (%q, done=%v), want (\"100\", done=false) — only page 1's checkpoint landed", cursor, done)
	}

	// Restart: same sink (the persisted cursor), failure disarmed.
	sink.failAfter = 0
	if err := overlay.Backfill(context.Background(), f, sink, "companies"); err != nil {
		t.Fatalf("the resumed Backfill returned an error: %v", err)
	}

	// Convergence: exactly 250 distinct rows, every one with the content
	// the incumbent actually holds for it — proves the resumed page-2
	// re-fetch (which re-ingests 49 already-landed records) never
	// produces a duplicate or a corrupted final value.
	if got := len(sink.ingested); got != 250 {
		t.Fatalf("after resuming: ingested %d/250 distinct records", got)
	}
	for i := 0; i < 250; i++ {
		id := fmt.Sprint(i)
		rec, ok := sink.ingested[id]
		if !ok {
			t.Fatalf("record %s never ingested", id)
		}
		if rec.ObjectClass != "organization" {
			t.Errorf("record %s ObjectClass = %q, want organization", id, rec.ObjectClass)
		}
		if want := fmt.Sprintf("Org %d", i); rec.Fields["display_name"] != want {
			t.Errorf("record %s display_name = %v, want %q", id, rec.Fields["display_name"], want)
		}
	}

	// The re-ingested overlap actually happened: page 2's first 49
	// records (ids "100".."148") were ingested once before the crash and
	// once again on resume — net count 2 — which is exactly what makes
	// this a mid-page proof rather than a boundary one.
	reIngested := 0
	for id, n := range sink.ingestCountByKey {
		if n > 1 {
			reIngested++
			if n != 2 {
				t.Errorf("external id %s was ingested %d times, want at most 2 (once before the crash, once on resume)", id, n)
			}
		}
	}
	if reIngested != 49 {
		t.Fatalf("49 records (page 2's pre-crash portion) should have been re-ingested on resume; got %d", reIngested)
	}

	if cursor, done := sink.cursors["companies"], sink.done["companies"]; cursor != "" || !done {
		t.Errorf("final cursor = (%q, done=%v), want (\"\", true) — a converged backfill", cursor, done)
	}
}

// TestBackfillIsANoOpOnceConverged proves a Backfill call against an
// already-converged cursor (done=true) re-lists nothing and re-ingests
// nothing — a second call is a cheap no-op, not a redundant full re-sync.
func TestBackfillIsANoOpOnceConverged(t *testing.T) {
	f := fake.New()
	seedOrganizations(f, 250)
	sink := newFakeMirrorSink()

	if err := overlay.Backfill(context.Background(), f, sink, "companies"); err != nil {
		t.Fatalf("first Backfill returned an error: %v", err)
	}
	callsAfterFirstRun := sink.ingestCalls

	if err := overlay.Backfill(context.Background(), f, sink, "companies"); err != nil {
		t.Fatalf("second Backfill returned an error: %v", err)
	}

	if sink.ingestCalls != callsAfterFirstRun {
		t.Errorf("Ingest was called again on a converged backfill: %d calls before, %d after", callsAfterFirstRun, sink.ingestCalls)
	}
}

// TestBackfillFetchesDealCompanyAssociations proves Backfill fetches and
// upserts the deals→companies association design.md §9 names
// ("assoc→company→organization_id") for every ingested deal record, and
// does so with the INCUMBENT class names on both sides of the
// Associations call (the SEAM RULE), not the canonical "deal"/
// "organization" names the ingested Records themselves carry.
func TestBackfillFetchesDealCompanyAssociations(t *testing.T) {
	f := fake.New()
	for _, id := range []string{"d1", "d2"} {
		rec := fake.Rec(id, map[string]any{"name": "Deal " + id})
		rec.ObjectClass = "deal"
		f.Seed("deals", rec)
	}
	f.SeedAssoc("deals", "d1", "companies", overlay.Assoc{
		FromType: "deals", FromID: "d1", ToType: "companies", ToID: "c1", TypeID: 5, Category: "HUBSPOT_DEFINED",
	})
	f.SeedAssoc("deals", "d2", "companies", overlay.Assoc{
		FromType: "deals", FromID: "d2", ToType: "companies", ToID: "c2", TypeID: 5, Category: "HUBSPOT_DEFINED",
	})

	sink := newFakeMirrorSink()
	if err := overlay.Backfill(context.Background(), f, sink, "deals"); err != nil {
		t.Fatalf("Backfill returned an error: %v", err)
	}

	if len(sink.assocs) != 2 {
		t.Fatalf("upserted %d associations, want 2", len(sink.assocs))
	}
	want := map[string]string{"d1": "c1", "d2": "c2"}
	for _, a := range sink.assocs {
		if a.ToID != want[a.FromID] {
			t.Errorf("association %s -> %s, want -> %s", a.FromID, a.ToID, want[a.FromID])
		}
	}
}
