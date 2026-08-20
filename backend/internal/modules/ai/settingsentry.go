// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The ai module's own settings declarations (ADR-0090/A135).
//
// Routing is where the tier→model binding LIVES, moving it off a file read
// once at boot. The reason is not tidiness: a binding an operator can only
// change by editing a file on the server and restarting both roles is a
// binding they change rarely and out of band, and the two roles can disagree
// about it in between. In the database there is one answer, and it is the same
// answer for every role that asks.
//
// It is a setting rather than deployment config because re-pointing a tier is
// the admin's decision — it trades cost against quality for their own
// installation — and none of it is destructive or arms a capability. That is
// the line ADR-0061 §2 draws and settingscatalog_test.go enforces.

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// routingSettingsObject is the RBAC object gating the routing binding.
//
// Its own object rather than a shared ai one: what this governs is which
// vendor an installation's text is sent to, which is a different decision from
// reading a model's price, and a role that may see the second has no business
// changing the first.
const routingSettingsObject = "ai_routing"

// RoutingKey is the settings key the binding is stored under, exported so the
// composition root can seed it without re-spelling the string.
const RoutingKey = "ai.routing"

// Routing is the deployment's tier→model binding (ai-operational-spec §1.4).
//
// The default is the ZERO config, which is "unconfigured" and not a fallback
// binding: an installation that has bound no models runs with its AI lanes
// absent, exactly as one with no routing file did. A default that named
// vendors would send an installation's text somewhere nobody chose.
var Routing = settings.Define[RoutingConfig](
	RoutingKey,
	routingSettingsObject,
	"update",
	RoutingConfig{},
	validateStoredRouting,
)

// Definitions is the ai module's contribution to the settings registry.
func Definitions() []settings.Definition {
	return []settings.Definition{Routing}
}

// validateStoredRouting holds a stored binding to the same bar the file
// always had, so a write through the settings surface cannot land a config the
// file loader would have refused at boot.
//
// The zero config is the one exception: it is how "no models are bound" is
// spelled, and refusing it would make the registered default invalid.
func validateStoredRouting(cfg RoutingConfig) error {
	if cfg.Unconfigured() {
		return nil
	}
	// Bounds first, because validate() reads a defaulted width and cannot tell
	// an out-of-range one from a deliberate 0.
	if d := cfg.Embeddings.Dimensions; d < 0 || d > maxEmbedDimensions {
		return fmt.Errorf("ai: routing config: embeddings dimensions %d out of range [1,%d]", d, maxEmbedDimensions)
	}
	return cfg.validate()
}

// Unconfigured reports whether this config binds nothing at all — the state an
// installation is in before anyone has chosen models, and the one the AI lanes
// stay absent for.
//
// Tiers is what decides it: a config carrying a profile and no tiers routes
// nothing, and treating that as configured would build a Router that refuses
// every call it is handed.
func (cfg RoutingConfig) Unconfigured() bool { return len(cfg.Tiers) == 0 }
