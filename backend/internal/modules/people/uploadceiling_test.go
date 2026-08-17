// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A handler nobody told a ceiling has a wiring fault, and the caller's file is
// not the fault. Answering 413 "exceeds the 0 MB limit" — which is what a bare
// MaxBytesReader(0) produces — sends them off to shrink a file that was never
// too large, over a limit nobody set.
func TestAnUnwiredUploadCeilingIsOurFaultNotTheCallers(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/linkedin-connections",
		strings.NewReader("not that it gets read"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")

	Handlers{}.ImportLinkedInConnections(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("an unwired ceiling answered %d, want 500 — anything in the 4xx "+
			"range blames the caller for a number this composition never set",
			rec.Code)
	}
	if strings.Contains(rec.Body.String(), "MB") {
		t.Errorf("the refusal %q names a size limit, which is exactly the "+
			"misdirection this branch exists to avoid", rec.Body.String())
	}
}
