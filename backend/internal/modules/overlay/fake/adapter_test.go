// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package fake_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// fixedModified is a deterministic ModifiedAt for fixtures that don't
// assert on wall-clock timing — it keeps these tests off the real clock
// (fake.Rec stamps time.Now internally).
var fixedModified = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func TestFakeOwnersDirectoryAndEmailResolution(t *testing.T) {
	f := fake.New()
	f.SeedOwner("owner-1", "alice@example.com")
	f.SeedOwner("owner-2", "bob@example.com")

	owners, err := f.Owners(context.Background())
	if err != nil {
		t.Fatalf("Owners: %v", err)
	}
	got := map[string]string{}
	for _, o := range owners {
		got[o.ExternalID] = o.Email
	}
	want := map[string]string{"owner-1": "alice@example.com", "owner-2": "bob@example.com"}
	if len(got) != len(want) {
		t.Fatalf("Owners returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for id, email := range want {
		if got[id] != email {
			t.Errorf("Owners[%s] = %q, want %q", id, got[id], email)
		}
	}

	// A seeded owner now resolves by id — the directory and single-lookup
	// paths agree, which is what mirror_user_map's re-verification relies on.
	email, err := f.OwnerEmail(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("OwnerEmail(owner-1): %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("OwnerEmail(owner-1) = %q, want alice@example.com", email)
	}
}

func TestFakeBackfillPagesAllRecords(t *testing.T) {
	f := fake.New()
	for i := 0; i < 250; i++ {
		rec := fake.Rec(fmt.Sprint(i), map[string]any{"full_name": fmt.Sprint(i)})
		rec.ModifiedAt = fixedModified
		f.Seed("person", rec)
	}

	seen, cur := 0, ""
	for {
		p, err := f.Backfill(context.Background(), "person", cur)
		if err != nil {
			t.Fatal(err)
		}
		seen += len(p.Records)
		if p.NextCursor == "" {
			break
		}
		cur = p.NextCursor
	}

	if seen != 250 {
		t.Fatalf("paged %d/250", seen)
	}
}

// TestFakeName pins the fixed-value Name method every overlay.Incumbent
// implementation declares.
func TestFakeName(t *testing.T) {
	f := fake.New()
	if f.Name() != "fake" {
		t.Errorf("Name() = %q, want fake", f.Name())
	}
}

// TestFakeGetReturnsSeededRecordOrACleanError proves Get's happy path and
// its honest not-found error — never a panic on a miss.
func TestFakeGetReturnsSeededRecordOrACleanError(t *testing.T) {
	f := fake.New()
	seed := fake.Rec("1", map[string]any{"first_name": "Ada"})
	seed.ModifiedAt = fixedModified
	f.Seed("contacts", seed)

	rec, err := f.Get(context.Background(), "contacts", "1")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if rec.Fields["first_name"] != "Ada" {
		t.Fatalf("Fields[first_name] = %v, want Ada", rec.Fields["first_name"])
	}

	if _, err := f.Get(context.Background(), "contacts", "does-not-exist"); err == nil {
		t.Fatal("Get: want an error for an unseeded external id, got nil")
	}
}

// TestFakeModifiedFiltersBySinceAndPages proves Modified's since-filter
// and ascending-by-ModifiedAt ordering, distinct from Backfill's plain
// index-cursor paging.
func TestFakeModifiedFiltersBySinceAndPages(t *testing.T) {
	f := fake.New()
	now := fixedModified
	f.Seed("contacts", overlay.Record{ExternalID: "1", Fields: map[string]any{}, ModifiedAt: now.Add(-time.Hour)})
	f.Seed("contacts", overlay.Record{ExternalID: "2", Fields: map[string]any{}, ModifiedAt: now})

	page, err := f.Modified(context.Background(), "contacts", now.Add(-time.Minute), "")
	if err != nil {
		t.Fatalf("Modified: unexpected error: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ExternalID != "2" {
		t.Fatalf("Records = %#v, want exactly the record modified after since", page.Records)
	}
}

// TestFakeDeletionsFilterBySinceAndOrdering proves the deletion feed's
// since-filter AND its ascending-by-DeletedAt ordering: one deletion
// before since is dropped, and the two after it are returned oldest-first
// regardless of the order they were seeded — the removal signal continuous
// sync sweeps to purge mirror rows an incumbent deleted, distinct from the
// live-record Modified feed.
func TestFakeDeletionsFilterBySinceAndOrdering(t *testing.T) {
	f := fake.New()
	now := fixedModified
	// Seeded newest-first to prove Deletions re-sorts ascending.
	f.SeedDeletion("contacts", overlay.Deletion{ExternalID: "newer", DeletedAt: now})
	f.SeedDeletion("contacts", overlay.Deletion{ExternalID: "older", DeletedAt: now.Add(-time.Minute)})
	f.SeedDeletion("contacts", overlay.Deletion{ExternalID: "before", DeletedAt: now.Add(-time.Hour)})

	page, err := f.Deletions(context.Background(), "contacts", now.Add(-2*time.Minute), "")
	if err != nil {
		t.Fatalf("Deletions: unexpected error: %v", err)
	}
	if len(page.Deletions) != 2 {
		t.Fatalf("Deletions = %#v, want exactly the two deleted at or after since", page.Deletions)
	}
	if page.Deletions[0].ExternalID != "older" || page.Deletions[1].ExternalID != "newer" {
		t.Fatalf("Deletions order = [%s, %s], want ascending by DeletedAt [older, newer]",
			page.Deletions[0].ExternalID, page.Deletions[1].ExternalID)
	}
	if page.Deletions[0].ObjectClass != "contacts" {
		t.Errorf("Deletions[0].ObjectClass = %q, want contacts", page.Deletions[0].ObjectClass)
	}
}

// TestFakeAssociationsUnseededTripleAnswersNoEdges proves the honest-gap
// posture: a triple SeedAssoc never recorded answers no edges, not an
// error and not a fabricated one.
func TestFakeAssociationsUnseededTripleAnswersNoEdges(t *testing.T) {
	f := fake.New()
	assocs, err := f.Associations(context.Background(), "deals", "1", "companies")
	if err != nil {
		t.Fatalf("Associations: unexpected error: %v", err)
	}
	if len(assocs) != 0 {
		t.Fatalf("assocs = %#v, want none for an unseeded triple", assocs)
	}
}

// TestFakeOwnerEmailIsHonestlyUnseeded proves OwnerEmail's own
// not-fabricated posture: it always answers an error, since no test
// seeds it (fake/adapter.go's own doc comment).
func TestFakeOwnerEmailIsHonestlyUnseeded(t *testing.T) {
	f := fake.New()
	if _, err := f.OwnerEmail(context.Background(), "owner-1"); err == nil {
		t.Fatal("OwnerEmail: want an error, got nil")
	}
}

// TestFakeBackfillRejectsAMalformedCursor proves parseCursor's error
// path surfaces through Backfill rather than panicking on a garbage
// cursor a caller must never construct by hand.
func TestFakeBackfillRejectsAMalformedCursor(t *testing.T) {
	f := fake.New()
	rec := fake.Rec("1", map[string]any{})
	rec.ModifiedAt = fixedModified
	f.Seed("contacts", rec)
	if _, err := f.Backfill(context.Background(), "contacts", "not-a-number"); err == nil {
		t.Fatal("Backfill: want an error for a malformed cursor, got nil")
	}
}

// TestFakeWriteBackRoundTrip exercises the fake's write seam (Create →
// Update → Archive) end-to-end, plus the drift-check refusal, so the fake is
// a faithful in-memory Incumbent for write-back tests.
func TestFakeWriteBackRoundTrip(t *testing.T) {
	f := fake.New()
	ctx := context.Background()

	createRes, err := f.Create(ctx, "person", map[string]any{"first_name": "Ada"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	created := createRes.Record
	if created.ExternalID == "" || created.ObjectClass != "person" {
		t.Fatalf("Create returned %+v, want a stamped person record", created)
	}
	if created.Fields["first_name"] != "Ada" {
		t.Errorf("Create fields = %+v, want first_name=Ada", created.Fields)
	}
	// The write reports the properties it wrote (the echo-ledger producer input).
	if createRes.WrittenProps["first_name"] != "Ada" {
		t.Errorf("Create WrittenProps = %+v, want first_name=Ada", createRes.WrittenProps)
	}

	// A patch older than the stored record's ModifiedAt is refused
	// (incumbent-wins drift check).
	if _, err := f.Update(ctx, "person", created.ExternalID, map[string]any{"first_name": "Ada2"}, created.ModifiedAt.Add(-time.Hour)); err == nil {
		t.Error("Update with a stale baseline must be refused (version skew)")
	}

	// A patch at or after the record's baseline merges and re-stamps.
	updateRes, err := f.Update(ctx, "person", created.ExternalID, map[string]any{"first_name": "Ada2"}, created.ModifiedAt)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated := updateRes.Record
	if updated.Fields["first_name"] != "Ada2" {
		t.Errorf("Update fields = %+v, want first_name=Ada2", updated.Fields)
	}
	if updateRes.WrittenProps["first_name"] != "Ada2" {
		t.Errorf("Update WrittenProps = %+v, want first_name=Ada2", updateRes.WrittenProps)
	}

	// An empty patch returns the record unchanged and writes nothing (no ledger).
	sameRes, err := f.Update(ctx, "person", created.ExternalID, nil, updated.ModifiedAt)
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	same := sameRes.Record
	if same.Fields["first_name"] != "Ada2" {
		t.Errorf("no-op Update should return the record unchanged, got %+v", same.Fields)
	}
	if len(sameRes.WrittenProps) != 0 {
		t.Errorf("a read-only-fields Update must write nothing, got WrittenProps %+v", sameRes.WrittenProps)
	}

	// Archiving with a baseline older than the record is refused (drift).
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := f.Archive(ctx, "person", created.ExternalID, stale); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Errorf("Archive with a stale baseline: err = %v, want ErrVersionSkew", err)
	}
	if err := f.Archive(ctx, "person", created.ExternalID, same.ModifiedAt); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	// Archiving a now-absent record is an error, never a silent no-op.
	if err := f.Archive(ctx, "person", created.ExternalID, same.ModifiedAt); err == nil {
		t.Error("Archive of an already-removed record must error")
	}
	// Updating an unknown record is an error too.
	if _, err := f.Update(ctx, "person", "nope", map[string]any{"first_name": "x"}, fixedModified); err == nil {
		t.Error("Update of an unknown record must error")
	}
}
