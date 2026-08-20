// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Reading and replacing the installation's tier→model binding.
//
// The store owns what the transport must not: the RBAC gate, the validation the
// routing file was always held to, and telling whoever is serving that the
// binding changed. A handler that skipped any of the three would produce a
// binding nobody vetted, or one stored and never served.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RoutingStore reads and replaces the stored binding.
type RoutingStore struct {
	settings *settings.Store
	// keys resolves a provider's BYOK secret. It travels with the store for
	// the same reason it travels with a parsed config: a routing document names
	// providers and never their credentials, so the two meet only where a
	// binding is turned into something that can serve.
	keys config.Lookup
}

// NewRoutingStore builds the store over the settings catalog.
func NewRoutingStore(s *settings.Store, keys config.Lookup) *RoutingStore {
	return &RoutingStore{settings: s, keys: keys}
}

// Get reads the stored binding. An installation that has bound nothing reads as
// the zero config rather than an error — that is a state, not a fault.
func (s *RoutingStore) Get(ctx context.Context) (RoutingConfig, error) {
	if err := auth.Require(ctx, routingSettingsObject, principal.ActionRead); err != nil {
		return RoutingConfig{}, err
	}
	return settings.Get(ctx, s.settings, Routing)
}

// Replace stores a whole binding, having held it to the bar the file loader
// applies. The write is audit-only (EVT-NOEVT-3): the settings store stamps the
// audit row, and the closed event catalog defines no routing verb.
//
// It returns the FINALIZED config — defaults applied, version computed — rather
// than what the caller sent, because that is what will be served, and because
// the version is what a caller re-pointing a lane needs to see change.
func (s *RoutingStore) Replace(ctx context.Context, next RoutingConfig) (RoutingConfig, error) {
	if err := auth.Require(ctx, routingSettingsObject, principal.ActionUpdate); err != nil {
		return RoutingConfig{}, err
	}
	// Unconfigured is a legitimate destination: an operator unbinding every
	// model is choosing to run without AI, and it is the state a fresh
	// installation is already in.
	if !next.Unconfigured() {
		var err error
		if next, err = next.finalize(); err != nil {
			return RoutingConfig{}, settings.InvalidValue{
				Setting: RoutingKey, Code: "setting_invalid", Reason: err.Error(),
			}
		}
	}
	if err := settings.Set(ctx, s.settings, Routing, next); err != nil {
		return RoutingConfig{}, err
	}
	return next, nil
}
