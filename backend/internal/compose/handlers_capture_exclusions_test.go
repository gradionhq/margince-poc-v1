// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The create transport rejects a malformed or out-of-vocabulary body with a
// 422 BEFORE ever reaching the store — the bounded (kind, value) is the
// contract, not a free DSL. A store over a nil pool proves these branches
// never touch the database.
func TestCreateCaptureExclusionRejectsBadInput(t *testing.T) {
	h := captureExclusionHandlers{store: capture.NewExclusions(nil)}
	ctx := principal.WithActor(
		principal.WithWorkspaceID(context.Background(), ids.NewV7()),
		principal.Principal{Type: principal.PrincipalHuman, ID: "human:1", UserID: ids.NewV7()},
	)
	cases := map[string]string{
		"not json":     "{not-json",
		"unknown kind": `{"kind":"subject","value":"x"}`,
		"empty value":  `{"kind":"sender_domain","value":"  "}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/capture/exclusions", strings.NewReader(body)).WithContext(ctx)
			w := httptest.NewRecorder()
			h.CreateCaptureExclusion(w, r)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body did not reach the store): %s", w.Code, w.Body.String())
			}
		})
	}
}

// An unwired handler (no store) answers the repo's explicit 501 on every
// operation, never a nil-deref — the same declared-or-absent posture as the
// connector surface.
func TestCaptureExclusionHandlersUnwiredAre501(t *testing.T) {
	var h captureExclusionHandlers // zero value: store == nil
	ops := map[string]func(http.ResponseWriter, *http.Request){
		"list":   h.ListCaptureExclusions,
		"create": h.CreateCaptureExclusion,
		"delete": func(w http.ResponseWriter, r *http.Request) { h.DeleteCaptureExclusion(w, r, crmcontracts.Id{}) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			op(w, httptest.NewRequest(http.MethodGet, "/v1/capture/exclusions", nil))
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", w.Code)
			}
		})
	}
}
