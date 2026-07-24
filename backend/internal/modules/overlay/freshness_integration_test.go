// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// FreshnessReader's real integration test needs a real, migrated
// Postgres because the shed path's emitBudgetDegraded writes through
// storekit.LogSystem + storekit.Emit (event_outbox, system_log) inside
// database.WithWorkspaceTx, and the live path reads back through
// MirrorStore.Get's visibility-joined query — neither has a fake-DB
// substitute in this package.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// stubIncumbent is a single-record Incumbent fixture, local to this
// test file rather than the overlay/fake package: that package imports
// overlay (to implement overlay.Incumbent), and this file — package
// overlay itself, needed for its unexported helpers (testWorkspaceCtx,
// externalIDToUUID, noOwnerEmails) — cannot import it back without an
// import cycle. Only Get is ever exercised by FreshnessReader.Read; the
// other Incumbent methods (Backfill/Modified/Deletions/Associations/
// OwnerEmail/Owners + the Create/Update/Archive write seam) are declared
// unsupported so a test that accidentally calls them fails loudly rather
// than returning a fabricated answer.
//
// stubIncumbent.objectClass is deliberately the INCUMBENT class (e.g.
// "contacts"), never the canonical Margince name (e.g. "person") — this
// is exactly the asymmetry a real adapter enforces (hubspot.Adapter.Get
// rejects any class with no declared mapping) and this stub's Get
// checks it strictly, so a test seeding/calling this stub with the
// canonical name by mistake fails loudly instead of quietly matching
// (a generic "keyed by whatever string it's given" double would hide
// exactly the bug FreshnessReader.Read's translation step exists to
// prevent — see freshness.go's toIncumbentClass doc). Fakes for the
// poller lane should hold the same discipline.
type stubIncumbent struct {
	objectClass, externalID string
	rec                     Record
	calls                   int
	getErr                  error // when set, Get returns it (a live-read failure)
}

func (s *stubIncumbent) Name() string { return "stub" }
func (s *stubIncumbent) Backfill(context.Context, string, string) (Page, error) {
	return Page{}, fmt.Errorf("stubIncumbent: Backfill is not fixtured")
}

func (s *stubIncumbent) Modified(context.Context, string, time.Time, string) (Page, error) {
	return Page{}, fmt.Errorf("stubIncumbent: Modified is not fixtured")
}

func (s *stubIncumbent) Deletions(context.Context, string, time.Time, string) (DeletionPage, error) {
	return DeletionPage{}, fmt.Errorf("stubIncumbent: Deletions is not fixtured")
}

func (s *stubIncumbent) Associations(context.Context, string, string, string) ([]Assoc, error) {
	return nil, fmt.Errorf("stubIncumbent: Associations is not fixtured")
}

func (s *stubIncumbent) OwnerEmail(context.Context, string) (string, error) {
	return "", fmt.Errorf("stubIncumbent: OwnerEmail is not fixtured")
}

func (s *stubIncumbent) Owners(context.Context) ([]OwnerRef, error) { return nil, nil }
func (s *stubIncumbent) Create(context.Context, string, map[string]any) (WriteResult, error) {
	return WriteResult{}, fmt.Errorf("stubIncumbent: Create is not fixtured")
}

func (s *stubIncumbent) Update(context.Context, string, string, map[string]any, time.Time) (WriteResult, error) {
	return WriteResult{}, fmt.Errorf("stubIncumbent: Update is not fixtured")
}

func (s *stubIncumbent) Archive(context.Context, string, string, time.Time) error {
	return fmt.Errorf("stubIncumbent: Archive is not fixtured")
}

// Get answers the one fixtured record — proving Read reached the LIVE
// incumbent, not the mirror — and counts the call so the shed-path test
// can assert this method was never reached at all.
func (s *stubIncumbent) Get(_ context.Context, objectClass, externalID string) (Record, error) {
	s.calls++
	if s.getErr != nil {
		return Record{}, s.getErr
	}
	if objectClass != s.objectClass || externalID != s.externalID {
		return Record{}, fmt.Errorf("stubIncumbent: no record fixtured for %s/%s", objectClass, externalID)
	}
	return s.rec, nil
}

// canonicalClass/incumbentClass are this file's fixed stand-ins for the
// real seam's two distinct namespaces — canonical is what the mirror
// and datasource.EntityRef.Type carry ("person", the Margince entity
// name); incumbent is what Incumbent.Get takes as input ("contacts",
// HubSpot's own object class, mirroring hubspot.Mapping's real
// contactsMapping.Source/.Target pair). Using two DIFFERENT strings
// here (not the same name for both) is what makes the tests below
// actually exercise FreshnessReader's translation step rather than
// pass by coincidence.
const (
	canonicalClass = "person"
	incumbentClass = "contacts"
)

// translatorFor returns a canonical->incumbent translator that answers
// ok=true only for canonicalClass — a real hubspot.IncumbentClassesFor
// stand-in scoped to this file's one fixtured mapping, plus miss=true
// on request so the third test below can prove the honest-degrade path
// a genuine translator miss takes.
func translatorFor(miss bool) func(string) ([]string, bool) {
	return func(canonical string) ([]string, bool) {
		if miss || canonical != canonicalClass {
			return nil, false
		}
		return []string{incumbentClass}, true
	}
}

// seedMirrorAndLiveFixture ingests one mirror row under canonicalClass
// (mirrorTime) and returns a stubIncumbent fixtured under
// incumbentClass with a DIFFERENT, later ModifiedAt (liveTime) for the
// SAME external id — so a test can tell, from the returned
// FreshnessInfo.LastSyncedAt alone, whether Read served the mirror or
// the live incumbent, AND (via stubIncumbent.objectClass's strict
// match) whether Read reached it through the incumbent class or
// mistakenly through the canonical one.
func seedMirrorAndLiveFixture(ctx context.Context, t *testing.T, ms *MirrorStore, externalID string, mirrorTime, liveTime time.Time) *stubIncumbent {
	t.Helper()
	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("testWorkspaceCtx did not bind an actor")
	}
	userA := ids.From[ids.UserKind](actor.UserID)
	if err := ms.UpsertUserMap(ctx, userA, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := ms.Ingest(ctx, Record{
		ObjectClass:     canonicalClass,
		ExternalID:      externalID,
		Fields:          map[string]any{"firstname": "Mirror"},
		ModifiedAt:      mirrorTime,
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the mirror fixture: %v", err)
	}
	return &stubIncumbent{
		objectClass: incumbentClass,
		externalID:  externalID,
		rec: Record{
			ObjectClass: incumbentClass,
			ExternalID:  externalID,
			Fields:      map[string]any{"firstname": "Live"},
			ModifiedAt:  liveTime,
		},
	}
}

// TestFreshnessReaderUnderThresholdReadsLiveAndSpends proves the S2
// "happy path" (design.md §4.5): under the meter's shed band, Read
// translates the canonical entity type to its incumbent class BEFORE
// calling inc.Get (stubIncumbent.Get strictly checks it was called with
// incumbentClass, never canonicalClass — see its type doc), does that
// real translated inc.Get, spends exactly 1 unit on the force_fresh
// lane, and answers Authoritative:true with the LIVE incumbent's
// ModifiedAt — the one path this port ever answers Authoritative:true
// on for an overlay-mode workspace.
func TestFreshnessReaderUnderThresholdReadsLiveAndSpends(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const externalID = "100214862042"
	mirrorTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	liveTime := mirrorTime.Add(time.Hour)

	inc := seedMirrorAndLiveFixture(ctx, t, ms, externalID, mirrorTime, liveTime)

	meter := testBudgetMeter(t, "stub")
	fr := NewFreshnessReader(func(context.Context) (Incumbent, error) { return inc, nil }, ms, meter, translatorFor(false))

	id, err := externalIDToUUID(externalID)
	if err != nil {
		t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
	}
	ref := datasource.EntityRef{Type: datasource.EntityType(canonicalClass), ID: id}

	info, err := fr.Read(ctx, ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !info.Authoritative {
		t.Fatal("under threshold, Read must answer Authoritative:true from the live incumbent")
	}
	if !info.LastSyncedAt.Equal(liveTime) {
		t.Fatalf("LastSyncedAt = %v, want the live incumbent's ModifiedAt %v", info.LastSyncedAt, liveTime)
	}
	if inc.calls != 1 {
		t.Fatalf("inc.Get call count = %d, want exactly 1 — via the translated incumbent class %q", inc.calls, incumbentClass)
	}

	snap := meter.Snapshot(ctx, "stub")
	if snap.Consumed != 1 {
		t.Fatalf("meter consumed = %d, want 1 (one force_fresh spend)", snap.Consumed)
	}
	if snap.Band != overlaybudget.BandOK {
		t.Fatalf("band = %q, want %q after a single spend against limit %d", snap.Band, overlaybudget.BandOK, snap.Limit)
	}
}

// TestFreshnessReaderFailedLiveReadStillSpendsAndDegrades proves the
// reserve-before-Get discipline (review #56): the force-fresh unit is
// reserved BEFORE the live incumbent call, so a live read that FAILS still
// consumes it — its HTTP call spent the workspace's HubSpot quota either
// way — and the read degrades to the mirror (Authoritative:false) rather
// than erroring. Charging only on success would let an unbounded run of
// failing force-fresh reads hammer HubSpot without the meter ever shedding.
func TestFreshnessReaderFailedLiveReadStillSpendsAndDegrades(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const externalID = "100214862042"
	mirrorTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	inc := seedMirrorAndLiveFixture(ctx, t, ms, externalID, mirrorTime, mirrorTime.Add(time.Hour))
	inc.getErr = fmt.Errorf("hubspot: unreachable")

	meter := testBudgetMeter(t, "stub")
	fr := NewFreshnessReader(func(context.Context) (Incumbent, error) { return inc, nil }, ms, meter, translatorFor(false))

	id, err := externalIDToUUID(externalID)
	if err != nil {
		t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
	}
	ref := datasource.EntityRef{Type: datasource.EntityType(canonicalClass), ID: id}

	info, err := fr.Read(ctx, ref)
	if err != nil {
		t.Fatalf("Read must degrade to the mirror on a live-read failure, not error: %v", err)
	}
	if info.Authoritative {
		t.Fatal("a failed live read must degrade to the mirror (Authoritative:false)")
	}
	if inc.calls != 1 {
		t.Fatalf("inc.Get calls = %d, want 1 (the one failed live read)", inc.calls)
	}
	if snap := meter.Snapshot(ctx, "stub"); snap.Consumed != 1 {
		t.Fatalf("meter consumed = %d, want 1 — a FAILED force-fresh read still spends its reserved unit", snap.Consumed)
	}
}

// TestFreshnessReaderNoIncumbentClassMappingDegradesHonestly proves the
// translator-miss path (review F1): a canonical type with no declared
// incumbent-class mapping must degrade to the mirror exactly like a nil
// Incumbent would — never fall back to passing the canonical name
// straight to inc.Get (stubIncumbent.Get would reject "person" against
// its fixtured "contacts", so a regression here fails loudly) — and,
// because this is a wiring gap rather than a budget decision, it must
// NOT emit mirror.budget_degraded.
func TestFreshnessReaderNoIncumbentClassMappingDegradesHonestly(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const externalID = "100214862077"
	mirrorTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	liveTime := mirrorTime.Add(time.Hour)

	inc := seedMirrorAndLiveFixture(ctx, t, ms, externalID, mirrorTime, liveTime)

	meter := testBudgetMeter(t, "stub")
	fr := NewFreshnessReader(func(context.Context) (Incumbent, error) { return inc, nil }, ms, meter, translatorFor(true)) // every canonical type misses

	id, err := externalIDToUUID(externalID)
	if err != nil {
		t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
	}
	ref := datasource.EntityRef{Type: datasource.EntityType(canonicalClass), ID: id}

	info, err := fr.Read(ctx, ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if info.Authoritative {
		t.Fatal("a translator miss must never claim Authoritative:true")
	}
	if !info.LastSyncedAt.After(mirrorTime) && !info.LastSyncedAt.Equal(mirrorTime) {
		// LastSyncedAt is the mirror row's server-side now() at ingest
		// time (mirrorstore.go's ingestSQL), not the ModifiedAt baseline
		// this test seeded — it only has to be at/after it.
		t.Fatalf("LastSyncedAt = %v must come from the mirror row's own ingest at/after %v", info.LastSyncedAt, mirrorTime)
	}
	if inc.calls != 0 {
		t.Fatalf("inc.Get call count = %d, want 0 — a translator miss must never call the live incumbent", inc.calls)
	}
	if snap := meter.Snapshot(ctx, "stub"); snap.Consumed != 0 {
		t.Fatalf("meter consumed = %d, want 0 — a wiring gap spends nothing", snap.Consumed)
	}

	var eventCount int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'workspace_id' = $1 AND envelope->>'type' = 'mirror.budget_degraded'`,
		ws.String(),
	).Scan(&eventCount); err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("mirror.budget_degraded outbox rows = %d, want 0 — a translator miss is a wiring gap, not a budget decision", eventCount)
	}
}

// TestFreshnessReaderShedDegradesToMirrorAndEmitsBudgetDegraded proves
// the S2 degrade path: at/over the shed threshold, Read never reaches
// the live incumbent (0 quota spent, asserted via inc.calls AND via the
// meter's own unchanged consumption), answers the MIRROR row's own
// staleness with Authoritative:false, and emits mirror.budget_degraded
// (OVA-EVT-3) as a real event_outbox row — the observable trace a
// silent quality drop would not leave behind.
func TestFreshnessReaderShedDegradesToMirrorAndEmitsBudgetDegraded(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const externalID = "100214862099"
	mirrorTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	liveTime := mirrorTime.Add(time.Hour)

	inc := seedMirrorAndLiveFixture(ctx, t, ms, externalID, mirrorTime, liveTime)

	meter := testBudgetMeter(t, "stub")
	// Push the REST window straight to the shed band (cap 10, shed at 8)
	// via a DIFFERENT source (poller) — proving the band is the total
	// across sources, not a per-source count the force_fresh source alone
	// would never reach.
	if err := meter.ConsumeREST(ctx, "stub", overlaybudget.SourcePoller, 8); err != nil {
		t.Fatalf("pre-loading the poller source to shed: %v", err)
	}
	if got := meter.BandREST(ctx, "stub"); got != overlaybudget.BandShed {
		t.Fatalf("BandREST = %q after loading to the shed threshold, want %q", got, overlaybudget.BandShed)
	}

	fr := NewFreshnessReader(func(context.Context) (Incumbent, error) { return inc, nil }, ms, meter, translatorFor(false))

	id, err := externalIDToUUID(externalID)
	if err != nil {
		t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
	}
	ref := datasource.EntityRef{Type: datasource.EntityType(canonicalClass), ID: id}

	info, err := fr.Read(ctx, ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if info.Authoritative {
		t.Fatal("at/over the shed threshold, Read must never claim Authoritative:true")
	}
	if info.LastSyncedAt.Equal(liveTime) {
		t.Fatal("the shed path must not have reached the live incumbent's fresher timestamp")
	}
	if !info.LastSyncedAt.After(mirrorTime) && !info.LastSyncedAt.Equal(mirrorTime) {
		t.Fatalf("LastSyncedAt = %v must come from the mirror row's own ingest at/after %v", info.LastSyncedAt, mirrorTime)
	}
	if inc.calls != 0 {
		t.Fatalf("inc.Get call count = %d, want 0 — the shed path must never spend a live read", inc.calls)
	}

	snap := meter.Snapshot(ctx, "stub")
	if snap.Consumed != 8 {
		t.Fatalf("meter consumed = %d, want unchanged at 8 (the shed path spends nothing)", snap.Consumed)
	}

	var eventCount int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'mirror.budget_degraded'
		   AND envelope->>'workspace_id' = $1
		   AND envelope->'entity'->>'id' = $2`,
		ws.String(), id.String(),
	).Scan(&eventCount); err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("mirror.budget_degraded outbox rows = %d, want exactly 1", eventCount)
	}

	// system_log carries FORCE ROW LEVEL SECURITY (migrations/core/0074):
	// unlike event_outbox (a global, RLS-free infra table), this read
	// must run inside database.WithWorkspaceTx so the tenant GUC is set,
	// or RLS silently answers zero rows for a query that looks correct.
	var systemLogCount int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			context.Background(),
			`SELECT count(*) FROM system_log WHERE workspace_id = $1 AND action = 'mirror.budget_degraded'`,
			ws,
		).Scan(&systemLogCount)
	}); err != nil {
		t.Fatalf("querying system_log: %v", err)
	}
	if systemLogCount != 1 {
		t.Fatalf("mirror.budget_degraded system_log rows = %d, want exactly 1 (the event's ledger trace)", systemLogCount)
	}
}
