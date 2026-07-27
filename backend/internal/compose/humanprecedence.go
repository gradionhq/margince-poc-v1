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

// precedenceTables is the closed set of record types whose current values
// this file may read, and every one of them names its own table. The set is
// written out HERE rather than derived from a request, because the name is
// interpolated into SQL: a type absent from it reads as unknown and falls
// back to the stricter answer rather than to an identifier built from
// caller input.
var precedenceTables = newRecordTypeSet(
	"person", "organization", "deal", "lead", "activity",
	"offer", "offer_template", "product", "list", "tag",
	"relationship", "custom_field", "saved_view", "webhook_subscription",
)

func newRecordTypeSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// unauditedHolder renders the predicate deciding whether an UNAUDITED patch
// key is human-owned. With a table to read, the answer is "only if the row
// already holds a value there" — filling an empty field undoes nobody, so it
// stays auto-execute. Without one, every unaudited key on a human-created
// record is treated as theirs.
func unauditedHolder(table string) string {
	if table == "" {
		return "true"
	}
	return `EXISTS (
			    SELECT 1 FROM ` + table + ` t
			    WHERE t.id = $2 AND to_jsonb(t) -> p.key IS NOT NULL
			      AND to_jsonb(t) -> p.key <> 'null'::jsonb
			      AND to_jsonb(t) -> p.key <> '""'::jsonb
			  )`
}

// HumanOwnedConflicts names the patch fields whose latest audited write
// was human AND whose proposed value differs from that write's value —
// plus, on a record a HUMAN created, every patch field with no audit
// history at all. Equal values are never a conflict: re-stating what the
// human already typed overwrites nothing.
//
// That second clause is the fail-closed half, and it exists because the
// create paths do not audit what the human typed. Each of them records a
// single headline key — {name} for a deal, {full_name} for a person,
// {display_name} for an organization — while the UPDATE paths record the
// real per-field before/after images. So the premise this file opens with
// holds for updates and is false for creates, and a deal a human created
// in the shipped form (which sends name, amount_minor, currency,
// expected_close_date and the custom fields in ONE post) had exactly one
// protected field and the rest unguarded. An agent could rewrite the money
// and the forecast date at the auto-execute tier with no approval staged.
//
// The unaudited half is narrowed by the record's CURRENT value, read from
// the row itself: a field that is still empty has nothing a human could
// have typed and nothing an agent could undo, so filling it stays 🟢. A
// field that already holds a value on a human-created record might be that
// human's, the trail cannot say, and the tie goes to asking them.
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
	// The partner extension audits on its organization row (the table is
	// keyed by organization_id and the route's {id} IS the org) — the
	// ownership question reads the trail where those writes actually land.
	if entityType == "partner" {
		entityType = "organization"
	}
	// No table to read the current value from means the unaudited half
	// cannot be narrowed; the empty name makes it fail closed, treating
	// every unaudited patch key on a human-created record as theirs.
	table := ""
	if precedenceTables[entityType] {
		table = entityType
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
			  ORDER BY p.key, a.occurred_at DESC, a.id DESC
			),
			human_created AS (
			  SELECT EXISTS (
			    SELECT 1 FROM audit_log a
			    WHERE a.entity_type = $1 AND a.entity_id = $2
			      AND a.action = 'create' AND a.actor_type = 'human'
			  ) AS yes
			)
			SELECT key FROM latest
			WHERE actor_type = 'human' AND proposed_value IS DISTINCT FROM current_value
			UNION
			SELECT p.key FROM proposed p, human_created h
			WHERE h.yes
			  AND NOT EXISTS (SELECT 1 FROM latest l WHERE l.key = p.key)
			  AND `+unauditedHolder(table)+`
			ORDER BY 1`,
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
