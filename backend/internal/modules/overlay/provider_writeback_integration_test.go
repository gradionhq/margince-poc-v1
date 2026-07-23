// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The write-back engine's real-Postgres proof (AC-OV-4): an overlay write
// is incumbent-first, and the mirror is never marked authoritative ahead of
// the incumbent's ack. These assert the Provider↔mirror behavior — the
// incumbent's own drift check is unit-tested at the adapter seam
// (hubspot.TestAdapterUpdateRefusesOnBaselineDrift). The controllable
// incumbent double lets each test drive the exact ack/reject the Provider
// must honor.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// writeBackIncumbent is a controllable Incumbent for the Provider write
// tests: read verbs are not fixtured (write-back reads baselines from the
// mirror, never the incumbent), and each write verb returns exactly what
// the test configures — so the Provider's incumbent-first contract can be
// asserted against a known ack or reject.
type writeBackIncumbent struct {
	createRec  Record
	createErr  error
	updateRec  Record
	updateErr  error
	archiveErr error
	archived   bool
}

func (w *writeBackIncumbent) Name() string { return "writeback-double" }
func (w *writeBackIncumbent) Backfill(context.Context, string, string) (Page, error) {
	return Page{}, errNotFixtured()
}

func (w *writeBackIncumbent) Modified(context.Context, string, time.Time, string) (Page, error) {
	return Page{}, errNotFixtured()
}

func (w *writeBackIncumbent) Deletions(context.Context, string, time.Time, string) (DeletionPage, error) {
	return DeletionPage{}, errNotFixtured()
}

func (w *writeBackIncumbent) Get(context.Context, string, string) (Record, error) {
	return Record{}, errNotFixtured()
}

func (w *writeBackIncumbent) Associations(context.Context, string, string, string) ([]Assoc, error) {
	return nil, errNotFixtured()
}

func (w *writeBackIncumbent) OwnerEmail(context.Context, string) (string, error) {
	return "", errNotFixtured()
}
func (w *writeBackIncumbent) Owners(context.Context) ([]OwnerRef, error) { return nil, nil }

func (w *writeBackIncumbent) Create(context.Context, string, map[string]any) (Record, error) {
	return w.createRec, w.createErr
}

func (w *writeBackIncumbent) Update(context.Context, string, string, map[string]any, time.Time) (Record, error) {
	return w.updateRec, w.updateErr
}

func (w *writeBackIncumbent) Archive(context.Context, string, string, time.Time) error {
	w.archived = true
	return w.archiveErr
}

func errNotFixtured() error { return errors.New("writeBackIncumbent: read verb not fixtured") }

// providerFor constructs the Provider the write tests drive: a real
// MirrorStore over pool plus the controllable incumbent, wired through the
// production resolver hook.
func providerFor(ms *MirrorStore, inc Incumbent) *Provider {
	p := NewProvider(ms, nil)
	p.SetFreshnessIncumbentResolver(func(context.Context) (Incumbent, error) { return inc, nil })
	return p
}

// writebackOwner is the incumbent owner every write-back fixture maps the
// acting user to — the one spelling shared by mapActorToOwner and each
// seeded row's OwnerExternalID, so the mirror_visibility deny-join lets the
// actor see the rows the write verbs operate on.
const writebackOwner = "owner-1"

// seedActiveConnection inserts the active incumbent_connection a connected
// overlay workspace has — the row the write-back's disconnect fence
// (mirrorWriteResult's WithFence) asserts before re-mirroring, so a write on a
// connected workspace is not spuriously aborted as a teardown race.
func seedActiveConnection(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO incumbent_connection (workspace_id, incumbent, region, credential_ref, status)
			VALUES (current_setting('app.workspace_id')::uuid, 'hubspot', 'eu1', 'ref-writeback', 'active')`)
		return err
	}); err != nil {
		t.Fatalf("seeding active connection: %v", err)
	}
}

// mapActorToOwner maps the acting user to writebackOwner so the
// mirror_visibility deny-join lets it see rows owned by that owner — the
// same visibility setup the freshness fixtures use.
func mapActorToOwner(ctx context.Context, t *testing.T, ms *MirrorStore) {
	t.Helper()
	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("no actor bound")
	}
	if err := ms.UpsertUserMap(ctx, ids.From[ids.UserKind](actor.UserID), "hubspot", writebackOwner, "manual"); err != nil {
		t.Fatalf("mapping actor to owner %s: %v", writebackOwner, err)
	}
}

// TestProviderCreateMirrorsIncumbentResult: Create is incumbent-first — the
// incumbent's returned state is ingested into the mirror so a follow-up
// read sees the record, and the returned ref round-trips to it.
func TestProviderCreateMirrorsIncumbentResult(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	mapActorToOwner(ctx, t, ms)
	seedActiveConnection(ctx, t, pool)

	inc := &writeBackIncumbent{createRec: Record{
		ObjectClass:     "person",
		ExternalID:      "555",
		Fields:          map[string]any{"first_name": "Ada", "full_name": "Ada Lovelace"},
		ModifiedAt:      time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: writebackOwner,
	}}
	p := providerFor(ms, inc)

	ref, err := p.Create(ctx, datasource.CreateInput{
		EntityType: datasource.EntityPerson,
		Fields:     map[string]any{"first_name": "Ada", "last_name": "Lovelace"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec, err := p.Read(ctx, ref)
	if err != nil {
		t.Fatalf("Read after Create: %v", err)
	}
	// The incumbent's returned state must actually be ingested — assert the
	// field content, not just that a row exists.
	var fields map[string]any
	if err := json.Unmarshal(rec.Fields, &fields); err != nil {
		t.Fatalf("decoding read-back fields: %v", err)
	}
	if fields["first_name"] != "Ada" {
		t.Errorf("mirrored first_name = %v, want 'Ada' (the incumbent's created state)", fields["first_name"])
	}
	// A mirror read is never authoritative — the incumbent stays the SoR.
	if rec.Freshness.Authoritative {
		t.Error("a mirrored write result must not claim incumbent authority (T2, AC-OV-5)")
	}
}

// TestProviderUpdateRejectsIncumbentSkewLeavingMirrorUntouched (AC-OV-4):
// when the incumbent rejects the write with version skew, the Provider
// surfaces it to the caller AND leaves the mirror row exactly as it was —
// the mirror is never advanced ahead of an incumbent ack.
func TestProviderUpdateRejectsIncumbentSkewLeavingMirrorUntouched(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	mapActorToOwner(ctx, t, ms)
	seedActiveConnection(ctx, t, pool)

	// Seed the mirror row the caller read (baseline captured here).
	baseline := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := ms.Ingest(ctx, Record{
		ObjectClass:     "person",
		ExternalID:      "555",
		Fields:          map[string]any{"first_name": "Ada", "full_name": "Ada"},
		ModifiedAt:      baseline,
		OwnerExternalID: writebackOwner,
	}); err != nil {
		t.Fatalf("seeding mirror: %v", err)
	}

	inc := &writeBackIncumbent{updateErr: apperrors.ErrVersionSkew}
	p := providerFor(ms, inc)

	ref := datasource.EntityRef{Type: datasource.EntityPerson}
	id, err := externalIDToUUID("555")
	if err != nil {
		t.Fatalf("bridging id: %v", err)
	}
	ref.ID = id

	_, err = p.Update(ctx, datasource.UpdateInput{
		Ref:   ref,
		Patch: map[string]any{"first_name": "Changed"},
	})
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("Update on incumbent skew: err = %v, want ErrVersionSkew", err)
	}

	// The mirror row must be untouched — still the original first_name.
	row, err := ms.Get(ctx, "person", "555")
	if err != nil {
		t.Fatalf("re-reading mirror row: %v", err)
	}
	if row.Fields["first_name"] != "Ada" {
		t.Errorf("mirror first_name = %v, want unchanged 'Ada' after a rejected write", row.Fields["first_name"])
	}
}

// TestProviderUpdateMirrorsResultOnAck: a successful incumbent update is
// re-mirrored, so the mirror reflects the acked state.
func TestProviderUpdateMirrorsResultOnAck(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	mapActorToOwner(ctx, t, ms)
	seedActiveConnection(ctx, t, pool)

	baseline := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := ms.Ingest(ctx, Record{
		ObjectClass: "person", ExternalID: "555",
		Fields:     map[string]any{"first_name": "Ada", "full_name": "Ada"},
		ModifiedAt: baseline, OwnerExternalID: writebackOwner,
	}); err != nil {
		t.Fatalf("seeding mirror: %v", err)
	}

	inc := &writeBackIncumbent{updateRec: Record{
		ObjectClass: "person", ExternalID: "555",
		Fields:     map[string]any{"first_name": "Ada2", "full_name": "Ada2"},
		ModifiedAt: baseline.Add(time.Hour), OwnerExternalID: writebackOwner,
	}}
	p := providerFor(ms, inc)

	id, _ := externalIDToUUID("555")
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: id}
	if _, err := p.Update(ctx, datasource.UpdateInput{Ref: ref, Patch: map[string]any{"first_name": "Ada2"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	row, err := ms.Get(ctx, "person", "555")
	if err != nil {
		t.Fatalf("re-reading mirror: %v", err)
	}
	if row.Fields["first_name"] != "Ada2" {
		t.Errorf("mirror first_name = %v, want 'Ada2' after acked write", row.Fields["first_name"])
	}
}

// TestProviderArchivePurgesMirror: Archive removes the mirror row after the
// incumbent archive so it stops being readable.
func TestProviderArchivePurgesMirror(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	mapActorToOwner(ctx, t, ms)
	seedActiveConnection(ctx, t, pool)

	if err := ms.Ingest(ctx, Record{
		ObjectClass: "person", ExternalID: "555",
		Fields:     map[string]any{"first_name": "Ada", "full_name": "Ada"},
		ModifiedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), OwnerExternalID: writebackOwner,
	}); err != nil {
		t.Fatalf("seeding mirror: %v", err)
	}

	inc := &writeBackIncumbent{}
	p := providerFor(ms, inc)

	id, _ := externalIDToUUID("555")
	if _, err := p.Archive(ctx, datasource.EntityRef{Type: datasource.EntityPerson, ID: id}); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if !inc.archived {
		t.Error("Archive must reach the incumbent")
	}
	if _, err := ms.Get(ctx, "person", "555"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("mirror row after Archive: err = %v, want ErrNotFound (purged)", err)
	}
}
