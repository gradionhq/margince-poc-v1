// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Which provider adapter a process boots with, read from the environment once
// at startup (the websearchhttp.FromEnv pattern). Both roles read the same
// variable, so the api that queues a run and the worker that executes it can
// never disagree about who they are talking to.

import (
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/integrations/surfe"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// ProviderModeEnv selects WHICH adapter this process talks to: "live" (the
// default, the real vendor), "offline" for the deterministic fake, and "off"
// to register none at all.
//
// It does not decide whether the FEATURE exists. That is the connection's
// business, and an admin's to change from the settings page.
// ProviderModeEnv is exported so the composition roots can declare it as part
// of their configurable surface without spelling the string a second time.
const ProviderModeEnv = "MARGINCE_PROVIDER_SURFE"

// offlinePollsBeforeDone makes the fake's polled transport visible on a dev
// stack: two pending polls before it completes, so in_progress is a state a
// human actually sees on the person page rather than a frame nobody catches.
const offlinePollsBeforeDone = 2

// ProviderRegistryFromEnv builds the adapter registry this process runs with,
// and reports whether one is configured at all.
//
// The DEFAULT is live, because PI-AC-9 requires the surface to REMAIN FULLY
// AVAILABLE with no provider connected — rendering not_connected honestly and
// making no outbound call. Registering an adapter is what puts the card on the
// settings page; it is not what permits egress.
//
// What permits egress is a sealed credential, and nothing else. With no key
// there is no call any adapter could make: Connect verifies before it seals,
// and every submit and poll re-reads the credential under the connection lock
// (poll.go leaseForPoll) and abandons when it is gone. So an admin who has
// never pasted a key is in exactly the zero-egress state, while still being
// able to SEE that the feature exists and connect it themselves.
//
// An earlier version defaulted to off and wired nothing, which made the whole
// capability invisible: the endpoint answered 501 and the settings page showed
// no card, so connecting a provider required an environment variable and a
// server restart. That is a build flag wearing a setting's clothes.
//
// "off" survives for a deployment that wants the code absent entirely, and an
// unknown value is still a boot error — a typo must not silently disable a
// feature an operator asked for, nor silently choose the wrong vendor.
func ProviderRegistryFromEnv(now func() time.Time, env config.Lookup) (*integrations.Registry, bool, error) {
	switch mode := strings.ToLower(strings.TrimSpace(env(ProviderModeEnv))); mode {
	case "off":
		return nil, false, nil
	case "offline":
		reg, err := integrations.NewRegistry(integrations.NewOfflineProvider(offlinePollsBeforeDone, now))
		if err != nil {
			return nil, false, fmt.Errorf("compose: registering the offline provider: %w", err)
		}
		return reg, true, nil
	case "", "live":
		reg, err := integrations.NewRegistry(surfe.New(now))
		if err != nil {
			return nil, false, fmt.Errorf("compose: registering the Surfe adapter: %w", err)
		}
		return reg, true, nil
	default:
		return nil, false, fmt.Errorf("compose: %s=%q is not a provider mode (off, offline, live)", ProviderModeEnv, mode)
	}
}
