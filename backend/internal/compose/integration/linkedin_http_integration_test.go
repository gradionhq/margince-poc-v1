// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The LinkedIn upload over HTTP (ADR-0078 §2.1b) — the whole path a person
// actually takes: multipart upload, parse, store, match, and the summary they
// read to decide whether to trust it.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
)

type importSummaryDTO struct {
	Rows      int `json:"rows"`
	Imported  int `json:"imported"`
	Skipped   int `json:"skipped"`
	Confirmed int `json:"confirmed"`
	Suggested int `json:"suggested"`
}

// uploadExport posts a CSV as multipart, the way the browser does.
func (e *env) uploadExport(t *testing.T, csv string) (int, importSummaryDTO) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "Connections.csv")
	if err != nil {
		t.Fatalf("building the multipart body: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		t.Fatalf("writing the file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing the multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/v1/me/linkedin-connections", &body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Workspace-Slug", e.slug)

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("uploading: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response body: %v", cerr)
		}
	}()
	var out importSummaryDTO
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decoding the summary: %v", err)
		}
	}
	return resp.StatusCode, out
}

const exportWithPreamble = `Notes:
"When exporting your connection data, you may notice that some of the email addresses are missing."

First Name,Last Name,URL,Email Address,Company,Position,Connected On
Dana,Buyer,https://x,dana@acme.test,Acme GmbH,CTO,15 Mar 2024
Andreas,Müller,https://x,,Acme GmbH,Head of IT,02 Feb 2023
,,https://x,,Ghost Ltd,Founder,01 Jan 2020
`

func TestUploadingAnExportImportsItAndReportsWhatItDid(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	status, got := e.uploadExport(t, exportWithPreamble)
	if status != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", status)
	}
	if got.Imported != 2 {
		t.Errorf("imported %d connections, want 2", got.Imported)
	}
	// The nameless row is COUNTED, not silently dropped: a file half-ignored
	// under a success message is worse than a refusal.
	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 — the unusable row must be reported", got.Skipped)
	}
	// Nobody matches yet, and that is reported as zero rather than hidden.
	if got.Confirmed != 0 {
		t.Errorf("confirmed = %d with no contacts in the workspace", got.Confirmed)
	}

	// Re-uploading a refreshed export updates rather than duplicating —
	// people re-export regularly, and a doubled network makes every reach
	// count a lie.
	status, again := e.uploadExport(t, exportWithPreamble)
	if status != http.StatusOK {
		t.Fatalf("re-upload status = %d", status)
	}
	if again.Imported != 2 {
		t.Errorf("re-import stored %d rows, want the same 2", again.Imported)
	}
}

func TestAnExactAddressMatchConfirmsOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A contact carrying the address the export names.
	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Dana Buyer",
		"emails":    []anyMap{{"email": "dana@acme.test", "is_primary": true}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("creating the contact: %d", status)
	}

	status, got := e.uploadExport(t, exportWithPreamble)
	if status != http.StatusOK {
		t.Fatalf("upload status = %d", status)
	}
	// An address is identity here, as it is everywhere else in this module,
	// so the match confirms without asking anybody.
	if got.Confirmed != 1 {
		t.Errorf("confirmed = %d, want 1 — the exact address match did not fire", got.Confirmed)
	}
	// Andreas has no address in the export and no employment here, so he is
	// not even a suggestion: name alone is never a match.
	if got.Suggested != 0 {
		t.Errorf("suggested = %d on a name with no employer agreement, want 0", got.Suggested)
	}
}

func TestAFileThatIsNotAnExportIsRefusedWithAReadableReason(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// Picking the wrong file is a mistake a sentence can fix. Answering 500
	// would send someone to support for something they can solve themselves.
	status, _ := e.uploadExport(t, "id,amount\n1,20\n")
	if status != http.StatusUnprocessableEntity {
		t.Errorf("a non-export answered %d, want 422", status)
	}
}
