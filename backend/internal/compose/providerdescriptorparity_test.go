// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The offline fake and the live Surfe adapter must describe the SAME product.
//
// Every test that exercises pricing, cascades or budget through the fake
// (TestOfflineDescriptorPricesTheCascade, the reservation suites, the whole
// run pipeline) rests on the fake's own claim to "mirror Surfe's shape so the
// fake exercises the same cost table, the same cascade and the same two pools
// the real adapter will". Nothing enforced that, and a divergence in
// CostTable or Cascades would leave every one of those tests green against a
// shape production does not have — the money tests passing hardest where they
// matter least.
//
// A fitness function rather than a point fix (review-loop rule 2): the
// obligation is derived from the two descriptors, so a field added to either
// is compared without anyone remembering to add it here.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/integrations/surfe"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

func TestTheOfflineFakeDescribesTheSameProductAsTheLiveAdapter(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }
	fake := integrations.NewOfflineProvider(0, clock).Descriptor()
	live := surfe.New(clock).Descriptor()

	// The commercial shape: what it costs, what it meters, what it sells. A
	// difference here makes every fake-driven money test a test of fiction.
	if fake.Name != live.Name {
		t.Errorf("provider name: fake %q, live %q", fake.Name, live.Name)
	}
	if fake.Transport != live.Transport {
		t.Errorf("transport: fake %q, live %q — the run state machine differs per transport", fake.Transport, live.Transport)
	}
	if fake.Billing != live.Billing {
		t.Errorf("billing: fake %q, live %q — this decides what a no-match refunds", fake.Billing, live.Billing)
	}
	if fake.EgressHost != live.EgressHost {
		t.Errorf("egress host: fake %q, live %q", fake.EgressHost, live.EgressHost)
	}
	if fake.DefaultPreset != live.DefaultPreset {
		t.Errorf("default preset: fake %q, live %q", fake.DefaultPreset, live.DefaultPreset)
	}
	assertSameCategories(t, "credit pools", poolNames(fake.CreditPools), poolNames(live.CreditPools))
	assertSameCategories(t, "categories", categoryNames(fake.Categories), categoryNames(live.Categories))
	assertSamePresets(t, fake.Presets, live.Presets)
	assertSameCosts(t, fake.CostTable, live.CostTable)
	assertSameCascades(t, fake.Cascades, live.Cascades)

	// The one field that may legitimately differ is the disclosure copy: the
	// fake is not a vendor and links to nothing a customer must read. Every
	// field that decides BEHAVIOUR is compared above.
	if len(live.TermsLinks) == 0 {
		t.Error("the live adapter publishes no terms links, so the settings card can disclose nothing about who is being paid")
	}
}

func poolNames(pools []provider.Pool) []string {
	out := make([]string, len(pools))
	for i, p := range pools {
		out[i] = string(p)
	}
	return out
}

func categoryNames(categories []provider.Category) []string {
	out := make([]string, len(categories))
	for i, c := range categories {
		out[i] = string(c)
	}
	return out
}

// assertSameCategories compares two vocabularies as SETS: the descriptors
// declare them in whatever order reads best, and only membership is a
// behavioural fact.
func assertSameCategories(t *testing.T, what string, fake, live []string) {
	t.Helper()
	inFake, inLive := map[string]bool{}, map[string]bool{}
	for _, v := range fake {
		inFake[v] = true
	}
	for _, v := range live {
		inLive[v] = true
	}
	for v := range inLive {
		if !inFake[v] {
			t.Errorf("%s: the live adapter sells %q and the fake does not — a test exercising it would never run against production's shape", what, v)
		}
	}
	for v := range inFake {
		if !inLive[v] {
			t.Errorf("%s: the fake offers %q and the live adapter does not — tests pass against a category nobody can buy", what, v)
		}
	}
}

func assertSamePresets(t *testing.T, fake, live map[string][]provider.Category) {
	t.Helper()
	for name, liveSet := range live {
		fakeSet, ok := fake[name]
		if !ok {
			t.Errorf("preset %q exists live and not in the fake", name)
			continue
		}
		assertSameCategories(t, "preset "+name, categoryNames(fakeSet), categoryNames(liveSet))
	}
	for name := range fake {
		if _, ok := live[name]; !ok {
			t.Errorf("preset %q exists in the fake and not live", name)
		}
	}
}

func assertSameCosts(t *testing.T, fake, live map[provider.Category]map[provider.Pool]int) {
	t.Helper()
	for category, liveCost := range live {
		fakeCost, ok := fake[category]
		if !ok {
			t.Errorf("cost table: %q is priced live and absent from the fake", category)
			continue
		}
		for pool, n := range liveCost {
			if fakeCost[pool] != n {
				t.Errorf("cost table: %q costs %d %s live and %d in the fake — the reservation the fake tests is not the one production takes",
					category, n, pool, fakeCost[pool])
			}
		}
		for pool, n := range fakeCost {
			if liveCost[pool] != n {
				t.Errorf("cost table: %q costs %d %s in the fake and %d live", category, n, pool, liveCost[pool])
			}
		}
	}
	for category := range fake {
		if _, ok := live[category]; !ok {
			t.Errorf("cost table: %q is priced in the fake and absent live", category)
		}
	}
}

// assertSameCascades compares the fallback rules, which are the subtlest
// money in the descriptor: a cascade's cost, and the categories it excludes,
// both decide what a run may spend before it ever calls out.
func assertSameCascades(t *testing.T, fake, live []provider.Cascade) {
	t.Helper()
	if len(fake) != len(live) {
		t.Fatalf("cascades: fake has %d, live has %d", len(fake), len(live))
	}
	byCategory := map[provider.Category]provider.Cascade{}
	for _, c := range live {
		byCategory[c.Category] = c
	}
	for _, f := range fake {
		l, ok := byCategory[f.Category]
		if !ok {
			t.Errorf("cascade %q exists in the fake and not live", f.Category)
			continue
		}
		if f.After != l.After {
			t.Errorf("cascade %q triggers after %q in the fake and %q live", f.Category, f.After, l.After)
		}
		for pool, n := range l.Cost {
			if f.Cost[pool] != n {
				t.Errorf("cascade %q costs %d %s live and %d in the fake", f.Category, n, pool, f.Cost[pool])
			}
		}
		assertSameCategories(t, "cascade "+string(f.Category)+" exclusions",
			categoryNames(f.Excludes), categoryNames(l.Excludes))
	}
}
