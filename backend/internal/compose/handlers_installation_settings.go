// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The installation-settings surface (ADR-0090/A135): read the organization's
// name, reporting zone and base currency (every role), change them (admin/ops,
// human-only). Thin transport — the identity store owns the RBAC gate, the
// per-setting validation, the base-currency freeze and the audit-only write.

import (
	"encoding/json"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type installationSettingsHandlers struct {
	store *identity.InstallationSettingsStore
}

func (h installationSettingsHandlers) GetInstallationSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "GetInstallationSettings")
		return
	}
	s, err := h.store.GetInstallation(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractInstallationSettings(s))
}

func (h installationSettingsHandlers) UpdateInstallationSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "UpdateInstallationSettings")
		return
	}
	// Human-only (x-agent-access): an agent never renames the organization or
	// re-bases its reporting currency. The store re-checks the admin/ops grant.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.UpdateInstallationSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	s, err := h.store.UpdateInstallation(r.Context(), req.Name, req.Timezone, req.BaseCurrency)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractInstallationSettings(s))
}

// toContractInstallationSettings maps the stored values onto the wire shape.
// The lock reason is omitted rather than sent empty when the currency is still
// changeable — an empty string would render as a reason that says nothing.
func toContractInstallationSettings(s identity.InstallationSettings) crmcontracts.InstallationSettings {
	out := crmcontracts.InstallationSettings{
		Name:               s.Name,
		Timezone:           s.Timezone,
		BaseCurrency:       s.BaseCurrency,
		BaseCurrencyLocked: s.BaseCurrencyLocked,
	}
	if s.BaseCurrencyLockedReason != "" {
		reason := s.BaseCurrencyLockedReason
		out.BaseCurrencyLockedReason = &reason
	}
	return out
}
