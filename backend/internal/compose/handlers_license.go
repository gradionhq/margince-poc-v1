// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The entitlement surface: what the license grants, and how much of it is used.
//
// Composed here because the answer has two halves that live in different layers
// and must not import each other — the posture is platform's (resolved from the
// deployment file against the bundled validation module) and the seat count is
// identity's (rows in app_user). Neither half is a sensible home for the other,
// and the verdict that pairs them — whether the installation is over its limit —
// belongs where both are already in scope.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
)

type licenseHandlers struct {
	seats *identity.SeatUsageStore
	// posture answers what this process last resolved. A function, not a value:
	// the watcher re-checks while the process runs, so a screen opened on day
	// thirty reports the license as it stands and not as it booted.
	posture func() licensecheck.Posture
}

// GetLicenseEntitlement answers the entitlement and the usage measured against
// it.
//
// The seat count comes first because it carries the RBAC gate: the store refuses
// a caller without the `license` read grant, so a role that may not see the
// installation's commercial standing never reaches the posture either.
func (h licenseHandlers) GetLicenseEntitlement(w http.ResponseWriter, r *http.Request) {
	if h.seats == nil || h.posture == nil {
		httperr.NotImplemented(w, r, "GetLicenseEntitlement")
		return
	}
	// Human-only (x-agent-access), like every sibling governance read: what an
	// installation is entitled to is not reconnaissance to hand an agent, even
	// one carrying an admin's passport.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	used, err := h.seats.FullSeatsInUse(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractLicenseEntitlement(h.posture(), used))
}

// toContractLicenseEntitlement renders one posture and one count onto the wire.
//
// The reason a rejection carries is deliberately NOT here. It is the module's
// text about the installation's own configuration — and the module quotes token
// content it has not verified — so it belongs in the boot error and the process
// log, never in a response.
func toContractLicenseEntitlement(posture licensecheck.Posture, used int) crmcontracts.LicenseEntitlement {
	out := crmcontracts.LicenseEntitlement{
		State:     crmcontracts.LicenseEntitlementState(posture.State),
		SeatsUsed: used,
		CheckedAt: posture.CheckedAt,
	}
	granted, capped := posture.Seats()
	if !capped {
		// Absent, never zero. A client rendering a missing cap as 0 would tell an
		// admin their license permits nobody, and `over_limit` stays false because
		// there is no limit to be over — not because the installation is inside one.
		return out
	}
	out.SeatsGranted = &granted
	out.OverLimit = used > granted
	return out
}
