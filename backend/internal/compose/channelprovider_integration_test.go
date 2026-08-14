// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
)

// A provider not already seeded is inserted, with transport='core' (this
// reconcile only ever handles core connectors — a unit's declared channel is
// its own later slice).
func TestReconcileChannelProvidersInsertsAnUnseenProvider(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	// Restore the pre-registry default in BOTH packages' in-memory snapshots:
	// reconcileChannelProviders below sets them to this test's own composed
	// set, and a later test in this process must not see it.
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DELETE FROM channel_provider WHERE provider = 'fake-core-channel'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
		if _, err := owner.Exec(context.Background(), `DELETE FROM activity_kind WHERE kind = 'fake-core-channel'`); err != nil {
			t.Errorf("cleaning up activity_kind: %v", err)
		}
	})

	// telegram is already seeded by migration 0242; assert the reconcile is a
	// no-op for it and does insert BOTH the activity_kind and channel_provider
	// rows for a genuinely new one — standing in for "core ships a second
	// channel connector" without adding one.
	if err := reconcileChannelProviders(ctx, e.Pool, []string{"telegram", "fake-core-channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders: %v", err)
	}

	var kindExists bool
	if err := owner.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM activity_kind WHERE kind = 'fake-core-channel')`).Scan(&kindExists); err != nil {
		t.Fatalf("querying activity_kind: %v", err)
	}
	if !kindExists {
		t.Fatal("reconcileChannelProviders did not insert the activity_kind row a new provider needs before channel_provider can FK into it")
	}

	var transport string
	if err := owner.QueryRow(ctx,
		`SELECT transport FROM channel_provider WHERE provider = 'fake-core-channel'`).Scan(&transport); err != nil {
		t.Fatalf("querying the inserted row: %v", err)
	}
	if transport != "core" {
		t.Fatalf("transport = %q, want core", transport)
	}

	// Idempotent: calling it again with the SAME set changes nothing and
	// errors on nothing — a role that constructs the registry twice (the
	// worker's one-shot backfill helper does) must not fail its second call.
	if err := reconcileChannelProviders(ctx, e.Pool, []string{"telegram", "fake-core-channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders, second call: %v", err)
	}
}

// A provider whose supplier is gone on a LATER boot is kept, never deleted —
// activity and person_channel_identity rows may still reference it.
func TestReconcileChannelProvidersNeverDeletesARetiredRow(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	// reconcileChannelProviders below sets BOTH packages' in-memory snapshots
	// to the empty set this test passes it; restore the pre-registry default
	// so a later test in this process does not see telegram deregistered.
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})

	if err := reconcileChannelProviders(ctx, e.Pool, []string{}); err != nil {
		t.Fatalf("reconcileChannelProviders with an empty composed set: %v", err)
	}

	var count int
	if err := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT count(*) FROM channel_provider WHERE provider = 'telegram'`).Scan(&count); err != nil {
		t.Fatalf("querying channel_provider: %v", err)
	}
	if count != 1 {
		t.Fatalf("telegram's row was deleted when it dropped out of the composed set (count=%d)", count)
	}
}

// The seam SHIPS, not just the function: NewCaptureRegistry — the real
// composition-root entry point every process role calls — sets both
// activities' and comms' in-memory snapshots as a side effect of registering
// its connectors, not just something a hand-written call to
// reconcileChannelProviders would prove.
func TestNewCaptureRegistrySetsTheActivitiesAndCommsChannelSnapshots(t *testing.T) {
	e := integration.Setup(t)
	// Restore the pre-registry default (not nil/empty): both packages start
	// with {"telegram"} baked in, and a later test in this same process that
	// assumes telegram is still a channel kind must not see the emptied set
	// this test would otherwise leave behind.
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})

	NewCaptureRegistry(e.Pool, nil, CaptureConfig{})

	if !activities.IsChannelKind(capture.ProviderTelegram) {
		t.Error("NewCaptureRegistry did not set activities' channel-provider snapshot")
	}
	if _, capability := comms.SendScopeFor(capture.ProviderTelegram); capability != comms.SendsWithoutScope {
		t.Error("NewCaptureRegistry did not set comms' channel-provider snapshot")
	}
}

// The regression this design's own review round caught: reconcile must run
// over an infra transaction, never a workspace-bound one — NewCaptureRegistry
// is called during normal server construction, which happens BEFORE the
// installation is bootstrapped (no organization exists yet). A workspace-bound
// transaction fails to even resolve which workspace to bind, which is a
// deployment-halting panic on every fresh install, not a corner case.
func TestNewCaptureRegistryReconcilesBeforeTheInstallationIsBootstrapped(t *testing.T) {
	e := integration.Setup(t)
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})

	// Archived, not deleted: identity.Service.InstallationWorkspace counts
	// un-archived workspaces, and integration.Setup's own fixture rows
	// (app_user, team) FK-reference this workspace with ON DELETE RESTRICT, so
	// a DELETE here would fail on the wrong constraint before ever reaching
	// the one this test is about.
	if _, err := integration.OwnerConn(t).Exec(context.Background(),
		`UPDATE workspace SET archived_at = now()`); err != nil {
		t.Fatalf("archiving every workspace to simulate a pre-bootstrap install: %v", err)
	}

	NewCaptureRegistry(e.Pool, nil, CaptureConfig{})

	if !activities.IsChannelKind(capture.ProviderTelegram) {
		t.Error("NewCaptureRegistry did not reconcile with no organization bootstrapped yet")
	}
}
