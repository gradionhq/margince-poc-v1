// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Dispatcher's overlay-branch unit-level proof: every one of the 13
// datasource.SystemOfRecordProvider verbs, dispatched to the overlay
// provider without ever needing a real Postgres. This works by seeding
// the TTL cache directly (a package-internal test's own privilege) with
// overlay=true for a fixed workspace, so isOverlay never has to query
// workspace.x_sor_mode — and by handing Dispatcher an
// overlay.NewProvider(nil, nil), which answers a clean, well-defined
// error for every verb (proven by provider_test.go) rather than ever
// touching a mirror store. d.native is left nil: the overlay=true seed
// guarantees it is never dereferenced. The native-mode branch (and the
// real cache-miss DB query) is proven end to end by
// compose/integration/overlay_dispatch_integration_test.go, which needs
// a real, migrated Postgres.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// dispatcherFixedNow is the deterministic clock seed for these unit
// tests — the cache TTL math is relative to it, so a fixed base keeps
// them off the real clock without changing what they assert.
var dispatcherFixedNow = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// overlaySeededDispatcher builds a Dispatcher whose cache already
// answers overlay=true for wsID, so every verb call below dispatches to
// the overlay provider without any DB round trip.
func overlaySeededDispatcher(wsID ids.UUID, now time.Time) *Dispatcher {
	d := newDispatcherWithClock(nil, overlay.NewProvider(nil, nil), nil, func() time.Time { return now })
	d.cache[wsID] = sorModeCacheEntry{overlay: true, expiresAt: now.Add(time.Hour)}
	return d
}

func TestDispatcherRoutesEveryVerbToTheOverlayProviderWhenCached(t *testing.T) {
	wsID := ids.NewV7()
	now := dispatcherFixedNow
	d := overlaySeededDispatcher(wsID, now)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	if _, err := d.Read(ctx, ref); err == nil {
		t.Error("Read: want the overlay provider's nil-mirror-store error, got nil")
	}
	if _, err := d.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}}); err == nil {
		t.Error("Search: want the overlay provider's nil-mirror-store error, got nil")
	}
	if _, err := d.ListObjects(ctx); err == nil {
		t.Error("ListObjects: want the overlay provider's nil-mirror-store error, got nil")
	}
	if _, err := d.ListFields(ctx, datasource.EntityPerson); err == nil {
		t.Error("ListFields: want the overlay provider's nil-mirror-store error, got nil")
	}
	if _, err := d.RunReport(ctx, datasource.ReportPlan{Entity: datasource.EntityDeal}); err == nil {
		t.Error("RunReport: want ErrUnsupportedBySoR, got nil")
	}
	if _, _, err := d.StageSemantic(ctx, ids.NewV7()); err == nil {
		t.Error("StageSemantic: want ErrUnsupportedBySoR, got nil")
	}
	// Create/Update/Archive are supported write-back verbs now, so their
	// error here is the overlay provider's own (object-RBAC/nil-store), not
	// ErrUnsupportedBySoR — the assertion still proves the verb routed to the
	// overlay provider rather than the (nil) native one.
	if _, err := d.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson}); err == nil {
		t.Error("Create: want the overlay provider's error, got nil")
	}
	if _, err := d.Update(ctx, datasource.UpdateInput{Ref: ref}); err == nil {
		t.Error("Update: want the overlay provider's error, got nil")
	}
	if _, err := d.AdvanceDeal(ctx, datasource.AdvanceDealInput{}); err == nil {
		t.Error("AdvanceDeal: want ErrUnsupportedBySoR, got nil")
	}
	if _, err := d.Archive(ctx, ref); err == nil {
		t.Error("Archive: want the overlay provider's error, got nil")
	}
	if _, err := d.Merge(ctx, datasource.MergeInput{Type: datasource.EntityPerson}); err == nil {
		t.Error("Merge: want ErrUnsupportedBySoR, got nil")
	}
	if _, _, err := d.PromoteLead(ctx, ids.NewV7(), "manual", nil); err == nil {
		t.Error("PromoteLead: want ErrUnsupportedBySoR, got nil")
	}
	if _, err := d.Freshness(ctx, ref); err == nil {
		t.Error("Freshness: want the overlay provider's nil-mirror-store error, got nil")
	}
}

// TestDispatcherOverlayModeForCachesWithinTheTTL proves overlayModeFor's
// own caching contract in isolation: once seeded, a second call within
// the TTL answers from the cache (no re-resolution needed) — a
// regression here would show up as a spurious extra DB round trip on
// every single dispatched verb in production.
func TestDispatcherOverlayModeForCachesWithinTheTTL(t *testing.T) {
	wsID := ids.NewV7()
	now := dispatcherFixedNow
	d := overlaySeededDispatcher(wsID, now)

	got, err := d.overlayModeFor(context.Background(), wsID)
	if err != nil {
		t.Fatalf("overlayModeFor: unexpected error: %v", err)
	}
	if !got {
		t.Fatal("overlayModeFor: want true from the seeded cache entry")
	}
}

// TestDispatcherIsOverlayWithNoWorkspaceBoundAnswersFalse proves the
// honest default for a context with no workspace at all (e.g. a
// background/system context) — native, never a guess, and never a DB
// round trip (d.pool is nil here; a query would panic if attempted).
func TestDispatcherIsOverlayWithNoWorkspaceBoundAnswersFalse(t *testing.T) {
	d := newDispatcherWithClock(nil, nil, nil, time.Now)
	got, err := d.isOverlay(context.Background())
	if err != nil {
		t.Fatalf("isOverlay: unexpected error: %v", err)
	}
	if got {
		t.Fatal("isOverlay with no workspace bound: want false (native), got true")
	}
}
