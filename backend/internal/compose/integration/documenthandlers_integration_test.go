// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The transport for the account document library (DOC-WIRE-1/2).
//
// The claim that cannot be checked below the handler is how a sparse PATCH
// reads an absent field against an explicit null. openapi generates one
// nullable pointer for both, so the store alone cannot tell "leave the title
// alone" from "this document has no title" — only the raw body can, and only
// the handler sees it. A regression here silently wipes fields the caller
// never mentioned.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// patchMetadata drives the metadata handler the way the router does, and hands
// back the recorder so the caller judges the status itself.
func patchMetadata(t *testing.T, e *Env, h activities.Handlers, id ids.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docWritePerms)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/attachments/x/metadata",
		strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	h.UpdateAttachmentMetadata(rec, req, crmcontracts.Id(id),
		crmcontracts.UpdateAttachmentMetadataParams{})
	return rec
}

// decodeAttachment reads the handler's body as the wire attachment.
func decodeAttachment(t *testing.T, rec *httptest.ResponseRecorder) crmcontracts.Attachment {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out crmcontracts.Attachment
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode attachment: %v", err)
	}
	return out
}

func TestAttachmentMetadataTellsAnAbsentFieldFromAnExplicitNull(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	doc := seedDocument(t, e, org, "organization", org, "msa.pdf", "contract", false)

	// Give it a title to have something an absent field could wrongly erase.
	out := decodeAttachment(t, patchMetadata(t, e, h, doc, `{"title":"Master Services Agreement"}`))
	if out.Title == nil || *out.Title != "Master Services Agreement" {
		t.Fatalf("title after set = %v, want the title just written", out.Title)
	}

	// A patch that does not mention the title must leave it standing.
	out = decodeAttachment(t, patchMetadata(t, e, h, doc, `{"pinned":true}`))
	if out.Title == nil || *out.Title != "Master Services Agreement" {
		t.Fatalf("title after an unrelated patch = %v, want it untouched", out.Title)
	}
	if out.Pinned == nil || !*out.Pinned {
		t.Fatal("pinned did not take effect")
	}

	// An explicit null is an edit — the caller is clearing the title.
	out = decodeAttachment(t, patchMetadata(t, e, h, doc, `{"title":null}`))
	if out.Title != nil {
		t.Fatalf("title after an explicit null = %v, want it cleared", *out.Title)
	}
}

func TestAttachmentMetadataClearsSupersedesOnlyWhenAsked(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	first := seedDocument(t, e, org, "organization", org, "v1.pdf", "contract", false)
	second := seedDocument(t, e, org, "organization", org, "v2.pdf", "contract", false)

	out := decodeAttachment(t, patchMetadata(t, e, h, second,
		`{"supersedes_id":"`+first.String()+`"}`))
	if out.SupersedesId == nil || ids.UUID(*out.SupersedesId) != first {
		t.Fatalf("supersedes_id = %v, want v1", out.SupersedesId)
	}

	// Another field moving must not drop the supersedes link.
	out = decodeAttachment(t, patchMetadata(t, e, h, second, `{"doc_state":"final"}`))
	if out.SupersedesId == nil {
		t.Fatal("supersedes_id was dropped by a patch that never mentioned it")
	}

	// Null says the document replaces nothing after all.
	out = decodeAttachment(t, patchMetadata(t, e, h, second, `{"supersedes_id":null}`))
	if out.SupersedesId != nil {
		t.Fatalf("supersedes_id after an explicit null = %v, want it cleared", out.SupersedesId)
	}
}

func TestAttachmentMetadataRefusesAStaleIfMatch(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	doc := seedDocument(t, e, org, "organization", org, "msa.pdf", "contract", false)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docWritePerms)

	// A malformed If-Match never reaches the store.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/attachments/x/metadata",
		strings.NewReader(`{"pinned":true}`)).WithContext(ctx)
	req.Header.Set("If-Match", "not-a-version")
	h.UpdateAttachmentMetadata(rec, req, crmcontracts.Id(doc),
		crmcontracts.UpdateAttachmentMetadataParams{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed If-Match status = %d, want 422", rec.Code)
	}
}

func TestOrganizationDocumentsHandlerMapsItsFilters(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	seedDocument(t, e, org, "organization", org, "nda.pdf", "legal", false)
	seedDocument(t, e, org, "organization", org, "msa.pdf", "contract", true)

	list := func(t *testing.T, params crmcontracts.ListOrganizationDocumentsParams) crmcontracts.AttachmentListResponse {
		t.Helper()
		ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/organizations/x/documents", nil).WithContext(ctx)
		h.ListOrganizationDocuments(rec, req, crmcontracts.Id(org), params)
		if rec.Code != http.StatusOK {
			t.Fatalf("documents status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var out crmcontracts.AttachmentListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode documents: %v", err)
		}
		return out
	}

	// No filter means no filter: both documents.
	if all := list(t, crmcontracts.ListOrganizationDocumentsParams{}); len(all.Data) != 2 {
		t.Fatalf("unfiltered list returned %d documents, want 2", len(all.Data))
	}

	// The category param reaches the query rather than being dropped.
	legal := crmcontracts.ListOrganizationDocumentsParamsCategory("legal")
	only := list(t, crmcontracts.ListOrganizationDocumentsParams{Category: &legal})
	if len(only.Data) != 1 || only.Data[0].Filename != "nda.pdf" {
		t.Fatalf("category=legal returned %d documents, want only nda.pdf", len(only.Data))
	}

	// pinned_only likewise.
	pinned := true
	got := list(t, crmcontracts.ListOrganizationDocumentsParams{PinnedOnly: &pinned})
	if len(got.Data) != 1 || got.Data[0].Filename != "msa.pdf" {
		t.Fatalf("pinned_only returned %d documents, want only msa.pdf", len(got.Data))
	}
}
