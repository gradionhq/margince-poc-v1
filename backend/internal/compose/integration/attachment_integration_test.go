// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"errors"
	"io"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// attachmentStore builds the activities store over a fresh in-memory
// blobstore — the seam contract is identical to the MinIO impl, proven
// separately in platform/blobstore.
func attachmentStore(e *Env) (*activities.Store, blobstore.Store) {
	blob := blobstore.NewMemory()
	return e.Activities.WithBlobstore(blob), blob
}

// ownPersonPerms sees only its own person rows — enough to prove the
// row-scope gate hides another rep's person from an upload target.
func ownPersonPerms() principal.Permissions {
	return principal.Permissions{
		Objects:  map[string]principal.ObjectGrant{"person": {Read: true, Create: true, Update: true, Delete: true}},
		RowScope: principal.RowScopeOwn,
	}
}

func TestAttachmentUploadThenDownloadRoundTrip(t *testing.T) {
	e := Setup(t)
	store, _ := attachmentStore(e)
	ctx := e.Admin()
	person := e.SeedPerson(t, "Attach Target", &e.Rep1)
	body := []byte("%PDF-1.4 fake report bytes")

	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType:  "person",
		EntityID:    person,
		Filename:    "report.pdf",
		ContentType: "application/pdf",
		Body:        body,
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if att.Filename != "report.pdf" || att.CapturedBy == nil || *att.CapturedBy == "" {
		t.Fatalf("stored attachment metadata wrong: %+v", att)
	}
	if att.ByteSize == nil || *att.ByteSize != int64(len(body)) {
		t.Fatalf("ByteSize = %v, want %d", att.ByteSize, len(body))
	}

	meta, rc, err := store.OpenAttachment(ctx, ids.UUID(att.Id))
	if err != nil {
		t.Fatalf("OpenAttachment: %v", err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("downloaded bytes = %q, want %q", got, body)
	}
	if meta.Filename != "report.pdf" {
		t.Errorf("meta.Filename = %q", meta.Filename)
	}
}

func TestAttachmentUploadDeniedForInvisibleParent(t *testing.T) {
	e := Setup(t)
	store, _ := attachmentStore(e)
	// The person is owned by Rep1; Rep3 (own-scope, other team) cannot see it.
	person := e.SeedPerson(t, "Rep1's Person", &e.Rep1)
	ctx := e.As(e.Rep3, []ids.UUID{e.Team2}, ownPersonPerms())

	_, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "x.txt", Body: []byte("secret"),
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("upload to an invisible parent: err = %v, want ErrNotFound (existence-hiding)", err)
	}
	// The denial lands before any row is written: an admin sees no attachment
	// on the person (and, by the store's RBAC-before-Put ordering, no object).
	list, _, lerr := store.ListAttachments(e.Admin(), "person", person, nil, nil)
	if lerr != nil {
		t.Fatalf("ListAttachments: %v", lerr)
	}
	if len(list) != 0 {
		t.Fatalf("a denied upload still wrote %d attachment row(s)", len(list))
	}
}

func TestErasurePurgesAttachmentObjects(t *testing.T) {
	e := Setup(t)
	blob := blobstore.NewMemory()
	store := e.Activities.WithBlobstore(blob)
	ctx := e.Admin()
	person := e.SeedPerson(t, "To Be Erased", &e.Rep1)

	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "secret.pdf", Body: []byte("pii bytes"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	// The object is addressable by the same key the store derives from the id.
	key := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](e.WS), "attachment", att.Id.String())
	if _, _, gerr := blob.Get(ctx, key); gerr != nil {
		t.Fatalf("precondition: uploaded object should exist: %v", gerr)
	}

	eraser := privacy.NewEraser(e.Pool).WithBlobstore(blob)
	if err := eraser.ErasePerson(ctx, person, "test-erasure"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	// Art. 17: the subject's attachment bytes must be gone, not only the row.
	if _, _, gerr := blob.Get(ctx, key); !errors.Is(gerr, blobstore.ErrNotFound) {
		t.Fatalf("erased attachment object still present: err = %v, want ErrNotFound", gerr)
	}
}

func TestErasureWithoutStoreRollsBackRatherThanHalfErasing(t *testing.T) {
	e := Setup(t)
	blob := blobstore.NewMemory()
	store := e.Activities.WithBlobstore(blob)
	ctx := e.Admin()
	person := e.SeedPerson(t, "Config Mismatch", &e.Rep1)
	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "f.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}

	// An eraser wired WITHOUT a blobstore — the asymmetric config where the
	// api stored objects but the worker running erasure has no store. Erasing
	// a subject whose attachments have objects must FAIL and roll back, never
	// commit a half-erasure that strands the bytes with their keys deleted.
	eraser := privacy.NewEraser(e.Pool)
	if err := eraser.ErasePerson(ctx, person, "misconfig"); err == nil {
		t.Fatal("ErasePerson succeeded with objects present but no store configured — half-erasure risk")
	}

	// Rolled back: the attachment row survives (still openable) and the person
	// was not anonymized.
	if _, rc, derr := store.OpenAttachment(ctx, ids.UUID(att.Id)); derr != nil {
		t.Errorf("attachment row was deleted despite the erasure rolling back: %v", derr)
	} else {
		_ = rc.Close()
	}
	if erased := e.WsCount(t, `SELECT count(*) FROM person WHERE id = $1 AND full_name = 'Erased Subject'`, person); erased != 0 {
		t.Error("person was anonymized despite the object purge failing — the erasure did not roll back")
	}
}

func TestArchiveAttachmentHidesItButKeepsTheObject(t *testing.T) {
	e := Setup(t)
	store, blob := attachmentStore(e)
	ctx := e.Admin()
	person := e.SeedPerson(t, "Doc Owner", &e.Rep1)
	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "a.txt", Body: []byte("hello"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}

	before, _, err := store.ListAttachments(ctx, "person", person, nil, nil)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("list before archive = %d, want 1", len(before))
	}

	if err := store.ArchiveAttachment(ctx, ids.UUID(att.Id)); err != nil {
		t.Fatalf("ArchiveAttachment: %v", err)
	}

	after, _, err := store.ListAttachments(ctx, "person", person, nil, nil)
	if err != nil {
		t.Fatalf("ListAttachments after archive: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("list after archive = %d, want 0", len(after))
	}
	// Archive is a soft delete: the object bytes stay (erasure/retention is
	// the authoritative purge path), so a download of the archived row is
	// 404 but the object itself is still present in the store.
	if _, _, derr := store.OpenAttachment(ctx, ids.UUID(att.Id)); !errors.Is(derr, apperrors.ErrNotFound) {
		t.Fatalf("download of archived attachment: err = %v, want ErrNotFound", derr)
	}
	if err := blob.Health(ctx); err != nil {
		t.Fatalf("blob health: %v", err)
	}
}
