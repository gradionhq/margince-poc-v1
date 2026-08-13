// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Which provider adapter a process boots with, read from the environment once
// at startup (the websearchhttp.FromEnv pattern). Both roles read the same
// variable, so the api that queues a run and the worker that executes it can
// never disagree about who they are talking to.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/integrations/surfe"
)

// providerModeEnv selects the adapter: "off" (the default), "offline" for the
// deterministic fake, "live" for the real vendor adapter.
const providerModeEnv = "MARGINCE_PROVIDER_SURFE"

// offlinePollsBeforeDone makes the fake's polled transport visible on a dev
// stack: two pending polls before it completes, so in_progress is a state a
// human actually sees on the person page rather than a frame nobody catches.
const offlinePollsBeforeDone = 2

// ProviderRegistryFromEnv builds the adapter registry this process runs with,
// and reports whether one is configured at all.
//
// The DEFAULT is off, and off is a real configuration rather than a
// degradation: with no adapter registered, every provider surface answers
// honestly, no job kind registers, and no code path exists that could reach a
// vendor (PI-AC-9). An unknown value is a boot error — a typo in the mode
// must not silently disable a feature an operator asked for, and must never
// silently ENABLE egress either.
func ProviderRegistryFromEnv(now func() time.Time) (*integrations.Registry, bool, error) {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv(providerModeEnv))); mode {
	case "", "off":
		return nil, false, nil
	case "offline":
		reg, err := integrations.NewRegistry(integrations.NewOfflineProvider(offlinePollsBeforeDone, now))
		if err != nil {
			return nil, false, fmt.Errorf("compose: registering the offline provider: %w", err)
		}
		return reg, true, nil
	case "live":
		reg, err := integrations.NewRegistry(surfe.New(now))
		if err != nil {
			return nil, false, fmt.Errorf("compose: registering the Surfe adapter: %w", err)
		}
		return reg, true, nil
	default:
		return nil, false, fmt.Errorf("compose: %s=%q is not a provider mode (off, offline, live)", providerModeEnv, mode)
	}
}
