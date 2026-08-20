// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package compose

import (
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
)

// liveIncumbentFactory builds a HubSpot adapter over one connection's own
// region + vaulted token — the per-connection seam Connect's mirror_user_map
// seeding resolves the owners directory through. It is the ONE place compose
// binds the concrete incumbent for the connection lifecycle (the reconcile
// poller builds its own the same way, jobs_overlay.go's reconcileConnection);
// the overlay module never selects an incumbent itself (ADR-0054 §8 — concrete
// choice injected at compose).
//
// Split by build tag. This is the production half; overlayincumbent_refusing.go
// is the integration one, and it refuses rather than dials — see there.
//
//nolint:ireturn // returns the overlay.Incumbent seam by design — it is injected as a per-connection factory the module holds behind the interface, so tests substitute a fake.
func liveIncumbentFactory(region, token string) overlay.Incumbent {
	return hubspot.NewAdapter(hubspot.NewClient(region, token))
}
