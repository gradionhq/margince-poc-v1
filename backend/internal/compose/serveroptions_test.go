// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Option's own wiring-only unit-level proof: each Option is a plain
// field assignment/rebuild that never touches the pool at construction
// time (the module constructors it calls — approvals.NewService,
// people.NewStore, briefs.Handlers.WithL2Ranker — all just store the
// pool reference for later use), so a nil pool and nil PageFetcher/Brain
// are safe here: this test never calls the wired handler, only proves
// the Option actually set what it documents.

import (
	"context"
	"testing"
)

func TestWithBusReadySetsTheProbe(t *testing.T) {
	s := &Server{}
	var called bool
	WithBusReady(func(ctx context.Context) error { called = true; return nil })(s, nil)

	if s.busReady == nil {
		t.Fatal("WithBusReady did not set busReady")
	}
	if err := s.busReady(context.Background()); err != nil {
		t.Fatalf("busReady: unexpected error: %v", err)
	}
	if !called {
		t.Fatal("busReady did not invoke the check function WithBusReady was given")
	}
}

func TestWithColdStartWiresTheEngine(t *testing.T) {
	s := &Server{}
	WithColdStart(nil, nil)(s, nil)
	if s.coldstartHandlers.engine == nil {
		t.Fatal("WithColdStart did not wire a coldStartEngine")
	}
}

func TestWithScrapeWiresTheEngine(t *testing.T) {
	s := &Server{}
	WithScrape(nil, nil)(s, nil)
	if s.scrapeHandlers.engine == nil {
		t.Fatal("WithScrape did not wire a scrapeEngine")
	}
}
