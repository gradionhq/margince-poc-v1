// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Two numbers answer "is this run still alive", and they must not disagree.
//
// The sweep decides when a running row is abandoned (stuckRunGrace); the rail
// decides when a live occurrence stops being believable (runner.RunStaleAfter).
// If the rail's number is the smaller one, there is a window where the reader
// is told a run has stalled while the sweep still considers it live and will
// not touch it — the reader acts on a verdict the server has not reached.
//
// This binding exists because the two constants live in different packages for
// a real reason: the grace is compose's (it owns the sweep's cadence) and the
// lease is the runner's (it owns what it publishes about its own rows), and
// neither can import the other's rationale. So the relationship is asserted
// rather than assumed.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
)

func TestTheRailNeverCallsARunStaleBeforeTheSweepWould(t *testing.T) {
	if runner.RunStaleAfter < stuckRunGrace {
		t.Fatalf("runner.RunStaleAfter is %s but the sweep's grace is %s — the rail would report a run "+
			"stalled while the sweep still considers it live, so a reader would be handed a verdict the "+
			"server has not reached. Raise the lease, or lower the grace deliberately",
			runner.RunStaleAfter, stuckRunGrace)
	}
}
