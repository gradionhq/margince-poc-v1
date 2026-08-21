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
//
// It SURVIVES a data reset, for the reason AsInstallationIdentity names: it is
// a value bootstrap takes from the deployment configuration, like the
// installation's name and currency. A reset wipes an installation's data, not
// the decision about which vendor may process it — and wiping it would be
// quiet, because a dev stack re-seeds the binding from its routing file on the
// next boot while a production installation, which has no file, would simply
// come back with its AI lanes gone.
var Routing = settings.Define[RoutingConfig](
	RoutingKey,
	routingSettingsObject,
	"update",
	RoutingConfig{},
	validateStoredRouting,
).AsInstallationIdentity()

// Definitions is the ai module's contribution to the settings registry.
func Definitions() []settings.Definition {
	return append([]settings.Definition{Routing}, keyDefinitions()...)
}

// validateStoredRouting holds a stored binding to the same bar the file always
// had, so a write through the settings surface cannot land a config the file
// loader would have refused at boot.
//
// The ZERO config is the one exception, and only the zero one: it is how "no
// models are bound" is spelled and it is the registered default, which has to
// validate. A document that sets a profile and binds no tiers is NOT that — the
// file loader refuses it ("no tiers bound"), and accepting it here would store
// something an operator wrote, that reads as configured, and that routes
// nothing. Silently doing nothing is the failure this surface exists to end.
func validateStoredRouting(cfg RoutingConfig) error {
	if cfg.zero() {
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
// every call it is handed. This is the RUNTIME question — "is there anything to
// serve" — which is a different question from whether a document may be stored;
// see zero.
func (cfg RoutingConfig) Unconfigured() bool { return len(cfg.Tiers) == 0 }

// zero reports whether nothing at all was set, which is the only document the
// validator exempts from the file loader's bar.
//
// Deliberately narrower than Unconfigured. Both mean "serves nothing", but a
// document that names a profile is one somebody WROTE, and storing it
// unexamined would accept a half-bound binding that reads as configured and
// routes nothing.
func (cfg RoutingConfig) zero() bool {
	e := cfg.Embeddings
	return cfg.Profile == "" && len(cfg.Tiers) == 0 &&
		e.Provider == "" && e.Model == "" && e.BaseURL == "" && len(e.Input) == 0 && e.Dimensions == 0
}
