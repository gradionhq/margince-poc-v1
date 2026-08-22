// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package geocode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// A SLOW PROVIDER is not a shutdown, and the difference decides whether a
// company keeps its attempts.
//
// Both surface as a deadline: the http.Client's own timeout reports
// context.DeadlineExceeded, exactly as a cancelled parent would. So a caller
// that tests the ERROR cannot tell "we were stopped" from "the provider did
// not answer in time" — and the second is a real failed lookup that should be
// counted, while the first learned nothing and must not be.
//
// The caller therefore asks the CONTEXT, which is live here and done for a
// shutdown. This test pins the shape that makes that distinction available.
func TestASlowProviderLooksLikeADeadlineButTheContextIsLive(t *testing.T) {
	// Held on a channel rather than a sleep: the handler blocks until the test
	// releases it, so the client's timeout is what ends the request and the
	// test never waits on a clock.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)

	ctx := context.Background()
	client := NewNominatim(slow.URL, &http.Client{Timeout: 50 * time.Millisecond})
	_, _, err := client.Resolve(ctx, "Anywhere")
	if err == nil {
		t.Fatal("a provider that never answered returned no error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a slow provider answered %v, want a deadline — the shape a caller must "+
			"not confuse with a shutdown", err)
	}
	if ctx.Err() != nil {
		t.Error("the context is done after only the HTTP call timed out; the caller would " +
			"read a slow provider as a shutdown and never count the failed lookup")
	}
}
