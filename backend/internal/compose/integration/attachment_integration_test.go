// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// multipartAttachment builds a multipart/form-data upload body.
func multipartAttachment(t *testing.T, entityType, entityID, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("entity_type", entityType); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("entity_id", entityID); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// TestAttachmentHandlersHTTPRoundTrip drives the /attachments handlers over
// the real HTTP surface (multipart parse, streamed download, status codes),
// end to end against real Postgres + an in-memory store.
func TestAttachmentHandlersHTTPRoundTrip(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "HTTP Attach", &e.Rep1)

	// Upload → 201 + Location + the stored metadata.
	body, ctype := multipartAttachment(t, "person", person.String(), "report.pdf", []byte("PDF-BYTES"))
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", body).WithContext(ctx)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	h.UploadAttachment(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: status %d, body %s", rec.Code, rec.Body.String())
	}
	var att crmcontracts.Attachment
	if err := json.Unmarshal(rec.Body.Bytes(), &att); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if att.Filename != "report.pdf" || rec.Header().Get("Location") == "" {
		t.Fatalf("upload response wrong: %+v (Location %q)", att, rec.Header().Get("Location"))
	}

	// Upload with a bad entity_type → 422 (not a 500).
	badBody, badType := multipartAttachment(t, "widget", person.String(), "x.txt", []byte("y"))
	badReq := httptest.NewRequest(http.MethodPost, "/v1/attachments", badBody).WithContext(ctx)
	badReq.Header.Set("Content-Type", badType)
	badRec := httptest.NewRecorder()
	h.UploadAttachment(badRec, badReq)
	if badRec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad entity_type: status %d, want 422", badRec.Code)
	}

	// Download → 200, exact bytes, Content-Disposition names the file.
	dlRec := httptest.NewRecorder()
	dlReq := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String(), nil).WithContext(ctx)
	h.DownloadAttachment(dlRec, dlReq, att.Id)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download: status %d", dlRec.Code)
	}
	if got := dlRec.Body.String(); got != "PDF-BYTES" {
		t.Errorf("downloaded bytes = %q", got)
	}
	if cd := dlRec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="report.pdf"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}

	// List → 200, one item.
	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/v1/attachments", nil).WithContext(ctx)
	h.ListAttachments(listRec, listReq, crmcontracts.ListAttachmentsParams{
		EntityType: crmcontracts.ListAttachmentsParamsEntityType("person"),
		EntityId:   openapi_types.UUID(person),
	})
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: status %d", listRec.Code)
	}
	var page crmcontracts.AttachmentListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Data) != 1 {
		t.Errorf("list returned %d attachments, want 1", len(page.Data))
	}

	// Delete → 204; a subsequent download → 404.
	delRec := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/attachments/"+att.Id.String(), nil).WithContext(ctx)
	h.DeleteAttachment(delRec, delReq, att.Id)
	if delRec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", delRec.Code)
	}
	goneRec := httptest.NewRecorder()
	goneReq := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String(), nil).WithContext(ctx)
	h.DownloadAttachment(goneRec, goneReq, att.Id)
	if goneRec.Code != http.StatusNotFound {
		t.Errorf("download after delete: status %d, want 404", goneRec.Code)
	}
}

// TestAttachmentHandlersErrorPaths covers the handlers' 404/422 branches.
func TestAttachmentHandlersErrorPaths(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	missing := openapi_types.UUID(ids.NewV7())

	// Download / delete a nonexistent id → 404.
	dl := httptest.NewRecorder()
	h.DownloadAttachment(dl, httptest.NewRequest(http.MethodGet, "/v1/attachments/x", nil).WithContext(ctx), missing)
	if dl.Code != http.StatusNotFound {
		t.Errorf("download missing: status %d, want 404", dl.Code)
	}
	del := httptest.NewRecorder()
	h.DeleteAttachment(del, httptest.NewRequest(http.MethodDelete, "/v1/attachments/x", nil).WithContext(ctx), missing)
	if del.Code != http.StatusNotFound {
		t.Errorf("delete missing: status %d, want 404", del.Code)
	}

	// Upload with no file part → 422.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("entity_type", "person"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("entity_id", ids.NewV7().String()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewRecorder()
	upReq := httptest.NewRequest(http.MethodPost, "/v1/attachments", &buf).WithContext(ctx)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	h.UploadAttachment(up, upReq)
	if up.Code != http.StatusUnprocessableEntity {
		t.Errorf("upload without a file part: status %d, want 422", up.Code)
	}

	// List for a parent the caller cannot see → 404 (existence-hiding), not an
	// empty page that would confirm the entity exists.
	person := e.SeedPerson(t, "Rep1 Only", &e.Rep1)
	repCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, ownPersonPerms())
	lr := httptest.NewRecorder()
	h.ListAttachments(lr, httptest.NewRequest(http.MethodGet, "/v1/attachments", nil).WithContext(repCtx),
		crmcontracts.ListAttachmentsParams{
			EntityType: crmcontracts.ListAttachmentsParamsEntityType("person"),
			EntityId:   openapi_types.UUID(person),
		})
	if lr.Code != http.StatusNotFound {
		t.Errorf("list for an invisible parent: status %d, want 404", lr.Code)
	}
}

// TestAttachmentHandlersAnswer501WithoutAStore proves a role that wired no
// object store declares attachments unavailable rather than nil-derefing.
func TestAttachmentHandlersAnswer501WithoutAStore(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool) // no WithBlobstore
	ctx := e.Admin()
	person := e.SeedPerson(t, "No Store", &e.Rep1)

	body, ctype := multipartAttachment(t, "person", person.String(), "x.txt", []byte("y"))
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", body).WithContext(ctx)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	h.UploadAttachment(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("upload without a store: status %d, want 501", rec.Code)
	}
}

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
	} else if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
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
