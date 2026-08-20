// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the watcher DECIDES, judged against a real Router and without a
// database. The read it decides on is covered against a real database in
// compose/integration.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func routingFixture(t *testing.T, model string) ai.RoutingConfig {
	t.Helper()
	// The fake provider throughout: these tests judge the watcher's decision,
	// and a real vendor would add a credential and a network to a question that
	// has neither.
	cfg, err := ai.ParseRouting([]byte(`profile: eu_hosted
tiers:
  local_small: {provider: fake, model: ` + model + `}
  cheap_cloud: {provider: fake, model: ` + model + `}
  premium: {provider: fake, model: ` + model + `}
  frontier: {provider: fake, model: ` + model + `}
embeddings: {provider: fake, model: ` + model + `-embed, dimensions: 8}
`))
	if err != nil {
		t.Fatalf("ParseRouting: %v", err)
	}
	return cfg
}

// watcherServing builds a watcher over a REAL Router bound to cfg — not a
// stand-in, so what these tests exercise is the rebind production performs.
func watcherServing(t *testing.T, cfg ai.RoutingConfig) (*RoutingWatcher, *ai.Router) {
	t.Helper()
	r, err := ai.NewRouter(cfg, nil, ai.DefaultMonthlyTokens, nil, false, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &RoutingWatcher{target: r, log: slog.New(slog.DiscardHandler)}, r
}

// A tick that finds the same binding must do nothing. A rebind drops every
// cached completion, so rebinding unconditionally would empty the cache every
// thirty seconds on an installation nobody has touched.
func TestAnUnchangedBindingIsNotRebound(t *testing.T) {
	same := routingFixture(t, "steady")
	w, _ := watcherServing(t, same)
	if w.applyIfChanged(context.Background(), same) {
		t.Error("rebound an unchanged binding; every cached completion would be dropped each tick")
	}
}

func TestAChangedBindingIsAdopted(t *testing.T) {
	w, router := watcherServing(t, routingFixture(t, "before"))
	next := routingFixture(t, "after")

	if !w.applyIfChanged(context.Background(), next) {
		t.Fatal("a changed binding was not adopted")
	}
	if router.RoutingVersion() != next.RoutingVersion() {
		t.Errorf("serving %q, want %q", router.RoutingVersion(), next.RoutingVersion())
	}
	if m, ok := router.CurrentModelForTier(ai.TierPremium); !ok || m.Model != "after" {
		t.Errorf("premium = %+v ok=%v; the rebind did not reach the bound models", m, ok)
	}
}

// The binding was deleted out from under a running role. Keep serving: an
// operator removing one is choosing what the NEXT boot does, and an
// installation whose AI stopped answering with nothing written anywhere is the
// worst way to learn that.
func TestAVanishedBindingLeavesTheRunningOneServing(t *testing.T) {
	w, router := watcherServing(t, routingFixture(t, "still-serving"))
	before := router.RoutingVersion()

	if w.applyIfChanged(context.Background(), ai.RoutingConfig{}) {
		t.Error("unbound the running lanes because the stored binding vanished")
	}
	if router.RoutingVersion() != before {
		t.Errorf("version moved to %q, want the running %q", router.RoutingVersion(), before)
	}
	if _, ok := router.CurrentModelForTier(ai.TierPremium); !ok {
		t.Error("premium came unbound; the running binding must survive a vanished stored one")
	}
}

// A role with nothing to keep current gets a nil watcher, and a nil watcher is
// inert. This is the ORDINARY boot of a fresh installation, not an edge: one
// that has bound no models resolves no path, every role still starts a watcher,
// and ModelPath.Router takes a value receiver — so reaching through the nil
// path dereferences it. That panicked the api on boot until the nil handling
// moved in here, which is why it is asserted rather than assumed.
func TestAWatcherWithNothingToRebindIsInert(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	// A non-nil pool, so the PATH guard is what each case exercises. Passing nil
	// here would return on the pool check and leave the guard under test
	// unreached — the test would pass for the wrong reason, which is how it
	// read before a mutation showed it surviving the guard's removal. The pool
	// is only stored, never dialled, so a zero value is enough.
	pool := &pgxpool.Pool{}
	for name, path := range map[string]*ModelPath{
		"an installation that resolved no path": nil,
		"a path that binds no router":           {},
	} {
		if w := NewRoutingWatcher(pool, path, nil, log); w != nil {
			t.Errorf("%s: got a watcher; there is nothing to keep current", name)
		}
	}
	if w := NewRoutingWatcher(nil, &ModelPath{}, nil, log); w != nil {
		t.Error("a watcher with no pool must be nil; there is nothing to read")
	}
	var nothing *RoutingWatcher
	nothing.Recheck(context.Background())
	nothing.Run(context.Background())
}
