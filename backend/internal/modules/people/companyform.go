// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Writing the company form's fields. The form is the one surface where a human
// states the installation's own company directly, so each value lands on its
// column AND on its provenance row with source=human — the human IS the
// evidence, which is why these writes carry no snippet.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// writeCompanyFields applies the submitted fields: the column-backed ones onto
// their column (a human's own form overwrites — unlike a read-back, which only
// fills blanks), and every one onto its provenance row. Returns what changed,
// for the audit delta.
func writeCompanyFields(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, by string, fields map[string]*string) (map[string]any, error) {
	applied := map[string]any{}
	for _, spec := range companyFields {
		field := spec.name
		value, sent := fields[field]
		if !sent || value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if spec.update != "" {
			if err := setCompanyColumn(ctx, tx, orgID, spec, trimmed); err != nil {
				return nil, err
			}
		}
		if trimmed == "" {
			if _, err := tx.Exec(ctx,
				`DELETE FROM organization_profile_field
				 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3`,
				workspaceID(ctx), orgID, field); err != nil {
				return nil, fmt.Errorf("clear company field %s: %w", field, err)
			}
			applied[field] = nil
			continue
		}
		// A human-typed value has no snippet to quote — the human IS the
		// evidence, which is what source=human + captured_by=human:<id>
		// record. Confidence is 1: they are not guessing about themselves.
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, $3, $4, '', '', 1, 'human', $5)
			ON CONFLICT (workspace_id, organization_id, field)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = '', source_url = '',
			              confidence = 1, source = 'human',
			              captured_by = EXCLUDED.captured_by, captured_at = now()`,
			workspaceID(ctx), orgID, field, trimmed, by); err != nil {
			return nil, fmt.Errorf("save company field %s: %w", field, err)
		}
		applied[field] = trimmed
	}
	return applied, nil
}

// setCompanyColumn writes a column-backed field, clearing it to NULL rather
// than storing an empty string — an unfilled field reads as absent, never as
// the empty answer.
func setCompanyColumn(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, spec companyField, value string) error {
	var stored *string
	if value != "" {
		stored = &value
	}
	if _, err := tx.Exec(ctx, spec.update, orgID, stored); err != nil {
		return fmt.Errorf("set %s: %w", spec.name, err)
	}
	return nil
}
