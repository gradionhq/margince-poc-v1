// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Writing the fields a human states about the installation's own company —
// from the company form, and from the human-edited half of a site-read
// confirmation.
//
// A value lands on its provenance row always, and on an organization column
// when the field is one of the few that is column-backed; clearing a value
// deletes the provenance row rather than storing a blank one. The rows carry
// source=human and no evidence snippet, because on this path the human IS the
// evidence.

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
	renamed := false
	for _, spec := range companyFields {
		field := spec.name
		value, sent := fields[field]
		if !sent || value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if spec.update != "" {
			moved, err := setCompanyColumn(ctx, tx, orgID, spec, trimmed)
			if err != nil {
				return nil, err
			}
			renamed = renamed || (moved && field == fieldLegalName)
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
	// A legal name is the axis on which two records of one company converge, and
	// this function is the only writer of that column a human drives — through
	// the company form AND through a site-read confirmation, which is why the
	// re-check lives here rather than at either call site. Both callers took the
	// name lock in resolveOrCreateAnchor, ahead of the row lock, so the ordering
	// already holds.
	if renamed {
		if err := recheckOrgNameForDuplicates(ctx, tx, orgID, by); err != nil {
			return nil, err
		}
	}
	return applied, nil
}

// setCompanyColumn writes a column-backed field, clearing it to NULL rather
// than storing an empty string — an unfilled field reads as absent, never as
// the empty answer.
// setCompanyColumn writes one column-backed field and reports whether the
// value actually moved. Every statement carries IS DISTINCT FROM, so a
// resubmission of an unchanged form touches no row and answers false — which is
// what keeps the duplicate re-check below off a save that renamed nothing.
func setCompanyColumn(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, spec companyField, value string) (bool, error) {
	var stored *string
	if value != "" {
		stored = &value
	}
	tag, err := tx.Exec(ctx, spec.update, orgID, stored)
	if err != nil {
		return false, fmt.Errorf("set %s: %w", spec.name, err)
	}
	return tag.RowsAffected() > 0, nil
}
