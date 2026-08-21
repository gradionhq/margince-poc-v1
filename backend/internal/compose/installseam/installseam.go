// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package installseam names the installation values a module reads and the
// readers that answer them. It is a leaf: it imports the module that OWNS the
// settings and the modules that consume them, and nothing imports it back.
//
// It exists so there is ONE spelling of that wiring. Compose owns cross-module
// edges (ADR-0054), but the integration harness cannot reach compose — the
// harness is a non-test file in a package compose's own tests import, so the
// import is a cycle — and a second copy built by hand there is exactly the
// drift NewSettingsStore was extracted to prevent.
package installseam

import (
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// Deals is the installation seam the deals module reads through.
func Deals() deals.Installation {
	return deals.Installation{
		Name:         identity.NameOf,
		BaseCurrency: identity.BaseCurrencyOf,
		Timezone:     identity.TimezoneOf,
		// activities owns `activity`, so the stamp's write lives there and the
		// edge is injected here (ADR-0054).
		StampCorrespondence: activities.StampCorrespondenceForDeal,
		// people owns `partner`, so the "is this company a partner" read lives
		// there and the edge is injected here for the same reason.
		EnsurePartner: people.EnsureOrganizationIsPartner,
	}
}
