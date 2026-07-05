// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
)

// The transport turns the connector's sentinels into the right status +
// actionable detail, and the raw wrapped cause (host/network text) never
// reaches the client.
func TestWriteImapErrorMapsSentinels(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		leak       string // a substring that must NOT appear in the response
	}{
		{
			name:       "login rejected",
			err:        imap.ErrLoginRejected,
			wantStatus: 422,
			wantCode:   "imap_login_rejected",
		},
		{
			name:       "unreachable wraps the raw dial cause",
			err:        fmt.Errorf("imap: dial imap.secret-host.internal:993: %w", imap.ErrUnreachable),
			wantStatus: 502,
			wantCode:   "imap_unreachable",
			leak:       "secret-host.internal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/connectors/imap/connect", nil)
			writeImapError(rec, req, tc.err)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			body := rec.Body.String()
			var problem map[string]any
			if err := json.Unmarshal([]byte(body), &problem); err != nil {
				t.Fatalf("response is not JSON: %v (%s)", err, body)
			}
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %s", problem["code"], tc.wantCode)
			}
			if tc.leak != "" && strings.Contains(body, tc.leak) {
				t.Errorf("response leaked internal detail %q: %s", tc.leak, body)
			}
		})
	}
}
