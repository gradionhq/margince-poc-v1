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
	db := InstallationDB(e.Pool)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `DELETE FROM channel_provider WHERE provider = 'fake-core-channel'`)
		_, _ = owner.Exec(context.Background(), `DELETE FROM activity_kind WHERE kind = 'fake-core-channel'`)
	})

	// telegram is already seeded by migration 0239; assert the reconcile is a
	// no-op for it and does insert a genuinely new one. activity_kind must
	// already carry the new kind for the FK to succeed — seed it directly,
	// standing in for "core ships a second channel connector" without adding
	// one.
	if _, err := owner.Exec(ctx, `INSERT INTO activity_kind (kind) VALUES ('fake-core-channel')`); err != nil {
		t.Fatalf("seeding activity_kind for the test: %v", err)
	}

	if err := reconcileChannelProviders(ctx, db, []string{"telegram", "fake-core-channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders: %v", err)
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
	if err := reconcileChannelProviders(ctx, db, []string{"telegram", "fake-core-channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders, second call: %v", err)
	}
}

// A provider whose supplier is gone on a LATER boot is kept, never deleted —
// activity and person_channel_identity rows may still reference it.
func TestReconcileChannelProvidersNeverDeletesARetiredRow(t *testing.T) {
	e := integration.Setup(t)
	db := InstallationDB(e.Pool)
	ctx := context.Background()

	if err := reconcileChannelProviders(ctx, db, []string{}); err != nil {
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
	defer activities.SetChannelProviders(nil)
	defer comms.SetChannelProviders(nil)

	NewCaptureRegistry(e.Pool, nil, CaptureConfig{})

	if !activities.IsChannelKind(capture.ProviderTelegram) {
		t.Error("NewCaptureRegistry did not set activities' channel-provider snapshot")
	}
	if _, capability := comms.SendScopeFor(capture.ProviderTelegram); capability != comms.SendsWithoutScope {
		t.Error("NewCaptureRegistry did not set comms' channel-provider snapshot")
	}
}
