// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// cachedModeDispatcher builds a Dispatcher whose cache already answers
// `cached` for wsID while the workspace ROW (what queryMode reads) answers
// `stored`. Passing cached=false with stored=true reproduces the second api
// replica in the connect race: the process that committed the flip
// invalidated only its OWN cache, so this one still holds the pre-flip mode.
//
// queryMode counts its calls, so a test can assert not just where a verb
// routed but whether the mode was actually re-read or served from the entry
// seeded below. The native provider is left nil: a verb that wrongly routes
// native panics rather than quietly passing.
func cachedModeDispatcher(wsID ids.UUID, cached, stored bool) (*Dispatcher, *int) {
	calls := 0
	now := dispatcherFixedNow
	d := newDispatcherWithClock(nil, overlay.NewProvider(nil, nil), nil, func() time.Time { return now })
	d.queryMode = func(context.Context, ids.UUID) (bool, error) {
		calls++
		return stored, nil
	}
	d.cache[wsID] = sorModeCacheEntry{overlay: cached, expiresAt: now.Add(time.Hour)}
	return d, &calls
}

// TestDispatcherWriteVerbsIgnoreAStaleCachedMode is the regression gate for
// the silent-divergence window: an api replica holding a pre-flip 'native'
// answer must not route a MUTATION to the native modules after the workspace
// has been connected to an incumbent elsewhere.
//
// The damaging direction is native → overlay. A mutation taking the stale
// 'native' branch commits to a native table that no overlay read ever serves
// and that never reaches the incumbent: the write appears to succeed, the
// record never changes, and nothing anywhere reports a failure. Cache TTL
// alone cannot close it, because Invalidate reaches only the process that
// committed the flip.
//
// The native provider here is nil, so a verb that wrongly routed native would
// panic rather than quietly pass — the assertion is that none of them do.
func TestDispatcherWriteVerbsIgnoreAStaleCachedMode(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, false /* cached: native */, true /* stored: overlay */)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	writes := map[string]func() error{
		"Create": func() error {
			_, err := d.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson})
			return err
		},
		"Update": func() error {
			_, err := d.Update(ctx, datasource.UpdateInput{Ref: ref})
			return err
		},
		"Archive": func() error {
			_, err := d.Archive(ctx, ref)
			return err
		},
		"AdvanceDeal": func() error {
			_, err := d.AdvanceDeal(ctx, datasource.AdvanceDealInput{})
			return err
		},
		"Merge": func() error {
			_, err := d.Merge(ctx, datasource.MergeInput{Type: datasource.EntityPerson})
			return err
		},
		"PromoteLead": func() error {
			_, _, err := d.PromoteLead(ctx, ids.NewV7(), "manual", nil)
			return err
		},
	}

	for name, call := range writes {
		before := *calls
		if err := call(); err == nil {
			t.Errorf("%s: want the overlay provider's own error, got nil — the verb did not reach a provider", name)
		}
		if *calls == before {
			t.Errorf("%s: answered from the cached mode; a mutation must re-read workspace.x_sor_mode", name)
		}
	}
}

// TestDispatcherReadVerbsStillUseTheCachedMode is the other half of the
// trade: reads keep the cache, because paying a workspace-row read on every
// Read/Search is the cost the cache exists to avoid, and a read served from
// the pre-flip mode costs a stale screen that the next request corrects —
// not a divergent write.
func TestDispatcherReadVerbsStillUseTheCachedMode(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, true /* cached: overlay */, true /* stored: overlay */)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)

	if _, err := d.Read(ctx, datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}); err == nil {
		t.Fatal("Read: want the overlay provider's nil-mirror-store error, got nil")
	}
	if _, err := d.Search(ctx, datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}}); err == nil {
		t.Fatal("Search: want the overlay provider's nil-mirror-store error, got nil")
	}
	if *calls != 0 {
		t.Errorf("cached reads re-queried workspace.x_sor_mode %d time(s); avoiding that on every read is the whole reason the cache exists", *calls)
	}
}
