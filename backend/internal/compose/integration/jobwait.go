// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
)

// AwaitKindCompleted blocks until a job of the given kind reports completion, or
// the context deadline fires. No polling and no sleep: it reads River's own event
// subscription, so the test waits exactly as long as the job takes instead of
// guessing at a duration that turns into a flake on a slow machine.
//
// Exported because this package and the capture suite package both drive real
// River jobs. A copy per package would be a copy of a loop whose whole point is
// that it does not sleep, which is the kind of thing that decays into one that
// does.
func AwaitKindCompleted(ctx context.Context, t *testing.T, sub <-chan *river.Event, kind string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q to complete: %v", kind, ctx.Err())
		case ev := <-sub:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				return
			}
		}
	}
}
