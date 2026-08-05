// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The AC-OV read+sync acceptance suite (design.md §8 — the
// acceptance criteria as deterministic CI gates, not manual checks). One
// test per criterion, named for it, so a failing test names the exact
// criterion it breaks. This suite CODIFIES existing behaviour: every test
// here is expected to pass against the feature as already built — a
// failure means either a real product defect (fix the product) or a
// genuine, upstream-reconcilable spec gap (name it, never invent
// behaviour to make the test pass).
//
// This suite reuses, rather than rebuilds, two things already proven
// elsewhere:
//   - the composed harness (Setup/Env, seedOverlayModeWorkspace,
//     overlayActorCtx, stubOwnerEmails — overlay_dispatch_integration_test.go;
//     openAppPool, the env HTTP harness — overlay_e2e_test.go).
//   - the overlay/fake incumbent as the concurrent mutator every AC that
//     needs a "live incumbent changed something" fixture drives — seeded
//     and read by INCUMBENT class names (overlay.IncumbentClass*), never
//     the canonical entity name, per the seam rule fake's own doc and
//     backfill.go's own doc both state.
//
// Scope: the READ subset only. AC-OV-5 (T2 taint into embeddings/
// context-graph — no such derivative index of the overlay mirror exists
// yet in this build; see teardown.go's own doc), AC-OV-6 (injection
// re-gate), the write-path ACs (AC-OV-4/9/10), and the 2x-SLO staleness
// floor (AC-OV-11's branch-1b half) are OUT OF SCOPE here — asserting
// them would either fabricate behaviour this build doesn't have, or
// duplicate a gate that belongs to a later task.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// seedMirroredPersonFixture stands up the overlay-mode half of AC-OV-2's
// two-mode comparison: a second, overlay-mode workspace whose acting user is
// mapped to the fake incumbent's owner-1, one ingested mirror person, and the
// overlay Provider serving it.
//
// The ref it returns is the overlay Provider's OWN ref for the ingested
// fixture (the numeric-external-id<->UUID bridge is internal to package
// overlay) — resolved once via Search, then reused by every read verb the
// caller exercises.
func seedMirroredPersonFixture(t *testing.T, e *Env) (context.Context, *overlay.Provider, datasource.EntityRef) {
	t.Helper()
	overlayWS, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(overlayWS, actorID)
	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: "person", ExternalID: "100214862055",
		Fields: map[string]any{"firstname": "Ada Overlay"}, ModifiedAt: time.Now().UTC(), OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the overlay fixture: %v", err)
	}
	provider := overlay.NewProvider(mirror, nil)
	found, err := provider.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10})
	if err != nil || len(found.Records) != 1 {
		t.Fatalf("resolving the overlay fixture's own ref: err=%v records=%d", err, len(found.Records))
	}
	return ctx, provider, found.Records[0].Ref
}

// assertNonEmptyPersonPayload asserts rec carries a decodable, non-empty
// person field payload for wantRef — the structural half of
// bounded-equivalence (both modes return a real record of the same
// shape for the same requested ref), as distinct from the trust half
// AC-OV-2's subtests assert separately.
func assertNonEmptyPersonPayload(t *testing.T, mode string, rec datasource.Record, wantRef datasource.EntityRef) {
	t.Helper()
	if rec.Ref != wantRef {
		t.Errorf("%s Read Ref = %v, want the requested %v", mode, rec.Ref, wantRef)
	}
	var fields map[string]any
	if err := json.Unmarshal(rec.Fields, &fields); err != nil || len(fields) == 0 {
		t.Fatalf("%s Read fields = %s (err %v), want a non-empty person payload", mode, rec.Fields, err)
	}
}

// assertEverySeamVerbIsClassifiedExactlyOnce holds the published
// bounded-capability manifest, derived from the frozen interface's own method
// set rather than hand-listed twice. Write-back (branch 2) split the old
// "every write is unsupported" partition into three: the read verbs, the
// SUPPORTED write-back verbs (Create/Update/Archive — incumbent-first,
// OVA-MAP-W), and the still-unsupported verbs (AdvanceDeal needs the overlay
// stage-map StageSemantic also lacks; Merge/PromoteLead have no atomic
// incumbent projection, OVA-MAP-W6; RunReport/StageSemantic have no HubSpot
// analogue).
func assertEverySeamVerbIsClassifiedExactlyOnce(t *testing.T) {
	t.Helper()
	readVerbs := map[string]bool{"Read": true, "Search": true, "ListObjects": true, "ListFields": true, "Freshness": true}
	writeVerbs := map[string]bool{"Create": true, "Update": true, "Archive": true}
	unsupportedManifest := map[string]bool{
		"RunReport": true, "StageSemantic": true, "AdvanceDeal": true, "Merge": true, "PromoteLead": true,
	}
	ifaceType := reflect.TypeOf((*datasource.SystemOfRecordProvider)(nil)).Elem()
	for i := 0; i < ifaceType.NumMethod(); i++ {
		name := ifaceType.Method(i).Name
		classified := 0
		for _, set := range []map[string]bool{readVerbs, writeVerbs, unsupportedManifest} {
			if set[name] {
				classified++
			}
		}
		if classified != 1 {
			t.Fatalf("method %q is in %d manifest categories — it must be in exactly one (read / write / unsupported)", name, classified)
		}
	}
	if got, want := ifaceType.NumMethod(), len(readVerbs)+len(writeVerbs)+len(unsupportedManifest); got != want {
		t.Fatalf("SystemOfRecordProvider has %d methods but this test's manifest only classifies %d — a verb was added to the frozen seam with no manifest entry here", got, want)
	}
}

// TestAcceptance_AC_OV_2_BoundedEquivalence_ReadSubset proves design.md's
// AC-OV-2/ADR-0018 bounded-equivalence invariant for the read subset:
// every one of the frozen SystemOfRecordProvider's read verbs behaves
// (native vs overlay) — proven by actually calling each one against a
// native-mode Provider and an overlay-mode Provider seeded with an
// equivalent record — while every write verb plus RunReport declares the
// SAME apperrors.ErrUnsupportedBySoR overlay answers with, and that
// unsupported set is exactly the published manifest: derived from the
// frozen interface's own method set via reflection (never hand-
// duplicated against the interface declaration a second time), so a
// future verb added to the seam fails this test until classified rather
// than silently passing unclassified.
func TestAcceptance_AC_OV_2_BoundedEquivalence_ReadSubset(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Ada Native", nil)
	native := compose.NewProvider(e.Pool)
	personRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: personID}

	ctx, overlayProvider, overlayRef := seedMirroredPersonFixture(t, e)

	t.Run("Read is bounded-equivalent: same record shape, differing only in the trust dimension", func(t *testing.T) {
		nativeRec, err := native.Read(e.Admin(), personRef)
		if err != nil {
			t.Fatalf("native Read: %v", err)
		}
		overlayRec, err := overlayProvider.Read(ctx, overlayRef)
		if err != nil {
			t.Fatalf("overlay Read: %v", err)
		}
		assertNonEmptyPersonPayload(t, "native", nativeRec, personRef)
		assertNonEmptyPersonPayload(t, "overlay", overlayRec, overlayRef)
		// The one dimension bounded-equivalence PERMITS to differ: a native
		// read is authoritative; an overlay read is mirror-backed and must
		// declare Authoritative=false (03e §2.3 / AC-OV-5). Everything else
		// about the two reads is the same shape — that is the invariant.
		if !nativeRec.Freshness.Authoritative {
			t.Error("native Read must be Authoritative=true (SoR-mode is always authoritative)")
		}
		if overlayRec.Freshness.Authoritative {
			t.Error("overlay Read must be Authoritative=false (mirror-backed, never authoritative)")
		}
	})

	t.Run("Search is bounded-equivalent: both return person records, native authoritative, overlay not", func(t *testing.T) {
		query := datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10}
		nativeSearch, err := native.Search(e.Admin(), query)
		if err != nil || len(nativeSearch.Records) == 0 {
			t.Fatalf("native Search: err=%v records=%d", err, len(nativeSearch.Records))
		}
		overlaySearch, err := overlayProvider.Search(ctx, query)
		if err != nil || len(overlaySearch.Records) == 0 {
			t.Fatalf("overlay Search: err=%v records=%d", err, len(overlaySearch.Records))
		}
		for _, r := range nativeSearch.Records {
			if !r.Freshness.Authoritative {
				t.Errorf("native Search record %v must be Authoritative=true", r.Ref)
			}
		}
		for _, r := range overlaySearch.Records {
			if r.Freshness.Authoritative {
				t.Errorf("overlay Search record %v must be Authoritative=false", r.Ref)
			}
		}
	})

	t.Run("ListObjects/ListFields/Freshness behave equivalently, Freshness carrying the same trust split", func(t *testing.T) {
		if _, err := native.ListObjects(e.Admin()); err != nil {
			t.Fatalf("native ListObjects: %v", err)
		}
		if _, err := native.ListFields(e.Admin(), datasource.EntityPerson); err != nil {
			t.Fatalf("native ListFields: %v", err)
		}
		nativeFresh, err := native.Freshness(e.Admin(), personRef)
		if err != nil {
			t.Fatalf("native Freshness: %v", err)
		}
		if _, err := overlayProvider.ListObjects(ctx); err != nil {
			t.Fatalf("overlay ListObjects: %v", err)
		}
		if _, err := overlayProvider.ListFields(ctx, datasource.EntityPerson); err != nil {
			t.Fatalf("overlay ListFields: %v", err)
		}
		overlayFresh, err := overlayProvider.Freshness(ctx, overlayRef)
		if err != nil {
			t.Fatalf("overlay Freshness: %v", err)
		}
		if !nativeFresh.Authoritative {
			t.Error("native Freshness must report Authoritative=true")
		}
		if overlayFresh.Authoritative {
			t.Error("overlay Freshness must report Authoritative=false")
		}
	})

	assertEverySeamVerbIsClassifiedExactlyOnce(t)

	t.Run("AdvanceDeal + Merge + PromoteLead + RunReport + StageSemantic declare the published unsupported_by_sor manifest", func(t *testing.T) {
		if _, err := overlayProvider.AdvanceDeal(ctx, datasource.AdvanceDealInput{}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("AdvanceDeal = %v, want ErrUnsupportedBySoR", err)
		}
		if _, err := overlayProvider.Merge(ctx, datasource.MergeInput{Type: datasource.EntityPerson}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Merge = %v, want ErrUnsupportedBySoR", err)
		}
		if _, merged, err := overlayProvider.PromoteLead(ctx, ids.NewV7(), "manual", nil); !errors.Is(err, apperrors.ErrUnsupportedBySoR) || merged {
			t.Errorf("PromoteLead = (merged=%v, err=%v), want (false, ErrUnsupportedBySoR)", merged, err)
		}
		if _, err := overlayProvider.RunReport(ctx, datasource.ReportPlan{Entity: datasource.EntityDeal}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("RunReport = %v, want ErrUnsupportedBySoR", err)
		}
		if _, _, err := overlayProvider.StageSemantic(ctx, ids.NewV7()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("StageSemantic = %v, want ErrUnsupportedBySoR", err)
		}
	})

	t.Run("write-back verbs answer their declared capability, never a silent break", func(t *testing.T) {
		// AC-OV-2's bounded equivalence: each op either passes identically or
		// returns a DECLARED unsupported_by_sor — the provider's answer must
		// match SupportsWrite exactly, whichever way that falls.
		//
		// This overlayProvider is built without a write incumbent resolver, so
		// a verb it DOES serve surfaces a clear configuration error instead —
		// anything but ErrUnsupportedBySoR proves it is a recognized verb.
		if _, err := overlayProvider.Update(ctx, datasource.UpdateInput{Ref: datasource.EntityRef{Type: datasource.EntityPerson}}); errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Update must be a supported write-back verb, got ErrUnsupportedBySoR")
		}
		if _, err := overlayProvider.Archive(ctx, datasource.EntityRef{Type: datasource.EntityPerson}); errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Archive must be a supported write-back verb, got ErrUnsupportedBySoR")
		}
		// Create is declared unsupported for every type (SupportsWrite): the
		// write mapping leaves owner_id read-only, so a created incumbent
		// record would be unowned and the NULL-OWNER rule would hide it from
		// everyone including its author. The DECLARED refusal is the correct
		// bounded-equivalence answer, and it must hold at the provider — not
		// only at the REST guard, which the agent and automation seams bypass.
		if _, err := overlayProvider.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Create = %v, want the declared ErrUnsupportedBySoR", err)
		}
	})
}

// countAcceptanceMirrorConflictEvents counts event_outbox rows carrying
// mirror.conflict for ws and externalID — event_outbox is a global,
// RLS-free infra table (the same caveat
// freshness_integration_test.go/reconcile_integration_test.go's own
// queries document), so no workspace GUC is needed to read it, only to
// filter by workspace in the query itself.
func countAcceptanceMirrorConflictEvents(ctx context.Context, e *Env, ws, externalID string) (int, error) {
	var count int
	err := e.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'mirror.conflict'
		   AND envelope->>'workspace_id' = $1
		   AND envelope->'payload'->>'external_id' = $2`,
		ws, externalID,
	).Scan(&count)
	return count, err
}

// TestAcceptance_AC_OV_8_IncumbentWinsConflict proves design.md's AC-OV-8
// (OVA-EVT-1, the incumbent-wins reconcile rule) in both directions: a
// genuine incumbent-side change (strictly newer than the mirror's stored
// baseline) overwrites the mirror and emits exactly one mirror.conflict
// event; the REVERSE direction — an incumbent sweep answering a value
// OLDER than what the mirror already holds (a delayed/replayed page) —
// must never win: Ingest's own staleness guard holds the mirror at its
// current, fresher state, and Reconcile must emit nothing for a write
// that never actually landed. Driven through the package-level
// overlay.Reconcile with the fake incumbent as the concurrent mutator,
// the same seam backfill_integration_test.go and
// reconcile_integration_test.go already exercise a layer down.
func TestAcceptance_AC_OV_8_IncumbentWinsConflict(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(ws, actorID)
	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}

	const objectClass = "organization"
	const winsExternalID = "61655665900"
	const reverseExternalID = "61655665901"
	oldBaseline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	mirrorNewerBaseline := oldBaseline.Add(time.Hour)

	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: objectClass, ExternalID: winsExternalID,
		Fields: map[string]any{"display_name": "Old"}, ModifiedAt: oldBaseline, OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("seeding the pre-existing (wins-case) mirror row: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: objectClass, ExternalID: reverseExternalID,
		Fields: map[string]any{"display_name": "Current"}, ModifiedAt: mirrorNewerBaseline, OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("seeding the pre-existing (reverse-case) mirror row: %v", err)
	}

	// Both incumbent-side records carry the SAME owner ("owner-1") the
	// mirror rows were already ingested with above: Ingest's
	// ProjectOwnerVisibility re-projects visibility on every landed
	// write (mirrorstore.go), and an incoming record with NO owner would
	// clear the existing grant under the null-owner rule (visibility.go)
	// — an unrelated visibility regression this test must not trip over
	// while proving the conflict/no-conflict distinction.
	fakeInc := fake.New()
	winsRec := fake.Rec(winsExternalID, map[string]any{"display_name": "New From Incumbent"})
	winsRec.ObjectClass = objectClass
	winsRec.ModifiedAt = oldBaseline.Add(30 * time.Minute) // strictly newer than the mirror's baseline
	winsRec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassCompanies, winsRec)

	reverseRec := fake.Rec(reverseExternalID, map[string]any{"display_name": "Stale From Incumbent"})
	reverseRec.ObjectClass = objectClass
	reverseRec.ModifiedAt = mirrorNewerBaseline.Add(-30 * time.Minute) // OLDER than the mirror's own current baseline
	reverseRec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassCompanies, reverseRec)

	meter := acceptanceBudgetMeter(t)
	watermark := oldBaseline.Add(-time.Second)
	// watermark sits above the connection-derived floor (connectedAt minus
	// the 15-minute skew grace), so the sweep's internal floor leaves it
	// unchanged — this test is about the conflict/no-conflict distinction,
	// not the floor.
	if _, err := overlay.Reconcile(ctx, fakeInc, mirror, meter, overlay.IncumbentClassCompanies, watermark, oldBaseline); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	winsRow, err := mirror.Get(ctx, objectClass, winsExternalID)
	if err != nil {
		t.Fatalf("reading back the wins-case row: %v", err)
	}
	if winsRow.Fields["display_name"] != "New From Incumbent" {
		t.Fatalf("wins-case mirror row = %+v, want the incumbent-wins overwrite", winsRow.Fields)
	}

	reverseRow, err := mirror.Get(ctx, objectClass, reverseExternalID)
	if err != nil {
		t.Fatalf("reading back the reverse-case row: %v", err)
	}
	if reverseRow.Fields["display_name"] != "Current" {
		t.Fatalf("reverse-case mirror row = %+v, want UNCHANGED — a stale incumbent sweep must never overwrite a fresher mirror row", reverseRow.Fields)
	}

	winsEvents, err := countAcceptanceMirrorConflictEvents(context.Background(), e, ws.String(), winsExternalID)
	if err != nil {
		t.Fatalf("querying event_outbox for the wins case: %v", err)
	}
	if winsEvents != 1 {
		t.Fatalf("mirror.conflict rows for the wins case = %d, want exactly 1", winsEvents)
	}

	reverseEvents, err := countAcceptanceMirrorConflictEvents(context.Background(), e, ws.String(), reverseExternalID)
	if err != nil {
		t.Fatalf("querying event_outbox for the reverse case: %v", err)
	}
	if reverseEvents != 0 {
		t.Fatalf("mirror.conflict rows for the reverse case = %d, want 0 — the reverse direction must never fire", reverseEvents)
	}
}

// seedUnmappedAppUser inserts one more human app_user into ws, deliberately
// never given a mirror_user_map row — the "unmapped user" fixture
// overlay_e2e_test.go's own seedSecondAppUser builds for the default
// harness workspace, needed here for a SECOND, independent overlay-mode
// workspace seedOverlayModeWorkspace mints.
func seedUnmappedAppUser(t *testing.T, ws ids.UUID) ids.UUID {
	t.Helper()
	owner := OwnerConn(t)
	userID := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Unmapped')`,
		userID, ws, "unmapped-"+userID.String()+"@overlay.test"); err != nil {
		t.Fatalf("seeding the unmapped app_user: %v", err)
	}
	return userID
}

// TestAcceptance_AC_OV_11_FailClosedVisibility_ReadSubset proves design.md's
// AC-OV-11 branch-1 absence form (the fail-closed sharing/visibility
// re-enforcement over the mirror, design.md §4.6): three independent
// ways a read must resolve to nothing rather than leak — a row the
// acting user isn't granted visibility into, a null-owner record (fail-
// closed hidden for everyone per the pinned §4.6 rule), and an unmapped
// user (existence-hiding ErrNotFound, never an empty-but-successful
// page and never a 403). The 2x-SLO staleness floor is branch-1b
// (out of scope for this read-subset suite).
func TestAcceptance_AC_OV_11_FailClosedVisibility_ReadSubset(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(ws, actorID)
	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}

	const objectClass = "person"
	const hiddenOwnerExternalID = "100214862088" // owned by owner-2, whom nobody in this workspace is mapped to
	const nullOwnerExternalID = "100214862099"   // no owner at all
	const ownedExternalID = "100214862100"       // owned by owner-1 — proves a real, visible row exists so the hidden cases aren't vacuous

	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: objectClass, ExternalID: hiddenOwnerExternalID,
		Fields: map[string]any{"firstname": "OwnedByOther"}, ModifiedAt: time.Now().UTC(), OwnerExternalID: "owner-2",
	}); err != nil {
		t.Fatalf("ingesting the hidden-owner fixture: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: objectClass, ExternalID: nullOwnerExternalID,
		Fields: map[string]any{"firstname": "Unowned"}, ModifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ingesting the null-owner fixture: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: objectClass, ExternalID: ownedExternalID,
		Fields: map[string]any{"firstname": "VisibleToOwner1"}, ModifiedAt: time.Now().UTC(), OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the owned (visible) fixture: %v", err)
	}

	t.Run("a row the actor cannot see resolves hidden (ErrNotFound, never a 403)", func(t *testing.T) {
		if _, err := mirror.Get(ctx, objectClass, hiddenOwnerExternalID); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("Get for a row owned by an unrelated incumbent user = %v, want apperrors.ErrNotFound", err)
		}
	})

	t.Run("a null-owner record resolves hidden for every user, including a validly-mapped one", func(t *testing.T) {
		if _, err := mirror.Get(ctx, objectClass, nullOwnerExternalID); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("Get for a null-owner record = %v, want apperrors.ErrNotFound", err)
		}
	})

	t.Run("the owned fixture stays visible to its mapped owner (the hidden cases above are not vacuous)", func(t *testing.T) {
		row, err := mirror.Get(ctx, objectClass, ownedExternalID)
		if err != nil {
			t.Fatalf("Get for the actor's own owned record: %v", err)
		}
		if row.Fields["firstname"] != "VisibleToOwner1" {
			t.Fatalf("wrong row returned: %+v", row.Fields)
		}
	})

	t.Run("an unmapped user sees zero rows through the composed dispatcher (existence-hiding)", func(t *testing.T) {
		unmappedCtx := overlayActorCtx(ws, seedUnmappedAppUser(t, ws))
		d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, overlaybudget.New(nil, nil), nil), e.Pool)
		if _, err := d.Search(unmappedCtx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10}); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("dispatched Search for an unmapped user = %v, want apperrors.ErrNotFound (existence-hiding, zero rows)", err)
		}
	})
}

// TestAcceptance_OVA_AC_1_TeardownPurges proves design.md's OVA-AC-1
// (§4.9): disconnecting an overlay connection leaves no incumbent-derived
// tenant data queryable through the mirror, its association edges, the
// visibility projection over them, or the owner-identity map — proven
// both at the storage layer (direct table counts) AND through the
// production read path itself (a dispatched Search, the SAME seam a real
// native/read_record call rides, answers zero records post-teardown) —
// while the connection lifecycle's own audit trail is RETAINED and
// PII/credential-free. Driven end to end over the real composed HTTP
// surface (connect/disconnect) plus the fake incumbent as the
// concurrent mutator for the backfilled fixture, reusing the
// e2e harness's own pattern (overlay_e2e_test.go) rather than rebuilding
// it.
//
// Scope note: no embeddings/context-graph/FTS index of the overlay
// mirror exists in this build yet (teardown.go's own doc: "No
// embeddings/context-graph/FTS tables exist yet in this build... nothing
// here to purge on their behalf until that lands") — AC-OV-5 (the
// taint-into-derivatives criterion) is out of this read-subset suite's
// scope, so this test asserts exactly what purgeMirror actually owns
// today (mirror, association, visibility, user-map, and the
// backfill-cursor + reconcile-watermark sync checkpoints) rather than
// fabricate an assertion against a table that does not exist. The
// module's own teardown_integration_test.go DERIVES the full purge
// obligation from the catalog; this end-to-end test rides the real HTTP
// surface and checks the tables its own fixture populates.
func TestAcceptance_OVA_AC_1_TeardownPurges(t *testing.T) {
	vault := keyvault.NewMemory()
	e := setupWithOptions(t, compose.WithKeyvault(vault))
	e.bootstrapWorkspace(t)

	var conn map[string]any
	if status := e.call(t, "POST", "/v1/overlay/connection", anyMap{
		"incumbent": "hubspot", "region": "eu1", "privateAppToken": "fake-token-never-used",
	}, nil, &conn); status != http.StatusCreated {
		t.Fatalf("connect overlay = %d %v", status, conn)
	}

	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}
	adminID, err := ids.Parse(me["user"].(anyMap)["id"].(string))
	if err != nil {
		t.Fatalf("parsing admin user id: %v", err)
	}
	var wsIDStr string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsIDStr); err != nil {
		t.Fatalf("looking up the workspace id: %v", err)
	}
	wsID, err := ids.Parse(wsIDStr)
	if err != nil {
		t.Fatalf("parsing workspace id: %v", err)
	}

	pool := openAppPool(t)
	mirror := overlay.NewMirrorStore(pool, stubOwnerEmails{})
	adminCtx := overlayActorCtx(wsID, adminID)
	if err := mirror.UpsertUserMap(adminCtx, ids.From[ids.UserKind](adminID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the admin to the fake incumbent owner: %v", err)
	}

	fakeInc := fake.New()
	dealRec := fake.Rec("700001", map[string]any{"dealname": "Big Deal"})
	dealRec.ObjectClass = "deal"
	dealRec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassDeals, dealRec)
	fakeInc.SeedAssoc(overlay.IncumbentClassDeals, "700001", overlay.IncumbentClassCompanies, overlay.Assoc{
		FromType: "deal", FromID: "700001", ToType: "organization", ToID: "800001",
		TypeID: 5, Category: "HUBSPOT_DEFINED", Direction: "forward",
	})
	if _, err := overlay.Backfill(adminCtx, fakeInc, mirror, overlay.IncumbentClassDeals, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("backfilling the fake incumbent's deals (with its company association): %v", err)
	}

	var seededMirror, seededAssoc int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM overlay_mirror WHERE workspace_id = $1`, wsIDStr).Scan(&seededMirror); err != nil {
		t.Fatalf("counting the seeded mirror rows: %v", err)
	}
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM overlay_association WHERE workspace_id = $1`, wsIDStr).Scan(&seededAssoc); err != nil {
		t.Fatalf("counting the seeded association rows: %v", err)
	}
	if seededMirror == 0 || seededAssoc == 0 {
		t.Fatalf("fixture is broken: seeded mirror=%d association=%d, want both > 0", seededMirror, seededAssoc)
	}

	dispatcher := compose.NewDispatcher(compose.NewProvider(pool), compose.NewOverlayProvider(pool, overlaybudget.New(nil, nil), nil), pool)
	preTeardown, err := dispatcher.Search(adminCtx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityDeal}, Limit: 10})
	if err != nil || len(preTeardown.Records) != 1 {
		t.Fatalf("expected the mapped admin to see the one backfilled deal before disconnect: err=%v records=%d", err, len(preTeardown.Records))
	}

	// The OVB budget window is NOT in the teardown purge list: it lives in
	// Redis now (overlay-budget chapter), not a workspace-scoped Postgres
	// table, and its fixed-window counters expire on their own TTL. There
	// is no PG row for disconnect to purge.

	if code := e.call(t, "DELETE", "/v1/overlay/connection", nil, nil, nil); code != http.StatusAccepted {
		t.Fatalf("disconnect overlay = %d, want 202", code)
	}

	// overlay_backfill_cursor is in this list because the Backfill above
	// genuinely converged it (done=true) — a cursor surviving disconnect
	// would short-circuit the next connection's initial mirror load.
	counts := map[string]int{}
	for _, table := range []string{"overlay_mirror", "overlay_association", "mirror_visibility", "mirror_user_map", "overlay_backfill_cursor", "overlay_reconcile_watermark"} {
		var n int
		if err := e.owner.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE workspace_id = $1`, table), wsIDStr).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		counts[table] = n
	}
	for table, n := range counts {
		if n != 0 {
			t.Errorf("%s has %d rows after disconnect, want 0", table, n)
		}
	}

	var tombstoneCount int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM overlay_tombstone WHERE workspace_id = $1`, wsIDStr).Scan(&tombstoneCount); err != nil {
		t.Fatalf("counting overlay_tombstone rows: %v", err)
	}
	if tombstoneCount == 0 {
		t.Error("overlay_tombstone has no rows after disconnect, want at least one (the purged deal)")
	}

	// The production read path itself: the workspace flipped back to
	// native mode, so the SAME kind of dispatched Search call now answers
	// whatever the (empty) native deals store holds — never the purged
	// mirror — proving no incumbent-derived data is reachable through the
	// real seam, not merely absent from the tables a direct count checks.
	// A FRESH Dispatcher is built here rather than reusing the one above:
	// Dispatcher intentionally caches a workspace's resolved x_sor_mode
	// for a few seconds (dispatcher.go's own sorModeCacheTTL doc — a
	// deliberate, documented lag budget for a rare admin action, not a
	// bug), so the SAME instance queried moments ago would still answer
	// from that cache here; a fresh instance has no such cache to race
	// against, without this test needing a real-clock sleep past the TTL
	// (T11).
	//
	// The native deals module also gates Search on real object-RBAC
	// (unlike the overlay mirror's own visibility join adminCtx above was
	// built for), so this call rebinds the same admin actor with
	// AdminPerms, the harness's own full-access fixture.
	adminNativeCtx := principal.WithActor(
		principal.WithCorrelationID(principal.WithWorkspaceID(context.Background(), wsID), ids.NewV7()),
		principal.Principal{Type: principal.PrincipalHuman, ID: "human:" + adminID.String(), UserID: adminID, Permissions: AdminPerms},
	)
	postDisconnectDispatcher := compose.NewDispatcher(compose.NewProvider(pool), compose.NewOverlayProvider(pool, overlaybudget.New(nil, nil), nil), pool)
	postTeardown, err := postDisconnectDispatcher.Search(adminNativeCtx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityDeal}, Limit: 10})
	if err != nil {
		t.Fatalf("post-disconnect dispatched Search: %v", err)
	}
	if len(postTeardown.Records) != 0 {
		t.Fatalf("post-disconnect dispatched Search returned %d records, want 0 — no incumbent-derived data may be reachable through the production read path", len(postTeardown.Records))
	}

	var audit struct {
		Data []struct {
			EntityType string         `json:"entity_type"`
			Action     string         `json:"action"`
			Before     map[string]any `json:"before"`
			After      map[string]any `json:"after"`
		} `json:"data"`
	}
	if code := e.call(t, "GET", "/v1/audit-log?entity_type=incumbent_connection&action=archive", nil, nil, &audit); code != http.StatusOK {
		t.Fatalf("audit log = %d", code)
	}
	if len(audit.Data) != 1 {
		t.Fatalf("expected exactly one retained incumbent_connection archive audit row, got %d", len(audit.Data))
	}
	for _, snapshot := range []map[string]any{audit.Data[0].Before, audit.Data[0].After} {
		for key := range snapshot {
			if key != "incumbent" && key != "region" && key != "status" {
				t.Errorf("connection audit snapshot carries an unexpected field %q — PII/credential leak: %v", key, snapshot)
			}
		}
	}
}
