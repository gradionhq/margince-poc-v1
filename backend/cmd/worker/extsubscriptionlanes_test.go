// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Which listeners this role runs, and what one it cannot resolve costs the
// others.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

func noopHandler(context.Context, extension.Runtime, extension.Delivery) error { return nil }

// TestOneUnresolvableListenerDoesNotCostTheOthersTheirLane.
//
// A listener whose group cannot be built is a defect in this binary — the boot
// preflights every declared type against the catalog, so the two disagreeing is
// not a deployment's doing. What matters is the SHAPE of the response: it is
// logged and skipped, so a unit-sized fault stays unit-sized, where a return or
// a fatal would take every other unit's deliveries down with it.
func TestOneUnresolvableListenerDoesNotCostTheOthersTheirLane(t *testing.T) {
	t.Cleanup(func() { compose.SetComposedSubscriptionsForTest(nil) })
	compose.SetComposedSubscriptionsForTest([]compose.ComposedSubscription{
		{
			Unit: "alpha", Version: "1.0.0",
			Sub: extension.Subscription{Name: "unroutable", Events: []string{"invoice.created"}, Handle: noopHandler},
		},
		{
			Unit: "beta", Version: "1.0.0",
			Sub: extension.Subscription{Name: "listens", Events: []string{"activity.archived"}, Handle: noopHandler},
		},
	})

	var announced bytes.Buffer
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	lanes := extensionSubscriptionLanes(nil, logger, &announced)

	if len(lanes) != 1 {
		t.Fatalf("resolved %d lanes, want only the routable one", len(lanes))
	}
	if lanes[0].group.Name != "cg:ext-beta-listens" {
		t.Errorf("the surviving lane consumes %q, want beta's", lanes[0].group.Name)
	}
	// The one that was skipped is SAID, and says which listener it was: nothing
	// else in the process will ever mention it again.
	for _, want := range []string{"alpha", "unroutable"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the skip does not name %q: %s", want, logged.String())
		}
	}
	// And the operator's boot output names only what is actually running.
	if strings.Contains(announced.String(), "unroutable") {
		t.Errorf("the boot announced a lane it did not start: %s", announced.String())
	}
	if !strings.Contains(announced.String(), "cg:ext-beta-listens") {
		t.Errorf("the boot did not announce the lane it started: %s", announced.String())
	}
}

// With nothing composed there is nothing to run, and nothing said about it.
func TestNoSubscriptionsStartNoLanes(t *testing.T) {
	t.Cleanup(func() { compose.SetComposedSubscriptionsForTest(nil) })
	compose.SetComposedSubscriptionsForTest(nil)
	var announced bytes.Buffer
	if lanes := extensionSubscriptionLanes(nil, slog.New(slog.DiscardHandler), &announced); len(lanes) != 0 {
		t.Fatalf("resolved %d lanes with nothing composed", len(lanes))
	}
	if announced.Len() != 0 {
		t.Errorf("the boot announced %q with no listener composed", announced.String())
	}
}
