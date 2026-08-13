// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// "Recently updated" is a question a user asks of the records, not of the
// sweep. A mirrored record's updated_at must therefore be the incumbent's own
// last-modified instant, which the mapping lands under the canonical
// last_synced_at key from its Baseline property — NOT Record.Freshness.
// LastSyncedAt, the mirror row's ingest time, which every record in a workspace
// shares after one sweep. The two are one word apart and mean opposite things,
// so each entity is held to the canonical one here.
//
// The records are seeded through the REAL mapping for their incumbent class, so
// what is under test is the payload production writes: a hand-built canonical
// fixture would prove only that the wire reads its own author.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// wireIncumbentModifiedAt is the incumbent's own last-modified instant in the
// fixtures below, deliberately far from wireSyncedAt so a wire reading the
// mirror's ingest time cannot pass by coincidence.
var wireIncumbentModifiedAt = time.Date(2026, 5, 13, 6, 44, 38, 727000000, time.UTC)

// wireIncumbentModified is that instant in HubSpot's own wire spelling.
const wireIncumbentModified = "2026-05-13T06:44:38.727Z"

func TestOverlayWireDealReportsTheIncumbentsOwnStamps(t *testing.T) {
	canonical := canonicalFromMapping(t, "deals", "the deal fixture", map[string]any{
		"hs_object_id":        "77123456789",
		"dealname":            "Overlay renewal",
		"hs_lastmodifieddate": wireIncumbentModified,
	})
	deal, err := overlayWireDeal(wireCtx(), wireRecord(t, datasource.EntityDeal, canonical))
	if err != nil {
		t.Fatalf("overlayWireDeal: %v", err)
	}
	checkMirroredStamps(t, "deal", canonical, deal.CreatedAt, deal.UpdatedAt)
}

func TestOverlayWireLeadReportsTheIncumbentsOwnStamps(t *testing.T) {
	canonical := canonicalFromMapping(t, "leads", "the lead fixture", map[string]any{
		"hs_object_id":        "88123456789",
		"hs_lead_name":        "Ada Overlay",
		"hs_lastmodifieddate": wireIncumbentModified,
	})
	lead, err := overlayWireLead(wireCtx(), wireRecord(t, datasource.EntityLead, canonical))
	if err != nil {
		t.Fatalf("overlayWireLead: %v", err)
	}
	checkMirroredStamps(t, "lead", canonical, lead.CreatedAt, lead.UpdatedAt)
}

func TestOverlayWireActivityReportsTheIncumbentsOwnStamps(t *testing.T) {
	canonical := canonicalFromMapping(t, "calls", "the call fixture", map[string]any{
		"hs_object_id":        "99123456789",
		"hs_call_title":       "Intro call",
		"hs_lastmodifieddate": wireIncumbentModified,
	})
	act, err := overlayWireActivity(wireCtx(), wireRecord(t, datasource.EntityActivity, canonical))
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	checkMirroredStamps(t, "activity", canonical, act.CreatedAt, act.UpdatedAt)
}

// checkMirroredStamps holds one assembled record's two timestamps to what the
// canonical payload actually carries. created_at is asserted against the
// mapping rather than against a remembered fact: none of these three classes
// maps an incumbent create instant today, so the sync instant is the honest
// answer — and the day one of them does, this fails and says to read it instead
// of quietly reporting the sweep's time for a value the mirror now holds.
func checkMirroredStamps(t *testing.T, entity string, canonical map[string]any, created, updated time.Time) {
	t.Helper()
	if !updated.Equal(wireIncumbentModifiedAt) {
		t.Errorf("%s updated_at = %s, want the incumbent's own last-modified %s. The mirror row's ingest instant "+
			"reports every record in the workspace as modified when the sweep ran, so \"recently updated\" answers "+
			"the same for all of them; read the canonical %q key the mapping's Baseline lands.",
			entity, updated, wireIncumbentModifiedAt, overlayCanonicalLastModified)
	}
	if _, landed := canonical["created_at"]; landed {
		t.Fatalf("the %s mapping now lands a created_at canonical key, so created_at must read it rather than the "+
			"mirror's sync instant", entity)
	}
	if !created.Equal(wireSyncedAt) {
		t.Errorf("%s created_at = %s, want the mirror's own sync instant %s: this class's mapping lands no "+
			"incumbent create stamp, and the sync instant is the only time the mirror can honestly claim",
			entity, created, wireSyncedAt)
	}
}
