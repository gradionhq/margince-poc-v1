// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// EnsureAttachmentScanClean is the meta-row gate GetAttachmentExtraction
// and compose's accept-write share (defense-in-depth, RD-T05): the same
// scan_status vocabulary OpenAttachment gates live, answered from an
// already-fetched Attachment.ScanStatus instead of a second query.
func TestEnsureAttachmentScanCleanGatesOnStatus(t *testing.T) {
	clean := crmcontracts.AttachmentScanStatusClean
	scanning := crmcontracts.AttachmentScanStatusScanning
	blocked := crmcontracts.AttachmentScanStatusBlocked
	cases := map[string]struct {
		status  *crmcontracts.AttachmentScanStatus
		wantErr error
	}{
		"nil status reads clean (never a spurious refusal)": {status: nil, wantErr: nil},
		"clean passes":               {status: &clean, wantErr: nil},
		"scanning refuses retryably": {status: &scanning, wantErr: ErrScanPending},
		"blocked refuses terminally": {status: &blocked, wantErr: ErrAttachmentBlocked},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := EnsureAttachmentScanClean(tc.status)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// ScanGateHTTPError only matches the two scan sentinels — every other
// error must fall through to the caller's own mapping, never mistaken for
// a scan refusal.
func TestScanGateHTTPErrorOnlyMatchesScanSentinels(t *testing.T) {
	if _, ok := ScanGateHTTPError(apperrors.ErrNotFound); ok {
		t.Error("ScanGateHTTPError matched an unrelated sentinel")
	}
	detail, ok := ScanGateHTTPError(ErrScanPending)
	if !ok || detail.Code != "scan_pending" || detail.Status != http.StatusConflict {
		t.Errorf("ScanGateHTTPError(ErrScanPending) = %+v/%v, want 409 scan_pending/true", detail, ok)
	}
	detail, ok = ScanGateHTTPError(ErrAttachmentBlocked)
	if !ok || detail.Code != "attachment_blocked" || detail.Status != http.StatusConflict {
		t.Errorf("ScanGateHTTPError(ErrAttachmentBlocked) = %+v/%v, want 409 attachment_blocked/true", detail, ok)
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
