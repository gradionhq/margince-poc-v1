// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The AC-OV-3/AC-OV-7 half of the AC-OV acceptance suite
// (overlay_acceptance_test.go carries the suite's own scope doc): the two
// criteria that turn on the OVB meter — which lane a read spends on, and what
// a force-fresh read does once the window sheds.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// acceptanceIncumbent is the incumbent name this suite's meter-based
// proofs (AC-OV-3/7) charge against — the fake adapter's own Name().
const acceptanceIncumbent = "fake"

// acceptanceBudgetMeter builds a Redis-backed OVB meter with a small,
// fast-to-exhaust REST budget (cap 10, warn at 5, shed at 8) for the fake
// incumbent — the deterministic thresholds AC-OV-3/7 assert against. The
// raw-Redis dependency lives in budgettest (platform tier), never in this
// compose suite.
func acceptanceBudgetMeter(t *testing.T) *overlaybudget.Meter {
	t.Helper()
	return budgettest.Meter(t, budgettest.SmallConfig(acceptanceIncumbent))
}

// contactsTranslator is a fixed canonical->incumbent class translator
// scoped to this suite's one fixtured mapping (person -> contacts) — the
// same role hubspot.IncumbentClassesFor plays in production, stood in here
// so these tests never import the hubspot subpackage, which is itself what
// the AC-OV-1 gate proves of every package above the seam.
func contactsTranslator(canonical string) ([]string, bool) {
	if canonical == "person" {
		return []string{overlay.IncumbentClassContacts}, true
	}
	return nil, false
}

// TestAcceptance_AC_OV_3_MirrorReadMeetsBudget proves design.md's AC-OV-3
// via deterministic classification rather than a wall-clock p95
// assertion: OVA-PARAM-9 (the overlay-perf-addendum's own numeric
// latency budgets) is pinned upstream as "unset — open"
// (subsystems/overlay-augmentation.md), so asserting a specific
// millisecond threshold here would either fabricate an unpinned number
// or flake on a loaded CI runner (T11 bars real-clock-dependent
// assertions). Instead this proves the actual, load-bearing DISTINCTION
// AC-OV-3 requires: a mirror-served Provider.Read never touches the OVB
// meter at all — it rides the same always-available budget a native SoR
// read does — while a force-fresh read (FreshnessReader.Read, reached
// through Provider.Freshness) spends exactly one unit on the DEDICATED
// force_fresh lane, the overlay-perf-addendum bucket, every time. That
// bucketing is exactly what a perf harness's classification step reads
// to route the two kinds of read into different budgets; this test
// proves the classification is correct without needing a timer at all.
func TestAcceptance_AC_OV_3_MirrorReadMeetsBudget(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(ws, actorID)

	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	mirrorTime := time.Now().UTC().Add(-time.Hour)
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: "person", ExternalID: "100214862066",
		Fields: map[string]any{"firstname": "Budget"}, ModifiedAt: mirrorTime, OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the mirror fixture: %v", err)
	}

	basicProvider := overlay.NewProvider(mirror, nil)
	searchRes, err := basicProvider.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10})
	if err != nil || len(searchRes.Records) != 1 {
		t.Fatalf("resolving the fixture's own ref: err=%v records=%d", err, len(searchRes.Records))
	}
	ref := searchRes.Records[0].Ref

	fakeInc := fake.New()
	liveRec := fake.Rec("100214862066", map[string]any{"firstname": "Live"})
	liveRec.ObjectClass = overlay.IncumbentClassContacts
	fakeInc.Seed(overlay.IncumbentClassContacts, liveRec)

	meter := acceptanceBudgetMeter(t)
	ff := overlay.NewFreshnessReader(func(context.Context) (overlay.Incumbent, error) { return fakeInc, nil }, mirror, meter, contactsTranslator)
	fullProvider := overlay.NewProvider(mirror, ff)

	before := meter.Snapshot(ctx, acceptanceIncumbent)
	if _, err := fullProvider.Read(ctx, ref); err != nil {
		t.Fatalf("mirror-served Read: %v", err)
	}
	afterMirrorRead := meter.Snapshot(ctx, acceptanceIncumbent)
	if afterMirrorRead.Consumed != before.Consumed {
		t.Fatalf("a mirror-served Read spent %d OVB units (before=%d) — it must ride the same always-available native-mode read budget, never the overlay-perf-addendum meter", afterMirrorRead.Consumed, before.Consumed)
	}

	freshInfo, err := fullProvider.Freshness(ctx, ref)
	if err != nil {
		t.Fatalf("force-fresh Freshness: %v", err)
	}
	if !freshInfo.Authoritative {
		t.Fatal("a force-fresh read under threshold must reach the live incumbent and answer Authoritative:true")
	}
	afterForceFresh := meter.Snapshot(ctx, acceptanceIncumbent)
	if afterForceFresh.Consumed != before.Consumed+1 {
		t.Fatalf("force-fresh Consumed = %d, want %d (exactly one force_fresh-lane spend) — the addendum bucket must record it, distinctly from the mirror read above", afterForceFresh.Consumed, before.Consumed+1)
	}
}

// TestAcceptance_AC_OV_7_ForceFreshDegrades proves design.md's AC-OV-7
// (OVA-EVT-3): once the OVB meter reports the shed band, a force-fresh
// read degrades to mirror-with-staleness (Authoritative:false, zero
// additional quota spent — proven by the meter's own Consumed count
// staying unchanged, the only way FreshnessReader.Read could ever reach
// the live incumbent) and emits mirror.budget_degraded on the bus — never
// silently. Driven through compose.Dispatcher (the real production
// seam every Freshness call rides) with the fake incumbent as this
// task's mandated concurrent mutator, mirroring
// freshness_integration_test.go's module-level proof one layer up the
// composed stack.
func TestAcceptance_AC_OV_7_ForceFreshDegrades(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(ws, actorID)

	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	mirrorTime := time.Now().UTC().Add(-time.Hour)
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: "person", ExternalID: "100214862077",
		Fields: map[string]any{"firstname": "Shed"}, ModifiedAt: mirrorTime, OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the mirror fixture: %v", err)
	}

	basicProvider := overlay.NewProvider(mirror, nil)
	searchRes, err := basicProvider.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10})
	if err != nil || len(searchRes.Records) != 1 {
		t.Fatalf("resolving the fixture's own ref: err=%v records=%d", err, len(searchRes.Records))
	}
	ref := searchRes.Records[0].Ref

	fakeInc := fake.New()
	liveRec := fake.Rec("100214862077", map[string]any{"firstname": "Live"})
	liveRec.ObjectClass = overlay.IncumbentClassContacts
	fakeInc.Seed(overlay.IncumbentClassContacts, liveRec)

	meter := acceptanceBudgetMeter(t)
	// Push the window to shed (limit 10, shed at 8) via the POLLER lane —
	// proving Band is a total across lanes, never reachable by a
	// force-fresh spend alone.
	if err := meter.ConsumeREST(ctx, acceptanceIncumbent, overlaybudget.SourcePoller, 8); err != nil {
		t.Fatalf("pre-loading the poller lane to shed: %v", err)
	}
	if got := meter.BandREST(ctx, acceptanceIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("meter.Band = %q after loading to the shed threshold, want %q", got, overlaybudget.BandShed)
	}

	ff := overlay.NewFreshnessReader(func(context.Context) (overlay.Incumbent, error) { return fakeInc, nil }, mirror, meter, contactsTranslator)
	overlayProvider := overlay.NewProvider(mirror, ff)
	d := compose.NewDispatcher(compose.NewProvider(e.Pool), overlayProvider, e.Pool)

	info, err := d.Freshness(ctx, ref)
	if err != nil {
		t.Fatalf("dispatched Freshness under the shed band: %v", err)
	}
	if info.Authoritative {
		t.Fatal("under the shed band, force-fresh must degrade to the mirror — never Authoritative:true")
	}

	snap := meter.Snapshot(ctx, acceptanceIncumbent)
	if snap.Consumed != 8 {
		t.Fatalf("meter Consumed = %d, want unchanged at 8 — the shed path must spend nothing on the force_fresh lane (proof the live incumbent was never reached)", snap.Consumed)
	}

	var eventCount int
	if err := e.Pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'mirror.budget_degraded' AND envelope->>'workspace_id' = $1`,
		ws.String(),
	).Scan(&eventCount); err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("mirror.budget_degraded outbox rows = %d, want exactly 1", eventCount)
	}
}
