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
// records seen, guarding against a cursor that never terminates.
func drainBackfill(t *testing.T, inc overlay.Incumbent) int {
	t.Helper()
	seen, cursor := 0, ""
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatal("backfill cursor never terminated — the cap encoding is not converging")
		}
		p, err := inc.Backfill(context.Background(), overlay.IncumbentClassContacts, cursor)
		if err != nil {
			t.Fatalf("Backfill: %v", err)
		}
		seen += len(p.Records)
		if p.NextCursor == "" {
			return seen
		}
		cursor = p.NextCursor
	}
}

// TestCappedIncumbentBoundsBackfillWithinAPage proves the cap truncates a
// single page when the limit is below the incumbent's page size.
func TestCappedIncumbentBoundsBackfillWithinAPage(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	if got := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 50}); got != 50 {
		t.Fatalf("capped backfill saw %d records, want exactly 50", got)
	}
}

// TestCappedIncumbentBoundsBackfillAcrossPages proves the running count
// encoded into the cursor carries across pages, so a limit larger than one
// page still stops at exactly limit (restart-safe, stateless).
func TestCappedIncumbentBoundsBackfillAcrossPages(t *testing.T) {
	f := fake.New()
	seedContacts(f, 250)
	if got := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 150}); got != 150 {
		t.Fatalf("capped backfill saw %d records, want exactly 150 (spanning two pages)", got)
	}
}

// TestCappedIncumbentLimitAboveTotalSeesEverything proves the cap never
// drops records when the portal is smaller than the limit.
func TestCappedIncumbentLimitAboveTotalSeesEverything(t *testing.T) {
	f := fake.New()
	seedContacts(f, 40)
	if got := drainBackfill(t, cappedIncumbent{Incumbent: f, limit: 1000}); got != 40 {
		t.Fatalf("capped backfill saw %d records, want all 40", got)
	}
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
