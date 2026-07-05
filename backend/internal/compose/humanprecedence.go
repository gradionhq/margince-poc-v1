// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The human-edit-precedence lookup (interfaces.md §2.1, B-EP06.14):
// "human-typed" is a property of the audit trail, not of a separate
// per-field provenance store — a field is human-owned if the most
// recent write of its CURRENT value had actor_type=human. The audit
// before/after images are already per-field (storekit.Patch records
// changed columns), so one indexed scan answers the question.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type fieldOwnership struct {
	pool *pgxpool.Pool
}

// HumanOwnedConflicts names the patch fields whose latest audited write
// was human AND whose proposed value differs from that write's value.
// A field with no audit history (or last written by an agent/system)
// is not a conflict; equal values are not a conflict either — re-stating
// what the human already typed overwrites nothing.
func (f fieldOwnership) HumanOwnedConflicts(ctx context.Context, entityType string, id ids.UUID, patch json.RawMessage) ([]string, error) {
	if len(patch) == 0 {
		return nil, nil
	}
	// Validate the patch is an object before it reaches SQL; jsonb_each
	// on a non-object raises inside the transaction otherwise.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(patch, &probe); err != nil {
		return nil, fmt.Errorf("compose: human-edit precedence: patch is not a JSON object: %w", err)
	}
	if len(probe) == 0 {
		return nil, nil
	}
	var conflicts []string
	err := database.WithWorkspaceTx(ctx, f.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH proposed AS (
			  SELECT key, value FROM jsonb_each($3::jsonb)
			),
			latest AS (
			  SELECT DISTINCT ON (p.key) p.key, p.value AS proposed_value,
			         a.actor_type, a.after -> p.key AS current_value
			  FROM proposed p
			  JOIN audit_log a
			    ON a.entity_type = $1 AND a.entity_id = $2 AND a.after ? p.key
			  ORDER BY p.key, a.created_at DESC, a.id DESC
			)
			SELECT key FROM latest
			WHERE actor_type = 'human' AND proposed_value IS DISTINCT FROM current_value
			ORDER BY key`,
			entityType, id, patch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var field string
			if err := rows.Scan(&field); err != nil {
				return err
			}
			conflicts = append(conflicts, field)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("compose: human-edit precedence: %w", err)
	}
	return conflicts, nil
}
