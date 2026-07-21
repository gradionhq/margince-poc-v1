// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// TestProviderReadServesFromTheMirror proves Read is honest end to end:
// it goes through MirrorStore.Get (the visibility-joined path, never the
// visibility-blind getRaw), and the datasource.Record it builds carries
// Authoritative:false plus the mirror's own LastSyncedAt — never
// datasource.NewRecord's hardcoded Authoritative:true. It needs a real,
// migrated Postgres (RLS + the mirror_visibility deny-join), so it is
// gated behind //go:build integration like the rest of this package's
// mirror-backed tests.
func TestProviderReadServesFromTheMirror(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	p := NewProvider(store, nil)

	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("testWorkspaceCtx did not bind an actor")
	}

	const objectClass = "person"
	const externalID = "100214862042"
	baseline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	userA := ids.From[ids.UserKind](actor.UserID)
	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass:     objectClass,
		ExternalID:      externalID,
		Fields:          map[string]any{"firstname": "Christian"},
		ModifiedAt:      baseline,
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the fixture record: %v", err)
	}

	id, err := externalIDToUUID(externalID)
	if err != nil {
		t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
	}
	rec, err := p.Read(ctx, datasource.EntityRef{Type: datasource.EntityPerson, ID: id})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if rec.Freshness.Authoritative {
		t.Fatal("an overlay mirror read must never claim Authoritative:true")
	}
	if !rec.Freshness.LastSyncedAt.After(baseline) && !rec.Freshness.LastSyncedAt.Equal(baseline) {
		t.Fatalf("Freshness.LastSyncedAt must come from the mirror row, got %v (ingested at %v)", rec.Freshness.LastSyncedAt, baseline)
	}
	if rec.Ref.Type != datasource.EntityPerson || rec.Ref.ID != id {
		t.Fatalf("Ref mismatch: got %+v", rec.Ref)
	}

	var fields map[string]any
	if err := json.Unmarshal(rec.Fields, &fields); err != nil {
		t.Fatalf("unmarshaling Record.Fields: %v", err)
	}
	if fields["firstname"] != "Christian" {
		t.Fatalf("Record.Fields did not round-trip the mirror row: %+v", fields)
	}
}
