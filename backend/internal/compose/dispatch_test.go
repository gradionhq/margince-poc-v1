// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The fan-out is ONE insert, not a loop of them. A partial fan-out that then
// failed the dispatcher would be re-run from the top, and the children that
// had already completed would run a SECOND time — activeSweepStates excludes
// completed, so ByArgs uniqueness does not suppress them.
func TestDispatchWithEnqueuesTheWholeFleetInOneInsert(t *testing.T) {
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7(), ids.NewV7()}
	calls := 0
	var seen []ids.UUID
	insert := func(_ context.Context, params []river.InsertManyParams) error {
		calls++
		for _, p := range params {
			scoped, ok := p.Args.(jobs.WorkspaceScoped)
			if !ok {
				t.Fatalf("dispatcher built %T, which is not workspace-scoped", p.Args)
			}
			seen = append(seen, scoped.WorkspaceID())
		}
		return nil
	}

	if err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("dispatching a healthy fleet: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the fan-out made %d insert calls, want exactly 1 — a loop of single inserts can land partially", calls)
	}
	if len(seen) != len(fleet) {
		t.Fatalf("enqueued %d workspaces, want %d", len(seen), len(fleet))
	}
	for i, ws := range fleet {
		if seen[i] != ws {
			t.Fatalf("workspace %d enqueued as %s, want %s", i, seen[i], ws)
		}
	}
}

// A refused insert must fail the DISPATCHER. Swallowing it would leave the
// fleet un-swept while River recorded the tick as completed, which is the
// exact defect this phase removes one level down.
func TestDispatchWithFailsTheDispatcherWhenTheInsertIsRefused(t *testing.T) {
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7()}
	refused := errors.New("insert refused")
	insert := func(context.Context, []river.InsertManyParams) error { return refused }

	err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor)
	if err == nil {
		t.Fatal("a refused fan-out must surface, so the dispatcher row fails and the tick retries")
	}
	if !errors.Is(err, refused) {
		t.Fatalf("the dispatcher lost the cause: %v", err)
	}
}

// An installation with no live workspace has nothing to dispatch, and River
// rejects an empty InsertMany — so the fan-out must not reach it at all.
func TestDispatchWithEnqueuesNothingForAnEmptyFleet(t *testing.T) {
	called := false
	insert := func(context.Context, []river.InsertManyParams) error {
		called = true
		return nil
	}
	if err := dispatchWith(context.Background(), nil, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("an empty fleet is not a failure: %v", err)
	}
	if called {
		t.Fatal("the fan-out called InsertMany with no params; River refuses an empty batch")
	}
}

// The ladder is capped on purpose: the dispatcher's tick owns the cadence.
func TestWorkspaceSweepOptsCapsTheLadderAndDedupesOnActiveStates(t *testing.T) {
	opts := workspaceSweepOpts("ai_capture", sweepWorkspaceMaxAttempts)
	if opts.MaxAttempts != sweepWorkspaceMaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d — unset, River's 25-rung ladder silently replaces the tick as the retry cadence",
			opts.MaxAttempts, sweepWorkspaceMaxAttempts)
	}
	if opts.Queue != "ai_capture" {
		t.Fatalf("Queue = %q, want the queue the caller named", opts.Queue)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("uniqueness must be by args, or one workspace's job is indistinguishable from another's")
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == "completed" {
			t.Fatal("completed must stay out of the uniqueness window, or a finished pass blocks the next tick")
		}
	}
}

func closeDateWorkspaceArgsFor(ws ids.UUID) river.JobArgs {
	return CloseDateWorkspaceArgs{Workspace: ws}
}
