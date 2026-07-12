// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The extraction read + request-access (RD-T10): both ride the activities
// surface, gated identically to every other attachment op (parent
// invisible/missing → 404). The extraction read is a pure evidence-or-omit
// projection over the injected extraction.Extractor seam — honestly empty
// when none is wired — and now shares the raw-byte download's scan gate
// (defense-in-depth, RD-T05): 'scanning'/'blocked' refuse with the same
// typed 409s, before the extractor ever sees the bytes. Request-access is
// a courtesy audit note: poc-v1 has no restricted-but-disclosed attachment
// state, so a caller who can see the row already has the only "access"
// that exists here.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// TestGetAttachmentExtractionDefaultsToHonestlyEmpty proves the production
// default (no WithExtractor call) answers a valid empty extraction — never
// a 501 — once the attachment clears the scan gate.
func TestGetAttachmentExtractionDefaultsToHonestlyEmpty(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Extraction NoOp", &e.Rep1)
	att := uploadScanTestAttachment(ctx, t, h, person, "report.pdf", []byte("PDF-BYTES"))
	if att.ScanStatus == nil || *att.ScanStatus != crmcontracts.AttachmentScanStatusScanning {
		t.Fatalf("precondition: fresh upload scan_status = %v, want scanning", att.ScanStatus)
	}
	markAttachmentClean(ctx, t, e, ids.UUID(att.Id))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(ctx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got crmcontracts.AttachmentExtraction
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Fields) != 0 || len(got.Omitted) != 0 {
		t.Errorf("extraction = %+v, want {fields:[],omitted:[]}", got)
	}
	if !strings.Contains(rec.Body.String(), `"fields":[]`) || !strings.Contains(rec.Body.String(), `"omitted":[]`) {
		t.Errorf("wire body = %s, want empty arrays not null", rec.Body.String())
	}
}

// TestGetAttachmentExtractionPartitionsFixtureEvidence proves the wired
// Fixture extractor's grounded/omitted split rides the wire shape intact:
// two grounded fields with their evidence, one honestly omitted.
func TestGetAttachmentExtractionPartitionsFixtureEvidence(t *testing.T) {
	e := Setup(t)
	base := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Fixture Deal", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(ctx, t, base, deal, "quote.pdf", []byte("quote bytes"))
	markAttachmentClean(ctx, t, e, ids.UUID(att.Id))

	fx := extraction.FixtureExtractor{Fields: map[string][]extraction.ExtractedField{
		att.Id.String(): {
			{Field: "amount_minor", Value: "150000", SourceQuote: "Total: $1,500.00", PageOrSection: "p.1", Confidence: "high"},
			{Field: "currency", Value: "USD", SourceQuote: "$1,500.00 USD", PageOrSection: "p.1", Confidence: "medium"},
			{Field: "expected_close_date", Omitted: true, OmittedReason: "not_stated_in_file"},
		},
	}}
	h := base.WithExtractor(fx)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(ctx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got crmcontracts.AttachmentExtraction
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("Fields = %+v, want 2 grounded fields", got.Fields)
	}
	for _, f := range got.Fields {
		if f.SourceQuote == "" || f.PageOrSection == "" || f.Confidence == "" {
			t.Errorf("grounded field %+v missing evidence", f)
		}
	}
	if len(got.Omitted) != 1 || got.Omitted[0].Field != "expected_close_date" ||
		got.Omitted[0].Reason != "not_stated_in_file" {
		t.Errorf("Omitted = %+v, want one expected_close_date/not_stated_in_file entry", got.Omitted)
	}
}

// TestGetAttachmentExtractionReadsAnyEntityType proves the read is valid for
// a non-deal attachment: accepting fields onto a deal is what's deal-only,
// not reading the staged extraction.
func TestGetAttachmentExtractionReadsAnyEntityType(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	org := e.SeedOrg(t, "Non-Deal Parent", &e.Rep1)
	att := uploadScanTestAttachmentForOrg(ctx, t, h, org, "notes.txt", []byte("org notes"))
	markAttachmentClean(ctx, t, e, ids.UUID(att.Id))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(ctx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-deal attachment extraction read: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestGetAttachmentExtractionRefusesWhileScanning proves the extraction
// read's defense-in-depth scan gate (RD-T05): a fresh upload defaults to
// 'scanning' (0070), and the read must refuse it with the same typed 409
// the raw-byte download answers — before the extractor ever sees the
// bytes. Inert today under the NoOp/Fixture seams; essential the moment a
// real extractor reads unvetted content.
func TestGetAttachmentExtractionRefusesWhileScanning(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Extraction Scan Gate", &e.Rep1)
	att := uploadScanTestAttachment(ctx, t, h, person, "report.pdf", []byte("PDF-BYTES"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(ctx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusConflict {
		t.Fatalf("extraction read while scanning: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var p scanProblem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode scan_pending problem: %v", err)
	}
	if p.Code != "scan_pending" {
		t.Errorf("code = %q, want scan_pending", p.Code)
	}
}

// TestGetAttachmentExtractionRefusesWhenBlocked mirrors the scanning case
// for a quarantined verdict — terminal, never read.
func TestGetAttachmentExtractionRefusesWhenBlocked(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Extraction Blocked Gate", &e.Rep1)
	att := uploadScanTestAttachment(ctx, t, h, person, "malware.bin", []byte("EVIL"))
	if _, err := e.Activities.MarkScanResult(ctx, ids.UUID(att.Id), activities.FakeScanner{Result: "blocked"}); err != nil {
		t.Fatalf("MarkScanResult(blocked): %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(ctx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusConflict {
		t.Fatalf("extraction read while blocked: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var p scanProblem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode attachment_blocked problem: %v", err)
	}
	if p.Code != "attachment_blocked" {
		t.Errorf("code = %q, want attachment_blocked", p.Code)
	}
}

// TestGetAttachmentExtractionHidesAnInvisibleParent proves the extraction
// read carries the same existence-hiding row-scope gate as every other
// attachment op.
func TestGetAttachmentExtractionHidesAnInvisibleParent(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	adminCtx := e.Admin()
	person := e.SeedPerson(t, "Rep1's Extraction Target", &e.Rep1)
	att := uploadScanTestAttachment(adminCtx, t, h, person, "secret.pdf", []byte("secret"))

	repCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, ownPersonPerms())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String()+"/extraction", nil).WithContext(repCtx)
	h.GetAttachmentExtraction(rec, req, att.Id)
	if rec.Code != http.StatusNotFound {
		t.Errorf("extraction read through an invisible parent: status %d, want 404", rec.Code)
	}

	missing := openapi_types.UUID(ids.NewV7())
	missRec := httptest.NewRecorder()
	missReq := httptest.NewRequest(http.MethodGet, "/v1/attachments/x/extraction", nil).WithContext(adminCtx)
	h.GetAttachmentExtraction(missRec, missReq, missing)
	if missRec.Code != http.StatusNotFound {
		t.Errorf("extraction read of a missing attachment: status %d, want 404", missRec.Code)
	}
}

// TestRequestAttachmentAccessAuditsANoteAndReturnsRequested proves the
// courtesy op writes one activity note carrying the requesting principal,
// linked back to the parent deal, and answers {requested:true}.
func TestRequestAttachmentAccessAuditsANoteAndReturnsRequested(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Access Request Deal", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(ctx, t, h, deal, "contract.pdf", []byte("contract bytes"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments/"+att.Id.String()+"/request-access", nil).WithContext(ctx)
	h.RequestAttachmentAccess(rec, req, att.Id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got crmcontracts.RequestAccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Requested {
		t.Errorf("RequestAccessResponse = %+v, want requested:true", got)
	}

	notes := e.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE a.kind = 'note' AND a.source = 'attachment_access_request'
		  AND a.body LIKE '%contract.pdf%' AND al.entity_type = 'deal' AND al.deal_id = $1
		  AND a.captured_by IS NOT NULL AND a.captured_by <> ''`,
		deal)
	if notes != 1 {
		t.Errorf("access-request audit notes linked to the deal = %d, want exactly 1", notes)
	}
}

// TestRequestAttachmentAccessHidesAnInvisibleParent proves request-access
// carries the same existence-hiding gate: poc-v1 has no restricted-but-
// disclosed row, so a caller who cannot see the parent gets 404, not a
// locked-row placeholder.
func TestRequestAttachmentAccessHidesAnInvisibleParent(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	adminCtx := e.Admin()
	person := e.SeedPerson(t, "Rep1's Access Target", &e.Rep1)
	att := uploadScanTestAttachment(adminCtx, t, h, person, "hidden.pdf", []byte("hidden"))

	repCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, ownPersonPerms())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments/"+att.Id.String()+"/request-access", nil).WithContext(repCtx)
	h.RequestAttachmentAccess(rec, req, att.Id)
	if rec.Code != http.StatusNotFound {
		t.Errorf("request-access through an invisible parent: status %d, want 404", rec.Code)
	}

	notes := e.WsCount(t, `SELECT count(*) FROM activity WHERE source = 'attachment_access_request'`)
	if notes != 0 {
		t.Errorf("a hidden-parent request-access still wrote %d audit note(s), want 0", notes)
	}
}

// uploadAttachmentAs drives the real multipart handler for an arbitrary
// entity_type — uploadScanTestAttachment (harness.go) only ever targets
// "person", so the non-deal/deal-scoped extraction tests need their own
// parent kind.
func uploadAttachmentAs(ctx context.Context, t *testing.T, h activities.Handlers, entityType string, entityID ids.UUID, filename string, data []byte) crmcontracts.Attachment {
	t.Helper()
	body, ctype := multipartAttachment(t, entityType, entityID.String(), filename, data)
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

// uploadDealAttachment drives the real multipart handler for a deal-scoped
// attachment.
func uploadDealAttachment(ctx context.Context, t *testing.T, h activities.Handlers, dealID ids.UUID, filename string, data []byte) crmcontracts.Attachment {
	t.Helper()
	return uploadAttachmentAs(ctx, t, h, "deal", dealID, filename, data)
}

// uploadScanTestAttachmentForOrg mirrors uploadScanTestAttachment for an
// organization-scoped attachment (proving the extraction read is valid for
// any entity_type, not only deal).
func uploadScanTestAttachmentForOrg(ctx context.Context, t *testing.T, h activities.Handlers, orgID ids.UUID, filename string, data []byte) crmcontracts.Attachment {
	t.Helper()
	return uploadAttachmentAs(ctx, t, h, "organization", orgID, filename, data)
}
