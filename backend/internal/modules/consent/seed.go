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
	_, err := tx.Exec(ctx, `
		INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in)
		VALUES
		  (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'transactional', 'Transactional email', false),
		  (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'marketing_email', 'Marketing email', true)`)
	return err
}
