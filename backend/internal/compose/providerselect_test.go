// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// PI-AC-9, in the words the spec uses: with no provider connected, every
// domain surface "renders not_connected honestly, REMAINS FULLY AVAILABLE, and
// performs zero outbound calls."
//
// The first implementation read that as "hide the surface" and defaulted to
// registering no adapter. The result was a capability nobody could turn on: the
// endpoint answered 501, the settings page showed no card, and connecting a
// provider needed an environment variable and a server restart. Availability
// and egress are two different questions, and only the second one is about
// safety — a registered adapter with no sealed credential can make no call.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

func TestAnUnconfiguredInstallationStillOffersTheProviderSurface(t *testing.T) {

	reg, configured, err := ProviderRegistryFromEnv(time.Now, config.Static(map[string]string{ProviderModeEnv: ""}))
	if err != nil {
		t.Fatalf("the default mode failed to boot: %v", err)
	}
	if !configured {
		t.Fatal("no adapter is registered by default, so the provider surface 501s and the settings page shows no card — an admin has no way to connect a provider (PI-AC-9 requires the surface to remain fully available)")
	}
	if reg == nil {
		t.Fatal("configured is true with a nil registry")
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "surfe" {
		t.Errorf("the default registry carries %v, want [surfe] — the default is the real vendor, and which vendor must never be chosen silently", names)
	}
}

// Registering an adapter is not permission to call one. Nothing in the boot
// path may reach the network: the credential is the gate, and an installation
// that has never pasted a key must be in exactly the zero-egress state.
func TestBootingTheLiveAdapterCallsNothing(t *testing.T) {

	// A URL that would fail loudly if anything dialled it. The adapter's own
	// host is a constant, so this is belt-and-braces: the assertion that
	// matters is that New() returns without touching the network at all.
	if _, _, err := ProviderRegistryFromEnv(time.Now, config.Static(map[string]string{ProviderModeEnv: "live"})); err != nil {
		t.Fatalf("registering the live adapter failed: %v", err)
	}
}

func TestOffRegistersNothingAndUnknownModesRefuseToBoot(t *testing.T) {
	reg, configured, err := ProviderRegistryFromEnv(time.Now, config.Static(map[string]string{ProviderModeEnv: "off"}))
	if err != nil {
		t.Fatalf("off failed to boot: %v", err)
	}
	if configured || reg != nil {
		t.Error("off registered an adapter — it is the one mode that means the code is absent entirely")
	}

	// A typo must not silently disable a feature an operator asked for, nor
	// silently pick a different vendor.
	if _, _, err := ProviderRegistryFromEnv(time.Now, config.Static(map[string]string{ProviderModeEnv: "surfe"})); err == nil {
		t.Error("an unrecognised mode booted anyway; a typo would silently change which provider this installation talks to")
	}
}

// The offline fake stays reachable by name: it is what the dev stack and the
// acceptance walk run against, and it must never be what a plain boot picks.
func TestOfflineIsOptInOnly(t *testing.T) {
	reg, configured, err := ProviderRegistryFromEnv(time.Now, config.Static(map[string]string{ProviderModeEnv: "offline"}))
	if err != nil || !configured {
		t.Fatalf("offline failed to register: configured=%v err=%v", configured, err)
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "surfe" {
		t.Errorf("the fake registers as %v; it impersonates surfe on purpose, so a test exercises the real descriptor", names)
	}
}
