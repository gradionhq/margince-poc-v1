// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestJobKindsAreStable(t *testing.T) {
	if got := (CloseDateSweepArgs{}).Kind(); got != "close_date_sweep" {
		t.Errorf("CloseDateSweepArgs.Kind() = %q, want close_date_sweep", got)
	}
	if got := (FollowUpReconcileArgs{}).Kind(); got != "follow_up_reconcile" {
		t.Errorf("FollowUpReconcileArgs.Kind() = %q, want follow_up_reconcile", got)
	}
	if got := (GmailSyncArgs{}).Kind(); got != "gmail_sync" {
		t.Errorf("GmailSyncArgs.Kind() = %q, want gmail_sync", got)
	}
	if got := (OverlayReconcileArgs{}).Kind(); got != "overlay_reconcile" {
		t.Errorf("OverlayReconcileArgs.Kind() = %q, want overlay_reconcile", got)
	}
	if got := (TimeScanArgs{}).Kind(); got != "time_scan" {
		t.Errorf("TimeScanArgs.Kind() = %q, want time_scan", got)
	}
}

// TestReconcileConnectionRejectsANonHubSpotIncumbent proves
// reconcileConnection's first, no-DB-touched guard: branch 1 wires only
// HubSpot (design.md §2 D2/D3), so a due connection naming any other
// incumbent is an honest, named gap — returned before the vaulted token
// is even resolved, which is why this is safe to call with a nil vault/
// mirror store/meter/logger in a unit test.
func TestReconcileConnectionRejectsANonHubSpotIncumbent(t *testing.T) {
	d := overlay.DueOverlayConnection{
		Workspace: ids.WorkspaceID{UUID: ids.NewV7()},
		Incumbent: "salesforce",
	}
	err := reconcileConnection(context.Background(), nil, nil, nil, nil, nil, d, nil)
	if err == nil {
		t.Fatal("reconcileConnection: want an error for a non-HubSpot incumbent, got nil")
	}
}

// failingVault is a keyvault.Vault stub whose Get always fails —
// reconcileConnection's own "resolving the vaulted token" error path,
// which needs no real Postgres (it returns before ms/meter are ever
// touched).
type failingVault struct{ keyvault.Vault }

func (failingVault) Get(context.Context, ids.WorkspaceID, keyvault.Ref) ([]byte, error) {
	return nil, errors.New("failingVault: simulated resolution failure")
}

// TestReconcileConnectionSurfacesAVaultResolutionFailure proves
// reconcileConnection's second guard: a HubSpot connection whose vaulted
// token fails to resolve stops the sweep for this connection entirely
// (an honest "there is no adapter to sweep ANYTHING with," per the
// function's own doc) rather than silently skipping to the object-class
// loop with an empty token.
func TestReconcileConnectionSurfacesAVaultResolutionFailure(t *testing.T) {
	d := overlay.DueOverlayConnection{
		Workspace: ids.WorkspaceID{UUID: ids.NewV7()},
		Incumbent: "hubspot",
		Region:    "eu1",
	}
	err := reconcileConnection(context.Background(), nil, failingVault{}, nil, nil, nil, d, nil)
	if err == nil {
		t.Fatal("reconcileConnection: want an error when the vaulted token fails to resolve, got nil")
	}
}

// TestUniquenessWindowExcludesCompleted is the load-bearing invariant: the
// periodic passes suppress a duplicate only while a prior run is in flight,
// never after it completes — otherwise a completed 24h sweep would block the
// next day's run until the completed row is cleaned out. It must also keep
// the states River requires when ByState is set.
func TestUniquenessWindowExcludesCompleted(t *testing.T) {
	have := map[rivertype.JobState]bool{}
	for _, s := range activeSweepStates {
		have[s] = true
	}

	if have[rivertype.JobStateCompleted] {
		t.Error("activeSweepStates includes JobStateCompleted — a completed sweep would block the next scheduled run")
	}

	// River requires these states whenever ByState is set explicitly.
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
	} {
		if !have[required] {
			t.Errorf("activeSweepStates omits required state %q", required)
		}
	}
}

// TestReconcileWorkerCtxBindsActorAndCorrelation guards the regression
// commit 38fea75 fixed: overlay.Reconcile's emit path (reconcile.go's
// emitMirrorConflict, via storekit.LogSystem/Emit) fails unless the
// context carries a bound actor AND correlation id, not just the
// workspace. overlayReconcileWorker.Work previously bound only
// WorkspaceID — production emits failed silently while the integration
// test passed, because that test drove Reconcile through an
// over-bound testWorkspaceCtx that happened to carry both. This test
// pins reconcileWorkerCtx's contract directly: drop either
// principal.WithActor or principal.WithCorrelationID from the helper
// and this test fails, independent of any test harness's own
// over-binding.
func TestReconcileWorkerCtxBindsActorAndCorrelation(t *testing.T) {
	wsID := ids.WorkspaceID{UUID: ids.NewV7()}

	got := reconcileWorkerCtx(context.Background(), wsID)

	gotWS, ok := principal.WorkspaceID(got)
	if !ok {
		t.Fatal("reconcileWorkerCtx did not bind a workspace id")
	}
	if gotWS != wsID.UUID {
		t.Errorf("bound workspace id = %v, want %v", gotWS, wsID.UUID)
	}

	actor, ok := principal.Actor(got)
	if !ok {
		t.Fatal("reconcileWorkerCtx did not bind an actor — overlay.Reconcile's emitMirrorConflict requires one")
	}
	if actor.Type != principal.PrincipalSystem || actor.ID == "" {
		t.Errorf("bound actor = %+v, want a non-empty system principal", actor)
	}

	if _, ok := principal.CorrelationID(got); !ok {
		t.Fatal("reconcileWorkerCtx did not bind a correlation id — overlay.Reconcile's emitMirrorConflict requires one")
	}
}
