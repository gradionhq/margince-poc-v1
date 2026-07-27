// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
)

var capFixedTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func seedContacts(f *fake.Adapter, n int) {
	for i := 0; i < n; i++ {
		rec := fake.Rec(strconv.Itoa(i), map[string]any{"n": strconv.Itoa(i)})
		rec.ModifiedAt = capFixedTime
		f.Seed(overlay.IncumbentClassContacts, rec)
	}
}

// drainBackfill pages inc.Backfill to completion and returns the total
// records seen and whether ANY page came back Truncated (sticky, mirroring
// how Backfill itself, backfill.go, ORs the flag across pages), guarding
// against a cursor that never terminates.
func drainBackfill(t *testing.T, inc overlay.Incumbent) (seen int, truncated bool) {
	t.Helper()
	cursor := ""
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatal("backfill cursor never terminated — the cap encoding is not converging")
		}
		p, err := inc.Backfill(context.Background(), overlay.IncumbentClassContacts, cursor)
		if err != nil {
			t.Fatalf("Backfill: %v", err)
		}
		seen += len(p.Records)
		truncated = truncated || p.Truncated
		if p.NextCursor == "" {
			return seen, truncated
		}
		cursor = p.NextCursor
	}
}

// TestCappedIncumbentBoundsBackfillWithinAPage proves the cap truncates a
// single page when the limit is below the incumbent's page size, and flags
// the run Truncated — the incumbent's own list still had more.
func TestCappedIncumbentBoundsBackfillWithinAPage(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 50})
	if got != 50 {
		t.Fatalf("capped backfill saw %d records, want exactly 50", got)
	}
	if !truncated {
		t.Error("want Truncated=true — the cap declined 200 records the incumbent still has")
	}
}

// TestCappedIncumbentBoundsBackfillAcrossPages proves the running count
// encoded into the cursor carries across pages, so a limit larger than one
// page still stops at exactly limit (restart-safe, stateless) and is
// flagged Truncated.
func TestCappedIncumbentBoundsBackfillAcrossPages(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 150})
	if got != 150 {
		t.Fatalf("capped backfill saw %d records, want exactly 150 (spanning two pages)", got)
	}
	if !truncated {
		t.Error("want Truncated=true — the cap declined 100 records the incumbent still has")
	}
}

// TestCappedIncumbentLimitAboveTotalSeesEverything proves the cap never
// drops records when the portal is smaller than the limit, and — the
// honesty half — never flags a genuine convergence as Truncated.
func TestCappedIncumbentLimitAboveTotalSeesEverything(t *testing.T) {
	f := fake.New()
	seedContacts(f, 40)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 1000})
	if got != 40 {
		t.Fatalf("capped backfill saw %d records, want all 40", got)
	}
	if truncated {
		t.Error("want Truncated=false — the incumbent's own list ended, the cap never actually declined anything")
	}
}

// TestCappedIncumbentExactlyAtTheLimitIsNotTruncated proves the boundary
// case Truncated must get right: a portal whose size lands EXACTLY on the
// cap converges naturally — the incumbent's own list ends at the same
// record the cap would have stopped at — and must not be reported
// Truncated, or a portal that merely happens to match the cap would report
// backfillComplete=false forever for no honest reason.
func TestCappedIncumbentExactlyAtTheLimitIsNotTruncated(t *testing.T) {
	f := fake.New()
	seedContacts(f, 40)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 40})
	if got != 40 {
		t.Fatalf("capped backfill saw %d records, want all 40", got)
	}
	if truncated {
		t.Error("want Truncated=false — the portal's own list ended exactly at the cap, nothing was declined")
	}
}

// TestCappedIncumbentOneRecordOverTheLimitIsTruncated is the same boundary
// from the other side: one record more than the cap must still be declined
// and flagged, proving the exact-match case above isn't just an
// off-by-one that always reads false.
func TestCappedIncumbentOneRecordOverTheLimitIsTruncated(t *testing.T) {
	f := fake.New()
	seedContacts(f, 41)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 40})
	if got != 40 {
		t.Fatalf("capped backfill saw %d records, want exactly 40", got)
	}
	if !truncated {
		t.Error("want Truncated=true — one record was declined")
	}
}

// TestCappedIncumbentAtAPageBoundaryStopsAndFlagsTruncated proves the cap
// gets the page-boundary case right: a limit that consumes a whole page
// exactly leaves the incumbent handing out a NextCursor — its own signal
// that the list has more. Reading that as the end of the portal would report
// a declined class complete, so the cap must stop AND flag the run
// Truncated. This is the path a cap set to a multiple of the incumbent's
// page size always takes.
func TestCappedIncumbentAtAPageBoundaryStopsAndFlagsTruncated(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	got, truncated := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 100})
	if got != 100 {
		t.Fatalf("capped backfill saw %d records, want exactly 100 (one whole page)", got)
	}
	if !truncated {
		t.Error("want Truncated=true — the incumbent's own cursor says 150 records remain, the cap declined them")
	}
}

// TestCappedIncumbentResumedAtTheCapConvergesWithoutListing proves the
// decorator is restart-safe on its own cursor: handed a cursor whose encoded
// count has already reached the cap — what a sweep resuming a mid-flight
// capped backfill presents — it converges immediately, reaching the
// incumbent for nothing. Listing again would spend incumbent quota on
// records the cap is going to decline anyway, and returning Truncated=false
// here would report the declined class complete.
func TestCappedIncumbentResumedAtTheCapConvergesWithoutListing(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	spy := &listCountingIncumbent{Incumbent: f}

	page, err := cappedIncumbent{Incumbent: spy, limit: 50}.
		Backfill(context.Background(), overlay.IncumbentClassContacts, "50"+cappedCursorSep+"100")
	if err != nil {
		t.Fatalf("Backfill over a cursor resumed at the cap: %v", err)
	}
	if len(page.Records) != 0 || page.NextCursor != "" {
		t.Fatalf("resumed at the cap = %d record(s), NextCursor %q; want an empty terminal page",
			len(page.Records), page.NextCursor)
	}
	if !page.Truncated {
		t.Error("want Truncated=true — the incumbent's list was never exhausted, only declined")
	}
	if spy.lists != 0 {
		t.Errorf("a cursor already at the cap must not list the incumbent, got %d Backfill call(s)", spy.lists)
	}
}

// listCountingIncumbent counts Backfill calls so a test can assert the cap
// converged WITHOUT reaching the incumbent for another page.
type listCountingIncumbent struct {
	overlay.Incumbent
	lists int
}

func (l *listCountingIncumbent) Backfill(ctx context.Context, objectClass, cursor string) (overlay.Page, error) {
	l.lists++
	return l.Incumbent.Backfill(ctx, objectClass, cursor)
}

// TestCappedIncumbentDoesNotCapModified proves continuous sync stays
// uncapped: only Backfill is bounded, Modified passes straight through.
func TestCappedIncumbentDoesNotCapModified(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	capped := cappedIncumbent{Incumbent: f, limit: 10}
	seen, cursor := 0, ""
	for {
		p, err := capped.Modified(context.Background(), overlay.IncumbentClassContacts, capFixedTime.Add(-time.Hour), cursor)
		if err != nil {
			t.Fatalf("Modified: %v", err)
		}
		seen += len(p.Records)
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if seen != 250 {
		t.Fatalf("Modified saw %d records, want all 250 (modified sweeps are never capped)", seen)
	}
}

// TestOverlayIncumbentFactoryZeroLimitIsUncapped proves the factory does
// not wrap the adapter when no cap is configured.
func TestOverlayIncumbentFactoryZeroLimitIsUncapped(t *testing.T) {
	if _, ok := any(overlayIncumbentFactory(0)("us1", "tok")).(cappedIncumbent); ok {
		t.Fatal("overlayIncumbentFactory(0) must return an uncapped adapter, got a cappedIncumbent")
	}
	if _, ok := any(overlayIncumbentFactory(25)("us1", "tok")).(cappedIncumbent); !ok {
		t.Fatal("overlayIncumbentFactory(25) must return a cappedIncumbent")
	}
}
