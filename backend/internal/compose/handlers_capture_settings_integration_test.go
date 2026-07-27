// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The /capture/settings HTTP handlers over a real pool (CAP-WIRE-7,
// ADR-0072/A118): a GET returns the workspace posture, a PATCH from an admin
// toggles it. Thin transport, so this only proves the wire shape and the
// store-gate reach; the RBAC/audit behaviour is the store's own integration
// test.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func captureSettingsHumanCtx(e *integration.Env, grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestCaptureSettingsHandlers(t *testing.T) {
	e := integration.Setup(t)
	h := captureSettingsHandlers{store: capture.NewSettings(e.Pool)}

	// GET returns the default-on posture.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/capture/settings", nil).
		WithContext(captureSettingsHumanCtx(e, principal.ObjectGrant{Read: true}))
	getRec := httptest.NewRecorder()
	h.GetCaptureSettings(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %s)", getRec.Code, getRec.Body)
	}
	var got crmcontracts.CaptureSettings
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if !got.AutoEnrich {
		t.Fatal("GET auto_enrich = false, want the default true")
	}

	// PATCH from an admin (read+update) turns it off and echoes the new state.
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/capture/settings",
		strings.NewReader(`{"auto_enrich":false}`)).
		WithContext(captureSettingsHumanCtx(e, principal.ObjectGrant{Read: true, Update: true}))
	patchRec := httptest.NewRecorder()
	h.UpdateCaptureSettings(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200 (body %s)", patchRec.Code, patchRec.Body)
	}
	var patched crmcontracts.CaptureSettings
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode PATCH: %v", err)
	}
	if patched.AutoEnrich {
		t.Fatal("PATCH auto_enrich = true, want false after the toggle")
	}
}
