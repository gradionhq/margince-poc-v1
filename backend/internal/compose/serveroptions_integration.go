// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// WithOverlayIncumbentResolver is a test-only Option, gated behind the
// integration build tag so it can never link into a production binary
// (cmd/api, cmd/worker, cmd/mcp build untagged). No cmd/ role calls it —
// every real deployment reaches HubSpot only through WithKeyvault's
// vaulted region+token path. It exists because compose.New returns a bare
// http.Handler (nothing exposes Server or its Dispatcher to a caller
// outside this package) and the integration suite that exercises the
// write-back seam over the real HTTP surface needs to substitute
// overlay/fake for the live hubspot.Adapter WithKeyvault would otherwise
// build — there is no other seam to reach that resolver from outside
// compose.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// WithOverlayIncumbentResolver overrides the per-request live-incumbent
// resolver the overlay read dispatch's force-fresh lane AND the write-back
// seam (Create/Update/Archive) both consult, replacing whatever
// WithKeyvault would otherwise wire from the connection's own vaulted
// region+token. Apply it AFTER WithKeyvault in the Option list:
// WithKeyvault's own SetOverlayIncumbentResolver call would otherwise win
// by running later.
func WithOverlayIncumbentResolver(resolve func(context.Context) (overlay.Incumbent, error)) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.sorDispatch.SetOverlayIncumbentResolver(resolve) }
}
