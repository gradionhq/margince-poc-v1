// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// The thread columns reach the wire, and one conversation can be asked for by
// name (AC-company-15).
//
// thread_key has been written by capture since migration 0093 and read only by
// the reply matcher. The timeline could not group by it because the column
// never reached the projection — the substrate was there and the surface was
// not, so every message rendered as its own event.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestThreadColumnsReachTheWire(t *testing.T) {
	e := setupSend(t)
	ctx := e.as(principal.RowScopeAll)
	anchor := e.seedAnchor(t, "msg-1", "thread-A")
	// The two batched-classification columns, set the way capture sets them.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE activity SET capture_label = 'commitment', bulk_mail_attested = true WHERE id = $1`,
		anchor); err != nil {
		t.Fatalf("labelling the anchor: %v", err)
	}

	got, _, err := e.store(nil).ListActivities(ctx, ListActivitiesInput{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, a := range got {
		if ids.UUID(a.Id) != anchor.UUID {
			continue
		}
		found = true
		if a.ThreadKey == nil || *a.ThreadKey != "thread-A" {
			t.Errorf("thread_key = %v, want thread-A — without it the timeline cannot group", a.ThreadKey)
		}
		if a.CaptureLabel == nil || string(*a.CaptureLabel) != "commitment" {
			t.Errorf("capture_label = %v, want commitment", a.CaptureLabel)
		}
		if a.BulkMailAttested == nil || !*a.BulkMailAttested {
			t.Errorf("bulk_mail_attested = %v, want true", a.BulkMailAttested)
		}
	}
	if !found {
		t.Fatal("the seeded activity is not in the list at all")
	}
}

func TestThreadKeyFilterReturnsOneConversation(t *testing.T) {
	e := setupSend(t)
	ctx := e.as(principal.RowScopeAll)
	e.seedAnchor(t, "msg-a1", "thread-A")
	e.seedAnchor(t, "msg-a2", "thread-A")
	e.seedAnchor(t, "msg-b1", "thread-B")
	// A message the provider never threaded. It must not join a conversation
	// just because it has no key of its own.
	e.seedAnchor(t, "msg-none", "")

	wanted := "thread-A"
	got, _, err := e.store(nil).ListActivities(ctx, ListActivitiesInput{ThreadKey: &wanted})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("thread-A returned %d messages, want the 2 that carry its key", len(got))
	}
	for _, a := range got {
		if a.ThreadKey == nil || *a.ThreadKey != wanted {
			t.Errorf("a message with thread_key %v came back for %q", a.ThreadKey, wanted)
		}
	}
}
