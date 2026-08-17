// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The upload ceiling, end to end: what the deployment file says, what the
// chassis lets through, and what the refusal tells the person holding the file.
//
// These run over a composed server with a real session rather than a
// hand-built handler, because the bug they exist to prevent IS a wiring bug —
// four places have to be handed the same number, and a test that constructs the
// handler itself proves nothing about whether composition did.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// uploadCeiling is the shipped attachment default, read from the deployment
// config rather than restated, so a test never disagrees with the file about
// what an unconfigured installation allows.
var uploadCeiling = deployconfig.Config{}.EffectiveUploads().Attachment

// The ceilings these suites configure. Deliberately UNUSUAL — small enough to
// exceed in a test, and nothing like a shipped default, so a composition that
// quietly fell back to a compiled-in number fails here instead of passing.
const (
	testAttachmentMB = 3
	testCSVImportMB  = 2
	// Between the two: over the import cap, under the attachment ceiling. What
	// makes the two keys provably separate rather than one key spelled twice.
	betweenTheCeilings = 2_500_000
)

func configuredUploads(t *testing.T) deployconfig.UploadLimits {
	t.Helper()
	cfg, err := deployconfig.Parse([]byte(`version: 1
uploads:
  attachment_mb: 3
  csv_import_mb: 2
`))
	if err != nil {
		t.Fatalf("the uploads section was refused: %v", err)
	}
	return cfg.EffectiveUploads()
}

// appWithUploads starts the real /v1 surface with an object store, the ceilings
// above, and a signed-in admin.
func appWithUploads(t *testing.T) *apptest.AppEnv {
	t.Helper()
	e := apptest.SetupAppWithOptions(t,
		compose.WithBlobstore(blobstore.NewMemory()),
		compose.WithUploadLimits(configuredUploads(t)))
	e.BootstrapWorkspace(t)
	return e
}

// postUpload sends one multipart body to a route and returns its status and
// response text.
func postUpload(t *testing.T, e *apptest.AppEnv, path string, form *bytes.Buffer, contentType string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.TS.URL+path, form)
	if err != nil {
		t.Fatalf("building the upload: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	//nolint:bodyclose // apptest.CloseBody closes it in the deferred call below, which the checker cannot follow across the helper.
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("uploading: %v", err)
	}
	defer apptest.CloseBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the upload response: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// attachmentForm builds an upload of `size` bytes against the installation's
// own organization, which every bootstrapped workspace has.
func attachmentForm(t *testing.T, e *apptest.AppEnv, size int) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for field, value := range map[string]string{
		"entity_type": "organization",
		"entity_id":   anOrganizationID(t, e),
	} {
		if err := form.WriteField(field, value); err != nil {
			t.Fatalf("writing %s: %v", field, err)
		}
	}
	part, err := form.CreateFormFile("file", "scan.pdf")
	if err != nil {
		t.Fatalf("creating the file part: %v", err)
	}
	if _, err := part.Write(make([]byte, size)); err != nil {
		t.Fatalf("writing the file part: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("closing the form: %v", err)
	}
	return &buf, form.FormDataContentType()
}

// anOrganizationID creates a company over the real endpoint, so the parent an
// upload is filed against is one the product itself made.
func anOrganizationID(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	return createdID(t, e, "/v1/organizations", apptest.AnyMap{"display_name": "Ceiling Test GmbH"})
}

func importForm(t *testing.T, size int) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	if err := form.WriteField("object", "leads"); err != nil {
		t.Fatalf("writing the object field: %v", err)
	}
	part, err := form.CreateFormFile("file", "estate.csv")
	if err != nil {
		t.Fatalf("creating the file part: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a,b,c\n"), size/6)); err != nil {
		t.Fatalf("writing the file part: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("closing the form: %v", err)
	}
	return &buf, form.FormDataContentType()
}

// TestAnUploadOverTheJSONBoundReachesTheHandler is the defect the ceiling
// exists for: before it, every route rode 1 MiB, so a 2 MB document was refused
// by the chassis with a message about a JSON body nobody sent.
func TestAnUploadOverTheJSONBoundReachesTheHandler(t *testing.T) {
	e := appWithUploads(t)
	form, contentType := attachmentForm(t, e, 2<<20)

	code, body := postUpload(t, e, "/v1/attachments", form, contentType)
	if code != http.StatusCreated {
		t.Fatalf("a 2 MiB upload under a %d MB ceiling answered %d: %s",
			testAttachmentMB, code, body)
	}
}

// TestAnOversizeUploadIsRefusedWithTheConfiguredNumber is the other half: the
// refusal has to name the limit THIS installation applies. A compiled-in number
// in that sentence sends the reader off to shrink a file against the wrong bar.
func TestAnOversizeUploadIsRefusedWithTheConfiguredNumber(t *testing.T) {
	e := appWithUploads(t)
	form, contentType := attachmentForm(t, e, 4_000_000)

	code, body := postUpload(t, e, "/v1/attachments", form, contentType)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 4 MB upload under a %d MB ceiling answered %d, want 413: %s",
			testAttachmentMB, code, body)
	}
	if want := fmt.Sprintf("%d MB", testAttachmentMB); !strings.Contains(body, want) {
		t.Errorf("the refusal %q does not name the configured %s limit", body, want)
	}
}

// TestTheInstallationReadPublishesTheConfiguredCeiling closes the loop: the
// number the client is told is the number the server enforces. It is published
// so an upload surface can refuse before sending rather than after, which is
// only honest while the two cannot drift.
func TestTheInstallationReadPublishesTheConfiguredCeiling(t *testing.T) {
	e := appWithUploads(t)
	var settings crmcontracts.InstallationSettings
	if code := e.Call(t, http.MethodGet, "/v1/installation/settings", nil, nil, &settings); code != http.StatusOK {
		t.Fatalf("reading installation settings answered %d", code)
	}

	if want := configuredUploads(t).Attachment; settings.MaxUploadBytes != want {
		t.Errorf("the installation reports a %d-byte ceiling but enforces %d — a "+
			"client that trusts this number either refuses files the server would "+
			"take, or sends files it will not", settings.MaxUploadBytes, want)
	}
}

// TestEachUploadRouteIsBoundedSeparately proves the per-route keys are not one
// key with three spellings: the import cap refuses a body the attachment
// ceiling takes.
func TestEachUploadRouteIsBoundedSeparately(t *testing.T) {
	e := appWithUploads(t)

	form, contentType := attachmentForm(t, e, betweenTheCeilings)
	if code, body := postUpload(t, e, "/v1/attachments", form, contentType); code != http.StatusCreated {
		t.Fatalf("a %d-byte upload under the %d MB attachment ceiling answered %d: %s",
			betweenTheCeilings, testAttachmentMB, code, body)
	}

	importBody, importType := importForm(t, betweenTheCeilings)
	code, body := postUpload(t, e, "/v1/imports/sources", importBody, importType)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("the same %d bytes answered %d on the import route, want 413 — "+
			"raising what a scanned contract may weigh must not raise what a CSV "+
			"import will attempt: %s", betweenTheCeilings, code, body)
	}
	if code == http.StatusRequestEntityTooLarge &&
		!strings.Contains(body, fmt.Sprintf("%d MB", testCSVImportMB)) {
		t.Errorf("the import refusal %q names a limit other than its own %d MB",
			body, testCSVImportMB)
	}
}

// TestAStoredUploadKeepsItsOwnSizeAndChecksum guards the streaming write path.
// The bytes are hashed on one pass and stored on a second, and a rewind that
// did not happen would store an empty object under a checksum of the real one —
// a row that reads perfectly and describes nothing.
func TestAStoredUploadKeepsItsOwnSizeAndChecksum(t *testing.T) {
	e := appWithUploads(t)
	const size = 2 << 20
	form, contentType := attachmentForm(t, e, size)

	code, body := postUpload(t, e, "/v1/attachments", form, contentType)
	if code != http.StatusCreated {
		t.Fatalf("upload answered %d: %s", code, body)
	}
	var stored crmcontracts.Attachment
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatalf("decoding the stored attachment: %v", err)
	}
	if stored.ByteSize == nil || *stored.ByteSize != size {
		t.Errorf("the row records %v bytes for a %d-byte file", stored.ByteSize, size)
	}

	var downloaded bytes.Buffer
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		e.TS.URL+"/v1/attachments/"+stored.Id.String(), nil)
	if err != nil {
		t.Fatalf("building the download: %v", err)
	}
	//nolint:bodyclose // apptest.CloseBody closes it in the deferred call below, which the checker cannot follow across the helper.
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("downloading: %v", err)
	}
	defer apptest.CloseBody(t, resp)
	if _, err := io.Copy(&downloaded, resp.Body); err != nil {
		t.Fatalf("reading the download: %v", err)
	}
	if downloaded.Len() != size {
		t.Errorf("downloaded %d bytes of a %d-byte file — the object and the row "+
			"it is described by disagree", downloaded.Len(), size)
	}
}
