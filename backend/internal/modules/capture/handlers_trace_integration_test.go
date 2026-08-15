// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The two operations end to end, over a real store: the JSON a client actually
// receives, and the refusal a seat without the grant actually gets.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestThePersonalReadAnswersTheCallersOwnWindow(t *testing.T) {
	ctx, ws, db, _ := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	seedTrace(memberCtx, t, db, me, "http-mine", 0)
	seedTrace(memberCtx, t, db, ids.Nil, "http-shared", 0)

	handlers := capture.NewTraceHandlers(capture.NewTraceStore(db), false)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity", nil).WithContext(memberCtx)
	handlers.ListMyCaptureActivity(w, r, crmcontracts.ListMyCaptureActivityParams{})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body crmcontracts.CaptureActivityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the answer: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("data = %d entries, want 1 — the shared-channel row is not the caller's", len(body.Data))
	}
	if body.WindowHours != capture.TraceWindowHours {
		t.Errorf("window_hours = %d, want %d", body.WindowHours, capture.TraceWindowHours)
	}
	if body.PayloadCaptureEnabled {
		t.Error("payload_capture_enabled = true, want the posture this deployment set (off)")
	}
	if body.Funnel.Captured == nil || *body.Funnel.Captured != 1 {
		t.Errorf("funnel.captured = %v, want 1", body.Funnel.Captured)
	}
}

func TestTheWorkspaceReadAnswersOnlySharedChannels(t *testing.T) {
	ctx, ws, db, _ := traceReadWorkspace(t)
	member, manager := ids.NewV7(), ids.NewV7()
	seedTrace(memberContext(ctx, ws, member), t, db, member, "http-personal", 0)
	seedTrace(memberContext(ctx, ws, member), t, db, ids.Nil, "http-bot", 0)

	handlers := capture.NewTraceHandlers(capture.NewTraceStore(db), true)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity/workspace", nil).
		WithContext(managerContext(ctx, ws, manager))
	handlers.ListWorkspaceCaptureActivity(w, r, crmcontracts.ListWorkspaceCaptureActivityParams{})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body crmcontracts.CaptureActivityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the answer: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("data = %d entries, want 1 — a member's own row must not reach this operation", len(body.Data))
	}
	if !body.PayloadCaptureEnabled {
		t.Error("payload_capture_enabled = false, want the posture reported as the deployment set it")
	}
}

func TestTheWorkspaceReadRefusesASeatWithoutTheGrantOverHTTP(t *testing.T) {
	ctx, ws, db, _ := traceReadWorkspace(t)
	handlers := capture.NewTraceHandlers(capture.NewTraceStore(db), false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity/workspace", nil).
		WithContext(memberContext(ctx, ws, ids.NewV7()))
	handlers.ListWorkspaceCaptureActivity(w, r, crmcontracts.ListWorkspaceCaptureActivityParams{})

	// A refusal, not an empty page: an empty page reads as "your workspace has
	// no shared channels", which is a claim about the data rather than about
	// who may read it.
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
}
