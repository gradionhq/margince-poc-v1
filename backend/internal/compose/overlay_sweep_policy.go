// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// This file is the overlay reconcile sweep's object-class policy: which
// HubSpot classes the poller sweeps, and — for each — whether a failure
// sweeping it must abort the whole connection sweep or is tolerated as a
// best-effort miss. Kept beside jobs_overlay.go's sweep loop (its only
// caller) but in its own file so that loop stays focused on orchestration.

import (
	"errors"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// overlayObjectClasses are the HubSpot object classes design.md §9 maps —
// the poller sweeps each, per due connection, resuming each object class's
// own persisted watermark. The five engagement classes
// (calls/meetings/emails/notes/tasks) are swept separately: HubSpot v3 has
// no generic engagements object, and each maps to its own activity kind
// (OVA-MAP-1).
var overlayObjectClasses = append([]string{
	overlay.IncumbentClassContacts, overlay.IncumbentClassCompanies,
	overlay.IncumbentClassDeals, overlay.IncumbentClassLeads,
}, overlay.IncumbentEngagementClasses()...)

// scopeBackedOverlayClass reports whether the overlay connection requests a
// read scope for objectClass. connection.go's leastPrivilegeHubSpotScopes
// covers exactly contacts/companies/deals; leads and the engagement classes
// are swept best-effort with no requested scope. Kept in lockstep with that
// scope list — a class added there must be added here.
func scopeBackedOverlayClass(objectClass string) bool {
	switch objectClass {
	case overlay.IncumbentClassContacts, overlay.IncumbentClassCompanies, overlay.IncumbentClassDeals:
		return true
	default:
		return false
	}
}

// overlaySweepAborts decides whether a connection-level sweep error for
// objectClass must abort the whole connection sweep. A scope-backed class
// (contacts/companies/deals) always aborts on a connection-level error — a
// 403/404 there means the token we DO hold scope with was revoked or
// downscoped. A best-effort class (leads and the engagement classes, swept
// with no requested scope) never breaks the classes we can sync — but it still
// aborts on a CONNECTION-WIDE condition: budget exhaustion (continuing to spend
// against an exhausted quota is pointless) or ErrConnectionGone (a disconnect
// racing the sweep — the connection no longer exists, so the on-demand
// reconciler must see this to collapse it to ErrModeNotOverlay, and the poller
// must not reset backoff on a dead connection). Every per-OBJECT failure — a
// missing scope (403), an absent object (404), or a validation 400 on an
// engagement endpoint this portal shapes differently — is skipped for a
// best-effort class, because these were swept without validation against every
// portal shape and a scope-backed outage would already have aborted earlier in
// the loop (the scope-backed classes are swept first).
func overlaySweepAborts(objectClass string, err error) bool {
	if scopeBackedOverlayClass(objectClass) {
		return true
	}
	return errors.Is(err, apperrors.ErrIncumbentBudgetExhausted) ||
		errors.Is(err, overlay.ErrConnectionGone)
}
