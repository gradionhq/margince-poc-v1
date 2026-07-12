// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The scan gate's wire shape is contract-fixed (crm.yaml, downloadAttachment
// 409 examples): stable type/code and the exact retry-vs-quarantine detail.
// This pins the mapping so a refactor cannot silently drift the codes the
// SPA and API clients key on.
func TestScanGateErrorsMapToTheContractProblems(t *testing.T) {
	cases := map[string]struct {
		err        error
		wantCode   string
		wantDetail string
	}{
		"scanning refuses retryably": {
			err:        ErrScanPending,
			wantCode:   "scan_pending",
			wantDetail: "This file is still being scanned; retry the download shortly.",
		},
		"blocked refuses terminally": {
			err:        ErrAttachmentBlocked,
			wantCode:   "attachment_blocked",
			wantDetail: "This file was quarantined by the virus scan and cannot be downloaded.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/attachments/x", nil)
			writeAttachmentErr(rec, req, tc.err)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			var p struct {
				Type   string `json:"type"`
				Status int    `json:"status"`
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decode problem body: %v", err)
			}
			if p.Type != "https://errors.gradion.com/"+tc.wantCode {
				t.Errorf("type = %q, want the errors.gradion.com/%s type", p.Type, tc.wantCode)
			}
			if p.Status != http.StatusConflict || p.Code != tc.wantCode || p.Detail != tc.wantDetail {
				t.Errorf("problem = %+v, want status 409 code %q detail %q", p, tc.wantCode, tc.wantDetail)
			}
		})
	}
}

// FakeScanner hands back exactly the verdict it was built with — the seam
// double tests and administration drive; it never invents a verdict.
func TestFakeScannerReturnsItsFixedVerdict(t *testing.T) {
	for _, verdict := range []string{"clean", "blocked"} {
		got, err := FakeScanner{Result: verdict}.Scan(t.Context(), "ws/key")
		if err != nil {
			t.Fatalf("Scan(%s): %v", verdict, err)
		}
		if got != verdict {
			t.Errorf("Scan returned %q, want %q", got, verdict)
		}
	}
}
