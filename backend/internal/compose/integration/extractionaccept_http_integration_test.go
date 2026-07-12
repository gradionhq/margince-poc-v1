// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for POST /attachments/{id}/extraction:accept over
// the real handler stack (session auth, the compose.WithExtractor wiring,
// the RFC 7807 mapper): the wire request/response shapes and the typed
// 422 codes that only exist at the transport. The engine-level matrix
// (grants, row scope, provenance stamps) is extractionaccept_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// uploadAttachmentHTTP drives the real multipart upload endpoint (e.call
// only speaks JSON) and returns the new attachment's id.
func (e *env) uploadAttachmentHTTP(t *testing.T, entityType, entityID, filename string) string {
	t.Helper()
	body, ctype := multipartAttachment(t, entityType, entityID, filename, []byte("attachment bytes"))
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/v1/attachments", body)
	if err != nil {
		t.Fatalf("building upload request: %v", err)
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("X-Workspace-Slug", e.slug)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading upload response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, body %s", resp.StatusCode, raw)
	}
	var att struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &att); err != nil {
		t.Fatalf("decoding upload response: %v", err)
	}
	return att.ID
}

// markAttachmentCleanHTTP flips one attachment's scan_status to 'clean'
// directly through the owner connection — there is no scan-verdict HTTP
// endpoint (MarkScanResult is administrative, never a public API) — inside
// a workspace-bound transaction so RLS (FORCE) admits the UPDATE. Mirrors
// setWorkspaceSeat's shape (e2e_integration_test.go). A fresh upload
// defaults to 'scanning' (0070), and the accept-write now scan-gates like
// every other path over an attachment's bytes, so any HTTP scenario that
// expects the accept to actually run its extractor must clear the gate
// first.
func (e *env) markAttachmentCleanHTTP(t *testing.T, attID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE attachment SET scan_status = 'clean' WHERE id = $1`, attID); err != nil {
		t.Fatalf("mark clean: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// acceptProblemWire is the RFC 7807 slice these assertions read.
type acceptProblemWire struct {
	Code    string `json:"code"`
	Details struct {
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	} `json:"details"`
}

// assertAcceptHTTPHappyPath drives the accept over the wire — the edited
// amount lands as human provenance, the extracted currency as
// ai-extracted — and reads the deal back through the API.
func assertAcceptHTTPHappyPath(t *testing.T, e *env, attID, dealID string) {
	t.Helper()
	var resp struct {
		DealID   string `json:"deal_id"`
		Accepted []struct {
			Field      string `json:"field"`
			Value      string `json:"value"`
			Provenance string `json:"provenance"`
		} `json:"accepted"`
	}
	status := e.call(t, "POST", "/v1/attachments/"+attID+"/extraction:accept", anyMap{
		"field_keys": []string{"amount_minor", "currency"},
		"edits":      anyMap{"amount_minor": "200000"},
	}, nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("accept = %d %+v", status, resp)
	}
	if resp.DealID != dealID {
		t.Errorf("deal_id = %s, want %s", resp.DealID, dealID)
	}
	if len(resp.Accepted) != 2 ||
		resp.Accepted[0].Field != "amount_minor" || resp.Accepted[0].Value != "200000" || resp.Accepted[0].Provenance != "human" ||
		resp.Accepted[1].Field != "currency" || resp.Accepted[1].Value != "EUR" || resp.Accepted[1].Provenance != "ai-extracted" {
		t.Errorf("accepted = %+v, want the edited amount as human and the extracted currency as ai-extracted", resp.Accepted)
	}

	var got anyMap
	if status := e.call(t, "GET", "/v1/deals/"+dealID, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("read back deal = %d", status)
	}
	if got["amount_minor"] != float64(200000) || got["currency"] != "EUR" {
		t.Errorf("deal after accept = amount %v currency %v, want 200000 EUR", got["amount_minor"], got["currency"])
	}
}

// assertAcceptHTTPNonDeal422 uploads a person-scoped attachment and checks
// the typed unsupported_entity_type refusal.
func assertAcceptHTTPNonDeal422(t *testing.T, e *env) {
	t.Helper()
	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Attachment Holder", "source": "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personAtt := e.uploadAttachmentHTTP(t, "person", person["id"].(string), "cv.pdf")

	var problem acceptProblemWire
	status := e.call(t, "POST", "/v1/attachments/"+personAtt+"/extraction:accept", anyMap{
		"field_keys": []string{"amount_minor"},
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "unsupported_entity_type" {
		t.Errorf("non-deal accept = %d code %q, want 422 unsupported_entity_type", status, problem.Code)
	}
}

// assertAcceptHTTPValidation422 posts the given field_keys and checks the
// validation_error problem names exactly one offending field/code.
func assertAcceptHTTPValidation422(t *testing.T, e *env, attID string, fieldKeys []string, wantField, wantCode string) {
	t.Helper()
	var problem acceptProblemWire
	status := e.call(t, "POST", "/v1/attachments/"+attID+"/extraction:accept", anyMap{
		"field_keys": fieldKeys,
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Fatalf("field_keys %v = %d code %q, want 422 validation_error", fieldKeys, status, problem.Code)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != wantField || problem.Details.Errors[0].Code != wantCode {
		t.Errorf("details = %+v, want %s/%s", problem.Details.Errors, wantField, wantCode)
	}
}

func TestAcceptAttachmentExtractionHTTP(t *testing.T) {
	// One extractor instance feeds BOTH the activities read and the accept
	// write (the compose.WithExtractor wiring); its map fills in after the
	// upload mints the attachment id.
	fx := extraction.FixtureExtractor{Fields: map[string][]extraction.ExtractedField{}}
	e := setupWithOptions(t, compose.WithExtractor(fx), compose.WithBlobstore(blobstore.NewMemory()))
	e.bootstrapWorkspace(t)
	stages := discoverSeededPipeline(t, e)

	var deal anyMap
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "HTTP Accept Deal", "pipeline_id": stages.pipelineID,
		"stage_id": stages.open, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	dealID := deal["id"].(string)
	attID := e.uploadAttachmentHTTP(t, "deal", dealID, "quote.pdf")
	fx.Fields[attID] = []extraction.ExtractedField{
		{Field: "amount_minor", Value: "150000", SourceQuote: "Total: EUR 1,500.00", PageOrSection: "p.2", Confidence: "high"},
		{Field: "currency", Value: "EUR", SourceQuote: "all amounts in EUR", PageOrSection: "p.2", Confidence: "medium"},
	}
	// This suite exercises the accept-write's own validation, not the scan
	// gate (TestAcceptAttachmentExtractionScanGateHTTP owns that) — clear
	// the upload default ('scanning') so every subtest below reaches the
	// extractor.
	e.markAttachmentCleanHTTP(t, attID)

	t.Run("200 persists the fields and flips the edited provenance", func(t *testing.T) {
		assertAcceptHTTPHappyPath(t, e, attID, dealID)
	})
	t.Run("422 unsupported_entity_type for a non-deal attachment", func(t *testing.T) {
		assertAcceptHTTPNonDeal422(t, e)
	})
	t.Run("422 validation_error for empty field_keys", func(t *testing.T) {
		assertAcceptHTTPValidation422(t, e, attID, []string{}, "field_keys", "required")
	})
	t.Run("422 validation_error naming the ungrounded key", func(t *testing.T) {
		assertAcceptHTTPValidation422(t, e, attID, []string{"probability"}, "field_keys[0]", "not_grounded")
	})
	t.Run("404 for a missing attachment", func(t *testing.T) {
		status := e.call(t, "POST", "/v1/attachments/"+ids.NewV7().String()+"/extraction:accept", anyMap{
			"field_keys": []string{"amount_minor"},
		}, nil, nil)
		if status != http.StatusNotFound {
			t.Errorf("missing attachment accept = %d, want 404", status)
		}
	})
}

// TestAcceptAttachmentExtractionScanGateHTTP proves the wire-level 409:
// a fresh deal-scoped upload starts 'scanning' (0070), and POST
// .../extraction:accept must refuse it with the exact contract problem the
// raw-byte download answers — before the extractor runs and with the deal
// left untouched — never a 200 that would let unvetted content reach a
// deal field.
func TestAcceptAttachmentExtractionScanGateHTTP(t *testing.T) {
	fx := extraction.FixtureExtractor{Fields: map[string][]extraction.ExtractedField{}}
	e := setupWithOptions(t, compose.WithExtractor(fx), compose.WithBlobstore(blobstore.NewMemory()))
	e.bootstrapWorkspace(t)
	stages := discoverSeededPipeline(t, e)

	var deal anyMap
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Scan Gate Accept Deal", "pipeline_id": stages.pipelineID,
		"stage_id": stages.open, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	dealID := deal["id"].(string)
	attID := e.uploadAttachmentHTTP(t, "deal", dealID, "quote.pdf")
	fx.Fields[attID] = []extraction.ExtractedField{
		{Field: "amount_minor", Value: "150000", SourceQuote: "Total: EUR 1,500.00", PageOrSection: "p.2", Confidence: "high"},
	}

	var problem acceptProblemWire
	status := e.call(t, "POST", "/v1/attachments/"+attID+"/extraction:accept", anyMap{
		"field_keys": []string{"amount_minor"},
	}, nil, &problem)
	if status != http.StatusConflict || problem.Code != "scan_pending" {
		t.Fatalf("accept while scanning = %d code %q, want 409 scan_pending", status, problem.Code)
	}

	var got anyMap
	if status := e.call(t, "GET", "/v1/deals/"+dealID, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("read back deal = %d", status)
	}
	if got["amount_minor"] != nil {
		t.Errorf("deal after a scan-gated accept refusal carries amount_minor = %v, want untouched (nil)", got["amount_minor"])
	}
}
