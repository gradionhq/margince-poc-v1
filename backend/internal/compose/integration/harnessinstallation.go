// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integration

// The installation half of the harness: the settings rows that make the
// fixture an installation rather than a bare workspace row, and the seam the
// modules read them through.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

// seedInstallationIdentity writes the installation's own settings rows.
func seedInstallationIdentity(ctx context.Context, t *testing.T, owner *pgx.Conn) {
	t.Helper()
	// The installation's identity as settings rows (ADR-0090/A135) — the other
	// half of the same fact. This harness builds the installation by raw SQL,
	// so bootstrap never seeded them and 0191's backfill ran before the
	// workspace existed, while the readers resolve the SETTINGS, not the
	// columns (issue #521). All three, because bootstrap writes all three:
	// name, currency and zone are one act, and a fixture holding some of them
	// is a state no installation is ever in.
	//
	// They must match the columns above. A suite whose two copies disagree
	// measures the drift rather than the behaviour under test — except where
	// the disagreement IS the test, which is what basecurrencyseam does.
	if _, err := owner.Exec(ctx,
		`INSERT INTO setting (key, value) VALUES
			('installation.name', '"Authz"'::jsonb),
			('installation.base_currency', '"EUR"'::jsonb),
			('installation.timezone', '"UTC"'::jsonb)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
}

// harnessInstallation is compose.DealsInstallation, spelled again.
//
// It cannot be shared: this file is a NON-test file in the integration
// package, and compose's own tests import this package — so importing compose
// here is an import cycle. The readers themselves are still identity's, which
// is what keeps the duplication to the shape of the struct rather than to how
// a setting is read.
func harnessInstallation() deals.Installation {
	return deals.Installation{
		Name:         identity.NameOf,
		BaseCurrency: identity.BaseCurrencyOf,
		Timezone:     identity.TimezoneOf,
	}
}
