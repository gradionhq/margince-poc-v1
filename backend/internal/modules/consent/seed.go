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
// double-opt-in norm from day one; business_correspondence is the lane
// for answering somebody who wrote to us, which ADR-0098 rules is not
// advertising and therefore not consent-gated at all.
func SeedDefaultPurposesTx(ctx context.Context, tx pgx.Tx) error {
	// transactional and business_correspondence are seeded by SeedPurposesTx
	// itself as module invariants; only the marketing lane is a default.
	return SeedPurposesTx(ctx, tx, []PurposeSeed{
		{Key: "marketing_email", Label: "Marketing email", DoubleOptIn: true, Class: ClassMarketing},
	})
}

// PurposeSeed is one configured row of the purpose catalog
// (A107/ADR-0061: the deployment file may shape the bootstrap catalog).
type PurposeSeed struct {
	Key         string
	Label       string
	DoubleOptIn bool
	// Class decides which gate the purpose answers to. An operator-configured
	// entry that names none is treated as marketing — the strict lane — because
	// the safe direction for an unclassified purpose is the one that asks for
	// consent rather than the one that assumes it.
	Class Class
}

// classOrMarketing defaults an unnamed class to the strict lane.
func (p PurposeSeed) classOrMarketing() Class {
	if p.Class == "" {
		return ClassMarketing
	}
	return p.Class
}

// SeedPurposesTx plants the configured purpose catalog. The two
// non-consent lanes are always seeded first, whatever the configuration
// says; a configured entry naming either key would collide and is
// rejected by the catalog's uniqueness.
func SeedPurposesTx(ctx context.Context, tx pgx.Tx, purposes []PurposeSeed) error {
	// The two non-consent lanes are module invariants, not operator choices.
	// transactional carries operational mail (password reset, invites);
	// business_correspondence carries the reply to somebody who wrote to us,
	// which ADR-0098 rules is not advertising. A deployment that configured its
	// own catalog and thereby lost them would be an installation where a rep
	// cannot answer a customer — and the failure would read as correct
	// strictness rather than as a missing row.
	for _, invariant := range []PurposeSeed{
		{Key: "transactional", Label: "Transactional email", Class: ClassTransactional},
		{Key: "business_correspondence", Label: "Business correspondence", Class: ClassBusinessCorrespondence},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in, class)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, false, $3)`,
			invariant.Key, invariant.Label, invariant.Class); err != nil {
			return err
		}
	}
	for _, p := range purposes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in, class)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)`,
			p.Key, p.Label, p.DoubleOptIn, p.classOrMarketing()); err != nil {
			return err
		}
	}
	return nil
}
