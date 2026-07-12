// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package quotas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Both zero-denominator refusals answer the contract's single 422
// attainment_target_zero code, but the caller's remedy differs: a stored
// zero target is edited on the quota, a converted zero means the target
// is too small for the stored FX rate — so the converted case's detail
// must name the conversion, never claim target_minor itself is zero.
func TestWriteQuotaErr_TargetZeroDetailNamesTheRefusalCause(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantDetail []string // substrings the wire detail must carry
	}{
		{
			name:       "stored zero target",
			err:        ErrAttainmentTargetZero,
			wantDetail: []string{"target_minor is zero"},
		},
		{
			name:       "target converts to zero in the base currency",
			err:        &ConvertedTargetZeroError{From: "USD", To: "EUR"},
			wantDetail: []string{"converts to zero", "EUR"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeQuotaErr(rec, httptest.NewRequest(http.MethodGet, "/v1/quotas/x/attainment", nil), tt.err)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var problem struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decoding the problem body: %v", err)
			}
			if problem.Code != "attainment_target_zero" {
				t.Fatalf("code = %q, want attainment_target_zero (one code, two causes)", problem.Code)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(problem.Detail, want) {
					t.Errorf("detail = %q, must mention %q", problem.Detail, want)
				}
			}
		})
	}
}
