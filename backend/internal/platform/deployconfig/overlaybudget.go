// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The overlay_budget block of margince.yaml: the per-incumbent OVB
// consumption-meter configuration, its built-in defaults, and the load-time
// safety check that keeps a misconfigured cap from overrunning a quota this
// installation shares with integrations it cannot see. It sits in its own
// file because it is the one section of the deployment file with arithmetic
// of its own — defaults to merge and orderings to enforce — rather than
// values to hand on.

import "fmt"

// OverlayBudget is the per-incumbent OVB consumption-meter configuration
// (overlay-budget chapter): keyed by incumbent name (e.g. "hubspot"), each
// entry pins that incumbent's two windows' ceilings/caps and the shared
// warn/shed fractions. An incumbent with no entry falls back to the
// built-in default (defaultIncumbentBudgets) so an installation that never
// wrote the block still meters HubSpot safely rather than fail-closing
// every force-fresh read. The numeric values are unpinned upstream (the
// corpus leaves per-incumbent calibration open, OVB Parameters appendix);
// these are this build's conservative defaults, tunable here.
type OverlayBudget map[string]IncumbentBudget

// IncumbentBudget is one incumbent's meter config: a per-second Search
// window and a daily REST window, each a ceiling/cap pair (the cap the
// conservative distance below the published ceiling we share with
// integrations we cannot see — OVB-PARAM-3), plus the warn/shed fractions
// of the cap (OVB-PARAM-1/2) shared by both windows.
type IncumbentBudget struct {
	// Search is the per-second search-API window (fixed 1s bucket).
	Search WindowBudget `yaml:"search"`
	// REST is the daily REST-allocation window — a fixed UTC-day bucket
	// (resets at UTC midnight), the meter's "fixed-window counters with
	// expiry" mechanism, not a sliding 24h window.
	REST WindowBudget `yaml:"rest"`
	// WarnFraction/ShedFraction are the consumed/cap ratios at which a
	// charge answers warn then shed (OVB-PARAM-1/2). Zero means "use the
	// spec default" (0.70 / 0.90) — resolved by Effective, not stored as 0.
	WarnFraction float64 `yaml:"warn_fraction"`
	ShedFraction float64 `yaml:"shed_fraction"`
}

// WindowBudget is one rolling window's published ceiling and our own
// (lower) cap. Consumption is metered against the cap; the ceiling is the
// incumbent's published rate limit the cap must stay strictly below.
type WindowBudget struct {
	Ceiling int `yaml:"ceiling"`
	Cap     int `yaml:"cap"`
}

// Default warn/shed fractions when an incumbent leaves them unset
// (OVB-PARAM-1/2 defaults).
const (
	defaultOverlayWarnFraction = 0.70
	defaultOverlayShedFraction = 0.90
)

// defaultIncumbentBudgets is the built-in per-incumbent OVB configuration
// used for any incumbent the YAML does not override. HubSpot: the Search
// API's per-second ceiling (~5 req/s) with a conservative 4 req/s cap
// (AC-overlay-budget-1's "x / 4.0 req/s"), and a fixed UTC-day REST
// allocation (the ~100k/day org ceiling) capped conservatively below it —
// "UTC-day" and not "rolling 24h", because that is what the meter does and
// the difference decides when an exhausted quota comes back.
func defaultIncumbentBudgets() OverlayBudget {
	return OverlayBudget{
		"hubspot": {
			Search:       WindowBudget{Ceiling: 5, Cap: 4},
			REST:         WindowBudget{Ceiling: 100000, Cap: 90000},
			WarnFraction: defaultOverlayWarnFraction,
			ShedFraction: defaultOverlayShedFraction,
		},
	}
}

// EffectiveOverlayBudget merges the operator's overlay_budget block over
// the built-in defaults: a configured incumbent wins entirely, an absent
// one gets its default, and an entry that leaves warn/shed at zero gets
// the spec-default fractions. This is the map the meter is constructed
// from — validate() has already rejected any unsafe explicit values.
func (c Config) EffectiveOverlayBudget() OverlayBudget {
	out := defaultIncumbentBudgets()
	for name, ib := range c.OverlayBudget {
		if ib.WarnFraction == 0 {
			ib.WarnFraction = defaultOverlayWarnFraction
		}
		if ib.ShedFraction == 0 {
			ib.ShedFraction = defaultOverlayShedFraction
		}
		out[name] = ib
	}
	return out
}

// validate rejects an unsafe OVB config at load (OVB-AC-4): each window's
// cap must be strictly below its published ceiling (OVB-PARAM-3), and the
// warn/shed fractions must be ordered inside (0,1] (OVB-PARAM-4) — a
// misconfiguration fails fast at boot rather than silently overrunning the
// shared incumbent quota at runtime. Zero warn/shed is allowed here (it
// means "use the default", resolved by EffectiveOverlayBudget); a non-zero
// value must be in range.
func (ib IncumbentBudget) validate(name string) error {
	for _, w := range []struct {
		label  string
		window WindowBudget
	}{{"search", ib.Search}, {"rest", ib.REST}} {
		if w.window.Ceiling <= 0 || w.window.Cap <= 0 {
			return fmt.Errorf("deployconfig: overlay_budget.%s.%s ceiling and cap must both be positive (got ceiling=%d cap=%d)", name, w.label, w.window.Ceiling, w.window.Cap)
		}
		if w.window.Cap >= w.window.Ceiling {
			return fmt.Errorf("deployconfig: overlay_budget.%s.%s cap %d must be strictly below the published ceiling %d (OVB-PARAM-3)", name, w.label, w.window.Cap, w.window.Ceiling)
		}
	}
	warn, shed := ib.WarnFraction, ib.ShedFraction
	if warn == 0 {
		warn = defaultOverlayWarnFraction
	}
	if shed == 0 {
		shed = defaultOverlayShedFraction
	}
	if !(warn > 0 && warn < shed && shed <= 1) {
		return fmt.Errorf("deployconfig: overlay_budget.%s fractions must satisfy 0 < warn < shed <= 1 (got warn=%g shed=%g, OVB-PARAM-4)", name, warn, shed)
	}
	return nil
}
