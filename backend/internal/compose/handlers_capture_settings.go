// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The workspace capture-settings surface (CAP-WIRE-7, ADR-0072/A118): read the
// captured-organization auto-enrich posture (every role), toggle it (admin/ops,
// human-only). Thin transport — the capture store owns the RBAC gate and the
// audit-only write.

import (
	"encoding/json"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type captureSettingsHandlers struct {
	store *capture.SettingsStore
}

func (h captureSettingsHandlers) GetCaptureSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "GetCaptureSettings")
		return
	}
	settings, err := h.store.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCaptureSettings(settings))
}

func (h captureSettingsHandlers) UpdateCaptureSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "UpdateCaptureSettings")
		return
	}
	// Human-only (x-agent-access): an agent never changes a workspace-wide
	// capture posture. The store re-checks the admin/ops object grant.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.UpdateCaptureSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	settings, err := h.store.Update(r.Context(), req.AutoEnrich)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCaptureSettings(settings))
}

// toContractCaptureSettings maps the stored posture onto the wire shape.
func toContractCaptureSettings(s capture.Settings) crmcontracts.CaptureSettings {
	return crmcontracts.CaptureSettings{AutoEnrich: s.AutoEnrich}
}
