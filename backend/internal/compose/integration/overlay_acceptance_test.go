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
	"go/build"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// backendModulePath is this module's own import path — spelled once so
// every import-path comparison below reads the same literal arch_test.go
// (backend/arch_test.go) already pins at the repo root.
const backendModulePath = "github.com/gradionhq/margince/backend"

// backendModuleRoot resolves the backend Go module's root directory from
// this test file's own location: `go test` always chdirs into the
// package directory it is testing (here,
// backend/internal/compose/integration), so three levels up is the
// module root — verified against go.mod rather than assumed, so a future
// package move fails loudly instead of silently walking the wrong tree.
func backendModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolving the backend module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved %q as the backend module root, but it has no go.mod: %v", root, err)
	}
	return root
}

// acceptancePackagesUnder/acceptanceDirectImports are the same
// tree-derived, direct-import-only technique backend/arch_test.go's own
// packagesUnder/projectImports use (that file lives in package
// backendarch at the module root, which holds no importable production
// code, so this suite — living in package integration — carries its own
// copy rather than reach across an import boundary that does not exist).
func acceptancePackagesUnder(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return dirs
}

// acceptanceImportContext is build.Default with the "integration" build tag
// appended: the zero-value build.Context (build.ImportDir's default) does
// NOT satisfy `//go:build integration`, so every integration-tagged file —
// including this very suite's own package and sibling packages like
// internal/modules/agents that carry integration test files — would be
// dropped into Context.ImportDir's IgnoredGoFiles and never scanned for
// imports at all. That would make an incumbent import smuggled into any
// integration-tagged file above the seam invisible to this gate. Starting
// from build.Default (not the zero value) also keeps GOOS/GOARCH/GOPATH
// correct for the host running the test.
func acceptanceImportContext() build.Context {
	ctx := build.Default
	ctx.BuildTags = append(append([]string{}, ctx.BuildTags...), "integration")
	return ctx
}

func acceptanceDirectImports(t *testing.T, dir string) []string {
	t.Helper()
	ctx := acceptanceImportContext()
	pkg, err := ctx.ImportDir(dir, 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			// A directory that genuinely holds no Go source files at all
			// (not even integration-tagged ones) has nothing to scan —
			// distinct from any other resolution error, which must
			// surface rather than be swallowed as "no imports" (T2).
			return nil
		}
		t.Fatalf("resolving %s: %v", dir, err)
	}
	var out []string
	for _, group := range [][]string{pkg.Imports, pkg.TestImports, pkg.XTestImports} {
		for _, imp := range group {
			if strings.Contains(imp, ".") {
				out = append(out, imp)
			}
		}
	}
	return out
}

// TestAcceptance_AC_OV_1_NoIncumbentImportAboveSeam proves design.md's
// AC-OV-1 (subsystems/overlay-augmentation.md: "the three AI layers and
// UI call only the SoR Provider interface — no direct incumbent-API or
// direct crm-core call exists above the seam"): no package outside
// internal/modules/overlay's own tree imports overlay/hubspot directly.
//
// internal/compose (the composition ROOT package only, ADR-0054/A69) is
// the one sanctioned exception — it is where the Dispatcher/Provider seam
// is WIRED to the concrete hubspot.Adapter (compose/overlay.go,
// compose/jobs.go), which is BELOW/AT the seam, not above it. Every
// compose SUBPACKAGE (this package included) gets no such exception: this
// test itself proves, by construction, that reuse-driving-the-fake
// throughout this suite never had to reach for hubspot directly either.
func TestAcceptance_AC_OV_1_NoIncumbentImportAboveSeam(t *testing.T) {
	root := backendModuleRoot(t)
	hubspotImportPath := backendModulePath + "/internal/modules/overlay/hubspot"
	overlayModulePrefix := backendModulePath + "/internal/modules/overlay"
	composeRootImportPath := backendModulePath + "/internal/compose"

	dirToImportPath := func(dir string) string {
		rel := strings.TrimPrefix(dir, filepath.ToSlash(root)+"/")
		return backendModulePath + "/" + rel
	}

	for _, sub := range []string{"internal/modules", "internal/platform", "internal/compose", "internal/contracts", "cmd"} {
		for _, dir := range acceptancePackagesUnder(t, filepath.Join(root, filepath.FromSlash(sub))) {
			importPath := dirToImportPath(dir)
			if strings.HasPrefix(importPath, overlayModulePrefix) {
				continue // the seam's own inside — overlay may reference its own hubspot subpackage's siblings (e.g. shared test fixtures)
			}
			if importPath == composeRootImportPath {
				continue // the ONE sanctioned composition root (see doc above)
			}
			for _, imp := range acceptanceDirectImports(t, dir) {
				if imp == hubspotImportPath {
					t.Errorf("%s imports %s directly — no package above the seam may import the incumbent adapter (AC-OV-1)", importPath, imp)
				}
			}
		}
	}

	// Positive half: the modules that DO reach records above the overlay
	// seam (the governed agent tool surface, the inbound capture sink,
	// and search's retrieval join) reach them ONLY through
	// ports/datasource — never overlay directly, confirming they are
	// exactly the "layers above the seam" AC-OV-1 describes and that the
	// seam is actually load-bearing for them, not merely unused.
	datasourcePath := backendModulePath + "/internal/shared/ports/datasource"
	for _, mod := range []string{"agents", "capture", "search"} {
		modDir := filepath.Join(root, "internal", "modules", mod)
		sawDatasource := false
		for _, dir := range acceptancePackagesUnder(t, modDir) {
			for _, imp := range acceptanceDirectImports(t, dir) {
				if imp == datasourcePath {
					sawDatasource = true
				}
				if strings.HasPrefix(imp, overlayModulePrefix) {
					t.Errorf("%s imports %s — the %s module must reach records only through %s, never overlay directly", dirToImportPath(dir), imp, mod, datasourcePath)
				}
			}
		}
		if !sawDatasource {
			t.Errorf("module %q never imports %s — expected it to reach records through the frozen SoR Provider seam", mod, datasourcePath)
		}
	}
}

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
// so these tests never import the hubspot subpackage (see AC-OV-1 above).
func contactsTranslator(canonical string) ([]string, bool) {
	if canonical == "person" {
		return []string{overlay.IncumbentClassContacts}, true
	}
	return nil, false
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
	overlayProvider := overlay.NewProvider(mirror, nil)

	// overlayRef is the overlay Provider's OWN ref for the ingested
	// fixture (the numeric-external-id<->UUID bridge is internal to
	// package overlay) — resolved once via Search, then reused by every
	// subtest below exactly like Read/Search's own subtest already does.
	overlaySearch, err := overlayProvider.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10})
	if err != nil || len(overlaySearch.Records) != 1 {
		t.Fatalf("resolving the overlay fixture's own ref: err=%v records=%d", err, len(overlaySearch.Records))
	}
	overlayRef := overlaySearch.Records[0].Ref

	// nonEmptyPersonPayload asserts rec carries a decodable, non-empty
	// person field payload for wantRef — the structural half of
	// bounded-equivalence (both modes return a real record of the same
	// shape for the same requested ref), as distinct from the trust half
	// asserted separately below.
	nonEmptyPersonPayload := func(t *testing.T, mode string, rec datasource.Record, wantRef datasource.EntityRef) {
		t.Helper()
		if rec.Ref != wantRef {
			t.Errorf("%s Read Ref = %v, want the requested %v", mode, rec.Ref, wantRef)
		}
		var fields map[string]any
		if err := json.Unmarshal(rec.Fields, &fields); err != nil || len(fields) == 0 {
			t.Fatalf("%s Read fields = %s (err %v), want a non-empty person payload", mode, rec.Fields, err)
		}
	}

	t.Run("Read is bounded-equivalent: same record shape, differing only in the trust dimension", func(t *testing.T) {
		nativeRec, err := native.Read(e.Admin(), personRef)
		if err != nil {
			t.Fatalf("native Read: %v", err)
		}
		overlayRec, err := overlayProvider.Read(ctx, overlayRef)
		if err != nil {
			t.Fatalf("overlay Read: %v", err)
		}
		nonEmptyPersonPayload(t, "native", nativeRec, personRef)
		nonEmptyPersonPayload(t, "overlay", overlayRec, overlayRef)
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

	// The published bounded-capability manifest, derived from the frozen
	// interface's own method set rather than hand-listed twice.
	readVerbs := map[string]bool{"Read": true, "Search": true, "ListObjects": true, "ListFields": true, "Freshness": true}
	unsupportedManifest := map[string]bool{
		"RunReport": true, "StageSemantic": true, "Create": true, "Update": true,
		"AdvanceDeal": true, "Archive": true, "Merge": true, "PromoteLead": true,
	}
	ifaceType := reflect.TypeOf((*datasource.SystemOfRecordProvider)(nil)).Elem()
	for i := 0; i < ifaceType.NumMethod(); i++ {
		name := ifaceType.Method(i).Name
		if readVerbs[name] == unsupportedManifest[name] {
			t.Fatalf("method %q is classified as both/neither read and unsupported — the manifest partition below is incomplete", name)
		}
	}
	if got, want := ifaceType.NumMethod(), len(readVerbs)+len(unsupportedManifest); got != want {
		t.Fatalf("SystemOfRecordProvider has %d methods but this test's manifest only classifies %d — a verb was added to the frozen seam with no manifest entry here", got, want)
	}

	t.Run("write verbs + RunReport + StageSemantic declare the published unsupported_by_sor manifest", func(t *testing.T) {
		if _, err := overlayProvider.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Create = %v, want ErrUnsupportedBySoR", err)
		}
		if _, err := overlayProvider.Update(ctx, datasource.UpdateInput{Ref: datasource.EntityRef{Type: datasource.EntityPerson}}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Update = %v, want ErrUnsupportedBySoR", err)
		}
		if _, err := overlayProvider.AdvanceDeal(ctx, datasource.AdvanceDealInput{}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("AdvanceDeal = %v, want ErrUnsupportedBySoR", err)
		}
		if _, err := overlayProvider.Archive(ctx, datasource.EntityRef{Type: datasource.EntityPerson}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("Archive = %v, want ErrUnsupportedBySoR", err)
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
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'mirror.budget_degraded' AND envelope->>'workspace_id' = $1`,
		ws.String(),
	).Scan(&eventCount); err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("mirror.budget_degraded outbox rows = %d, want exactly 1", eventCount)
	}
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
	since := oldBaseline.Add(-time.Second)
	if _, err := overlay.Reconcile(ctx, fakeInc, mirror, meter, overlay.IncumbentClassCompanies, since); err != nil {
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
	if err := overlay.Backfill(adminCtx, fakeInc, mirror, overlay.IncumbentClassDeals); err != nil {
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
