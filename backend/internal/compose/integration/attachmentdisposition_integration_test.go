// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The one line between "a stranger sent us a file" and stored XSS.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

// A downloaded attachment is SAVED, never rendered — including one whose bytes
// are markup.
//
// An attachment's bytes are sender-controlled, its content type is sniffed from
// those same bytes, and a text/html file served inline would execute in the
// viewer's session against this origin. The property is currently free —
// DownloadAttachment does not ask for Inline — and this test exists so that
// adding an inline preview has to confront the fact that a captured channel file
// comes from an untrusted sender rather than trip over it.
func TestDownloadedAttachmentIsSavedNotRendered(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.DB()).WithUploadLimit(uploadCeiling).WithBlobstore(blobstore.NewMemory())
	ctx := e.Admin()
	person := e.SeedPerson(t, "Untrusted Sender", &e.Rep1)

	// Markup, because that is the shape that would execute: a benign PDF proves
	// the header is set, not that the dangerous case is covered by it.
	body, ctype := multipartAttachment(t, "person", person.String(), "invoice.html",
		[]byte(`<html><script>document.title="xss"</script></html>`))
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

	dl := httptest.NewRecorder()
	h.DownloadAttachment(dl, httptest.NewRequest(http.MethodGet, "/v1/attachments/"+att.Id.String(), nil).WithContext(ctx), att.Id)
	if dl.Code != http.StatusOK {
		t.Fatalf("download: status %d", dl.Code)
	}
	if got := dl.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("Content-Disposition is %q; an attachment must be saved, not rendered — "+
			"a sniffed text/html file served inline is stored XSS", got)
	}
}
