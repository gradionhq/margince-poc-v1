// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The claim seam the flip's integration lane needs and nothing else
// does. It lives behind the integration tag so it is never linked into
// cmd/api or cmd/worker: the lane compiles this package with the tag,
// so it still takes the REAL advisory claim, while the shipped binaries
// carry no exported surface with no product caller.
//
// ReconstructForTest deliberately does NOT live here: the rebuild it
// wraps has no product caller either (the /import/* wire is IEM-GAP-2's
// contract extension), so tag-gating its only caller would make the
// whole rebuild path read as dead code to every untagged build.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ClaimFlipForTest takes the flip's real advisory claim, so the lane can
// prove the claim and FlipImportProbe key on the same lock. A fake would
// defeat the point, and claimFlip is bound to a flipRunner the lane has
// no reason to build. The returned release is idempotent.
func ClaimFlipForTest(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	runner := &flipRunner{pool: pool, log: slog.New(slog.DiscardHandler)}
	return runner.claimFlip(ctx)
}
