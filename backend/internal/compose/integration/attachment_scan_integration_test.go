// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The attachment scan gate (RD-T05): every new upload starts 'scanning'
// and the download stream is withheld — 409 scan_pending — until a
// Scanner verdict lands via MarkScanResult; 'blocked' withholds it
// terminally — 409 attachment_blocked. The metadata row (get/list/upload
// response) is always disclosed with its scan_status; only the bytes are
// gated.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scanProblem is the RFC 7807 wire shape the scan gate answers with.
type scanProblem struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// uploadScanTestAttachment drives the real multipart handler and returns
// the decoded 201 response.
func uploadScanTestAttachment(ctx context.Context, t *testing.T, h activities.Handlers, entityID ids.UUID, filename string, data []byte) crmcontracts.Attachment {
	t.Helper()
	body, ctype := multipartAttachment(t, "person", entityID.String(), filename, data)
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
	return att
}

// markAttachmentClean drives a clean verdict through the real scan-gate
// seam (Store.MarkScanResult) so a test can exercise a path that gate
// would otherwise block — the extraction read and the accept-write now
// share it with the raw-byte download (defense-in-depth, RD-T05).
func markAttachmentClean(ctx context.Context, t *testing.T, e *Env, id ids.UUID) {
	t.Helper()
	if _, err := e.Activities.MarkScanResult(ctx, id, activities.FakeScanner{Result: "clean"}); err != nil {
		t.Fatalf("MarkScanResult(clean): %v", err)
	}
}

func TestAttachmentScanGateOverHTTP(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Scan Gate", &e.Rep1)

	// A fresh upload's response already carries the gate's starting state.
	att := uploadScanTestAttachment(ctx, t, h, person, "report.pdf", []byte("PDF-BYTES"))
	if att.ScanStatus == nil || *att.ScanStatus != crmcontracts.AttachmentScanStatusScanning {
		t.Fatalf("upload response scan_status = %v, want scanning", att.ScanStatus)
	}

	// While scanning the stream is refused with the contract's exact
	// scan_pending problem — retryable, so the client knows to come back.
	rec := httptest.NewRecorder()
	h.DownloadAttachment(rec, httptest.NewRequest(http.MethodGet, "/v1/attachments/x", nil).WithContext(ctx), att.Id)
	if rec.Code != http.StatusConflict {
		t.Fatalf("download while scanning: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var p scanProblem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode scan_pending problem: %v", err)
	}
	want := scanProblem{
		Type:   "https://errors.gradion.com/scan_pending",
		Status: http.StatusConflict,
		Code:   "scan_pending",
		Detail: "This file is still being scanned; retry the download shortly.",
	}
	if p != want {
		t.Errorf("scan_pending problem = %+v, want %+v", p, want)
	}

	// The metadata row stays disclosed while gated: the list serves it,
	// scan_status visible.
	listRec := httptest.NewRecorder()
	h.ListAttachments(listRec, httptest.NewRequest(http.MethodGet, "/v1/attachments", nil).WithContext(ctx),
		crmcontracts.ListAttachmentsParams{
			EntityType: crmcontracts.ListAttachmentsParamsEntityType("person"),
			EntityId:   openapi_types.UUID(person),
		})
	if listRec.Code != http.StatusOK {
		t.Fatalf("list while scanning: status %d, want 200", listRec.Code)
	}
	var page crmcontracts.AttachmentListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ScanStatus == nil || *page.Data[0].ScanStatus != crmcontracts.AttachmentScanStatusScanning {
		t.Errorf("list row = %+v, want the scanning row disclosed with its scan_status", page.Data)
	}

	// A clean verdict opens the stream: exact bytes flow.
	marked, err := e.Activities.MarkScanResult(ctx, ids.UUID(att.Id), activities.FakeScanner{Result: "clean"})
	if err != nil {
		t.Fatalf("MarkScanResult(clean): %v", err)
	}
	if marked.ScanStatus == nil || *marked.ScanStatus != crmcontracts.AttachmentScanStatusClean {
		t.Fatalf("marked scan_status = %v, want clean", marked.ScanStatus)
	}
	okRec := httptest.NewRecorder()
	h.DownloadAttachment(okRec, httptest.NewRequest(http.MethodGet, "/v1/attachments/x", nil).WithContext(ctx), att.Id)
	if okRec.Code != http.StatusOK {
		t.Fatalf("download after clean verdict: status %d, body %s", okRec.Code, okRec.Body.String())
	}
	if got := okRec.Body.String(); got != "PDF-BYTES" {
		t.Errorf("downloaded bytes = %q, want the uploaded bytes", got)
	}
}

func TestAttachmentBlockedVerdictOverHTTP(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Quarantine Target", &e.Rep1)

	// A blocked verdict quarantines terminally with the contract's exact
	// attachment_blocked problem.
	bad := uploadScanTestAttachment(ctx, t, h, person, "malware.bin", []byte("EVIL"))
	if _, err := e.Activities.MarkScanResult(ctx, ids.UUID(bad.Id), activities.FakeScanner{Result: "blocked"}); err != nil {
		t.Fatalf("MarkScanResult(blocked): %v", err)
	}
	blockedRec := httptest.NewRecorder()
	h.DownloadAttachment(blockedRec, httptest.NewRequest(http.MethodGet, "/v1/attachments/x", nil).WithContext(ctx), bad.Id)
	if blockedRec.Code != http.StatusConflict {
		t.Fatalf("download while blocked: status %d, want 409 (body %s)", blockedRec.Code, blockedRec.Body.String())
	}
	var bp scanProblem
	if err := json.Unmarshal(blockedRec.Body.Bytes(), &bp); err != nil {
		t.Fatalf("decode attachment_blocked problem: %v", err)
	}
	wantBlocked := scanProblem{
		Type:   "https://errors.gradion.com/attachment_blocked",
		Status: http.StatusConflict,
		Code:   "attachment_blocked",
		Detail: "This file was quarantined by the virus scan and cannot be downloaded.",
	}
	if bp != wantBlocked {
		t.Errorf("attachment_blocked problem = %+v, want %+v", bp, wantBlocked)
	}
}

func TestMarkScanResultTransitionsAndAudits(t *testing.T) {
	e := Setup(t)
	store, _ := attachmentStore(e)
	ctx := e.Admin()
	person := e.SeedPerson(t, "Verdict Target", &e.Rep1)

	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "doc.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}

	marked, err := store.MarkScanResult(ctx, ids.UUID(att.Id), activities.FakeScanner{Result: "clean"})
	if err != nil {
		t.Fatalf("MarkScanResult: %v", err)
	}
	if marked.ScanStatus == nil || *marked.ScanStatus != crmcontracts.AttachmentScanStatusClean {
		t.Fatalf("scan_status after verdict = %v, want clean", marked.ScanStatus)
	}

	// The verdict is an audited update carrying the status transition.
	audits := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		WHERE entity_type = 'attachment' AND action = 'update' AND entity_id = $1
		  AND before->>'scan_status' = 'scanning' AND after->>'scan_status' = 'clean'`,
		ids.UUID(att.Id))
	if audits != 1 {
		t.Errorf("scan-verdict audit rows = %d, want exactly 1 update with the scanning→clean transition", audits)
	}

	// A missing id reads as not-found, like every other attachment op.
	if _, err := store.MarkScanResult(ctx, ids.NewV7(), activities.FakeScanner{Result: "clean"}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("MarkScanResult on a missing id: err = %v, want ErrNotFound", err)
	}
}

func TestMarkScanResultRefusesAScanningVerdict(t *testing.T) {
	e := Setup(t)
	store, _ := attachmentStore(e)
	ctx := e.Admin()
	person := e.SeedPerson(t, "No Auto Clean", &e.Rep1)

	att, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "doc.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}

	// 'scanning' is the row's own default, never a verdict: the seam
	// refuses it and the row is left untouched.
	if _, err := store.MarkScanResult(ctx, ids.UUID(att.Id), activities.FakeScanner{Result: "scanning"}); !errors.Is(err, activities.ErrInvalidScanVerdict) {
		t.Fatalf("scanning verdict: err = %v, want ErrInvalidScanVerdict", err)
	}
	still := e.WsCount(t, `SELECT count(*) FROM attachment WHERE id = $1 AND scan_status = 'scanning'`, ids.UUID(att.Id))
	if still != 1 {
		t.Errorf("row changed under a refused verdict: %d rows still 'scanning', want 1", still)
	}
	verdictAudits := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		WHERE entity_type = 'attachment' AND action = 'update' AND entity_id = $1`, ids.UUID(att.Id))
	if verdictAudits != 0 {
		t.Errorf("a refused verdict wrote %d audit update(s), want 0", verdictAudits)
	}
}

func TestMarkScanResultHidesAnInvisibleParent(t *testing.T) {
	e := Setup(t)
	store, _ := attachmentStore(e)
	person := e.SeedPerson(t, "Rep1's Scan Target", &e.Rep1)

	att, err := store.UploadAttachment(e.Admin(), activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "doc.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}

	// Rep3 (own-scope, other team) cannot see the parent person: the
	// verdict path hides the attachment exactly like every other op — 404,
	// never a leak, and no write.
	repCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, ownPersonPerms())
	if _, err := store.MarkScanResult(repCtx, ids.UUID(att.Id), activities.FakeScanner{Result: "clean"}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("MarkScanResult through an invisible parent: err = %v, want ErrNotFound", err)
	}
	still := e.WsCount(t, `SELECT count(*) FROM attachment WHERE id = $1 AND scan_status = 'scanning'`, ids.UUID(att.Id))
	if still != 1 {
		t.Errorf("an existence-hidden verdict still flipped the row (%d rows scanning, want 1)", still)
	}
}
