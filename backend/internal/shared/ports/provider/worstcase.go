// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package provider

import (
	"errors"
	"fmt"
	"sort"
)

// ErrUnsupportedBilling reports a descriptor whose billing basis the platform
// cannot honour yet.
var ErrUnsupportedBilling = errors.New("provider: unsupported billing basis")

// WorstCase computes the per-pool reservation for one run: the most this run
// could possibly cost, including every cascade the frozen policy permits
// (PI-FORM-1).
//
// The whole worst case is reserved ONCE, up front. The alternative — reserving
// the primary pass and topping up when a cascade fires — lets a run pass the
// ceiling check, submit, and only then discover it cannot afford its own
// fallback, which is a charge the customer never authorized.
//
// An unmetered provider reserves nothing: there are no credits to hold. Its
// daily run ceiling still applies, but that is an admission check, not a
// reservation.
func (d Descriptor) WorstCase(requested []Category) (map[Pool]int, error) {
	if d.Billing == BillingPerRecordSubscription {
		return nil, fmt.Errorf("%w: %s needs a per-subject entitlement ledger that is not specified yet", ErrUnsupportedBilling, d.Billing)
	}
	if d.Billing == BillingUnmetered {
		return map[Pool]int{}, nil
	}

	want := make(map[Category]bool, len(requested))
	for _, c := range requested {
		want[c] = true
	}

	cost := map[Pool]int{}
	for c := range want {
		for pool, n := range d.CostTable[c] {
			cost[pool] += n
		}
	}

	// A cascade costs only if the run may actually issue it: its own category
	// must be requested, and so must the category whose empty answer triggers
	// it. Asking for the personal-email fallback without asking for a
	// professional email in the first place is not a request this can bill.
	for _, cas := range d.Cascades {
		if !want[cas.Category] || !want[cas.After] {
			continue
		}
		for pool, n := range cas.Cost {
			cost[pool] += n
		}
	}
	return cost, nil
}

// ValidateSelection checks a saved configuration against what this provider
// actually sells. JSON Schema cannot do this — the category vocabulary is the
// provider's own, so the contract admits any string map and the service is
// what refuses an unsellable one.
func (d Descriptor) ValidateSelection(preset string, selected []Category) error {
	if preset != "" && preset != "custom" {
		if _, ok := d.Presets[preset]; !ok {
			return fmt.Errorf("provider %s: unknown preset %q", d.Name, preset)
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("provider %s: at least one category must be selected", d.Name)
	}
	known := make(map[Category]bool, len(d.Categories))
	for _, c := range d.Categories {
		known[c] = true
	}
	var unknown []string
	for _, c := range selected {
		if !known[c] {
			unknown = append(unknown, string(c))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("provider %s does not offer: %v", d.Name, unknown)
	}
	return nil
}

// ResolvePreset returns the categories a preset names. "custom" resolves to
// the caller's own selection, which is what makes it custom.
func (d Descriptor) ResolvePreset(preset string, custom []Category) []Category {
	if preset == "custom" || preset == "" {
		return custom
	}
	if cats, ok := d.Presets[preset]; ok {
		return cats
	}
	return custom
}

// PoolsInLockOrder returns the pools a cost map touches, sorted. Reservations
// lock pools in this fixed order so two runs touching the same two pools can
// never deadlock by taking them in opposite orders.
func PoolsInLockOrder(cost map[Pool]int) []Pool {
	pools := make([]Pool, 0, len(cost))
	for p := range cost {
		pools = append(pools, p)
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
	return pools
}
