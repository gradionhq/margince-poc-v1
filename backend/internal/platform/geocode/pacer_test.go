// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package geocode

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A shutdown cuts the wait short, and the caller must be able to tell.
//
// This is the shape that cost six of an installation's companies their
// coordinates. The pacer holds a lookup for up to the policy interval before
// the request is even built; when the worker stops, every lookup queued behind
// the current one comes back from Wait with the context's error. The caller
// that treats that as a failure of the ADDRESS burns one of its three attempts
// and sets a day-long backoff for a request that never reached the provider.
//
// Wait must therefore return an error that errors.Is(context.Canceled) rather
// than something of its own, because the caller's whole decision rests on
// telling "we were stopped" from "the provider said no".
func TestAStoppedWaitSaysItWasStopped(t *testing.T) {
	p := NewPacer(time.Hour)
	// Take the first slot, so the second caller has to wait out the interval.
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("the first wait answered %v, want the slot", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	stop()
	err := p.Wait(ctx)
	if err == nil {
		t.Fatal("a stopped wait answered nil, so the caller proceeds to ask a provider " +
			"nobody is waiting on")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped wait answered %v, want it to say it was cancelled — a caller "+
			"that cannot tell records the address as failed and spends one of its attempts", err)
	}
}
