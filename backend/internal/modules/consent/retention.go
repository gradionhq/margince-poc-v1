// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// The retention-policy defaults (data-model §3.4): a new workspace is
// compliant out-of-the-box with conservative GDPR storage-limitation
// rows, editable per workspace. One action per row — the ladder is
// separate rows at increasing retain_days.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SeedDefaultRetentionTx plants the §3.4 default-of-record inside the
// workspace-bootstrap transaction, same C5 atomicity as the purpose
// catalog.
func SeedDefaultRetentionTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action, lawful_basis)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, v.object_type, v.category, v.retain_days, v.action, 'storage_limitation'
		FROM (VALUES
		  ('lead',     'unconverted',        365,  'anonymize'),
		  ('activity', NULL,                 1095, 'archive'),
		  ('activity', 'transcript',         365,  'erase'),
		  ('person',   'no_consent_no_deal', 730,  'anonymize'),
		  ('deal',     'lost',               1825, 'archive'),
		  ('ai_call_payload', 'content',     365,  'erase')
		) AS v(object_type, category, retain_days, action)`)
	return err
}
