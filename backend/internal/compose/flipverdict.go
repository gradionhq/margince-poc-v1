// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's readiness verdict: the one place that decides whether a
// workspace may cut over, and — when it may not — which named gate is
// unsatisfied. Both the preflight and the execute admit through this, so
// a gate added here binds on both paths by construction.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// flipVerdict is the runner's internal readiness read: the raw checks
// plus the derived blocking reasons for a fresh-sync flip.
type flipVerdict struct {
	checks   overlay.FlipChecks
	blocking []crmcontracts.OverlayFlipPreflightBlocking
}

func (f *flipRunner) verdict(ctx context.Context) (flipVerdict, error) {
	checks, err := f.svc.FlipChecks(ctx)
	if err != nil {
		return flipVerdict{}, err
	}
	v := flipVerdict{checks: checks}
	// The same guard the direct importer runs (one constant, one rule):
	// a live-read import cannot pass with the connection revoked/error.
	if err := migration.GuardIncumbentSource(checks.ConnectionStatus); err != nil {
		v.blocking = append(v.blocking, crmcontracts.IncumbentUnreachable)
	}
	if !checks.ForceFreshDone {
		v.blocking = append(v.blocking, crmcontracts.ForceFreshIncomplete)
	}
	if checks.PendingSyncCount > 0 {
		v.blocking = append(v.blocking, crmcontracts.PendingSyncDraining)
	}
	exported, err := f.exportSince(ctx, exportCutoff(checks))
	if err != nil {
		return flipVerdict{}, err
	}
	if !exported {
		v.blocking = append(v.blocking, crmcontracts.ExportMissing)
	}
	return v, nil
}

// exportCutoff is the instant a pre-flip bundle must post-date: the
// later of the mirror's freshest row and the last successful sweep. The
// sweep is what makes the gate mean something on an estate with no rows
// to stamp a watermark — against a bare zero, an export written years
// before the incumbent was ever connected would satisfy it.
func exportCutoff(checks overlay.FlipChecks) time.Time {
	if checks.LastSweepAt.After(checks.LastSyncedAt) {
		return checks.LastSweepAt
	}
	return checks.LastSyncedAt
}

// exportSince answers whether a workspace export bundle was written
// after the flip's export cutoff — the preflight's "honest-scope
// export available" check (B-E18.26). The export audit row is the
// bundle writer's own (export.go); a bundle older than the mirror's
// last change no longer captures the estate the flip will migrate.
func (f *flipRunner) exportSince(ctx context.Context, since time.Time) (bool, error) {
	if since.IsZero() {
		// Neither a mirrored row nor a successful sweep: there is no
		// instant an export could be newer than, and `occurred_at >= 0001`
		// would match every export ever written — including one taken
		// before the incumbent was connected at all. Nothing is proven, so
		// the gate reports nothing proven.
		return false, nil
	}
	var ok bool
	err := database.WithWorkspaceTx(ctx, f.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM audit_log
				WHERE entity_type = 'workspace' AND action = 'export' AND occurred_at >= $1)`,
			since,
		).Scan(&ok)
	})
	if err != nil {
		return false, fmt.Errorf("flip preflight: checking for a pre-flip export: %w", err)
	}
	return ok, nil
}
