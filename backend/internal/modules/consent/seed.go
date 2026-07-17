// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SeedDefaultPurposesTx plants the core purpose catalog inside the
// workspace-bootstrap transaction (the C5 atomicity rule: a failed seed
// rolls the whole tenant back). transactional exists so operational
// mail has a lawful lane; marketing_email carries the German
// double-opt-in norm from day one.
func SeedDefaultPurposesTx(ctx context.Context, tx pgx.Tx) error {
	return SeedPurposesTx(ctx, tx, []PurposeSeed{
		{Key: "marketing_email", Label: "Marketing email", DoubleOptIn: true},
	})
}

// PurposeSeed is one configured row of the purpose catalog
// (A107/ADR-0061: the deployment file may shape the bootstrap catalog).
type PurposeSeed struct {
	Key         string
	Label       string
	DoubleOptIn bool
}

// SeedPurposesTx plants the configured purpose catalog. The
// `transactional` lane is always seeded first, whatever the
// configuration says — operational mail (password reset, invites) needs
// a lawful lane, so its presence is a module invariant, not an operator
// choice; a configured `transactional` entry would collide and is
// rejected by the catalog's uniqueness.
func SeedPurposesTx(ctx context.Context, tx pgx.Tx, purposes []PurposeSeed) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'transactional', 'Transactional email', false)`); err != nil {
		return err
	}
	for _, p := range purposes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
			p.Key, p.Label, p.DoubleOptIn); err != nil {
			return err
		}
	}
	return nil
}
