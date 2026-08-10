// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The workspace row has two halves. Bootstrap writes the installation's
// IDENTITY from the deployment configuration file — name, slug, base
// currency, timezone (installation.go) — and every other column on the row is
// CONFIGURATION: it arrives at the default its migration declared and is
// changed later through a settings surface. ResetWorkspaceConfig below
// restores the second half to those defaults. The first half is what a data
// reset must not touch, because the installation itself survives the reset.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// preservedWorkspaceColumns are the workspace columns the restore does not
// assign: the primary key, the four values bootstrap takes from the deployment
// configuration, and the row's own lifecycle timestamps. A reset wipes an
// installation's DATA — it does not re-create the installation, so its name,
// currency, zone and age outlive it.
//
// updated_at is listed for a different reason than the rest. The reset really
// does write this row, so trg_workspace_updated moves that column, and it
// should: it records when the row was last written, which is now. It is here
// because assigning it would take it from the trigger, not because the write
// is meant to be invisible — a reset that left it untouched would report the
// row as unwritten since before the reset, which is precisely the reading that
// exposed this bug in the first place.
//
// Everything NOT listed here is configuration and is restored. That direction
// is deliberate: a column added later is restored by default rather than
// silently escaping the reset, and a column that genuinely belongs to the
// installation's identity has to be declared here to be spared.
var preservedWorkspaceColumns = map[string]bool{
	"id": true, "name": true, "slug": true, "base_currency": true,
	"timezone": true, "created_at": true, "updated_at": true, "archived_at": true,
}

// workspaceConfigColumns lists the workspace columns a reset restores — every
// column of the table that is not preserved, derived from the catalog for the
// same reason the data reset derives its table sweep from one: a setting added
// later is restored automatically instead of escaping a hand-kept list.
//
// The set can be EMPTY on a core-only tree — core contributes no configuration
// column to this row today, only identity — and the caller handles that.
func workspaceConfigColumns(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname = 'workspace'
		  AND c.relkind = 'r'
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attname`)
	if err != nil {
		return nil, fmt.Errorf("identity: listing the workspace's configuration columns: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !preservedWorkspaceColumns[name] {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// ResetWorkspaceConfig returns the bound workspace's configuration columns to
// their declared defaults inside the caller's transaction — the workspace-row
// half of the non-production data reset.
//
// It exists because that reset's table sweep structurally cannot reach this
// row: the sweep targets tables carrying a workspace_id column, and workspace
// keys on id, so no workspace-level setting is in its candidate set at all.
// Without this every such setting survived a reset that reported the
// installation returned to first-boot state.
//
// `= DEFAULT` rather than values spelled here: the default belongs to the
// migration that declared the column, and a copy in Go is a second place for
// it to be wrong. What keeps that safe as columns are added is a fitness
// rail, not review — see workspaceconfig_integration_test.go.
//
// The caller gates: this is the reset orchestration's step, already behind
// that endpoint's non-production + human + admin + typed-confirmation chain,
// exactly as RevertToNative sits behind Disconnect's.
//
// The GUC is read WITHOUT missing_ok, for the reason RevertToNative gives:
// with it, an unset app.workspace_id would resolve to NULL, match no row, and
// let this report success having restored nothing. Zero rows updated is
// refused for the other half of that: unlike RevertToNative, this statement
// has no predicate beyond the id, so there is no reading under which it
// legitimately matches nothing.
func ResetWorkspaceConfig(ctx context.Context, tx pgx.Tx) error {
	cols, err := workspaceConfigColumns(ctx, tx)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		// A workspace row that is identity and nothing else has nothing to
		// restore, and returning early is the whole of the correct behaviour.
		//
		// This IS reachable now. capture_auto_enrich was core's only
		// configuration column on this row, and it moved into `setting`; what
		// remains — x_sor_mode, x_incumbent — comes from the fork-owned custom
		// namespace (ADR-0054 §7), which upstream ships empty. So a core-only
		// tree takes this branch. That is correct rather than a gap: with no
		// configuration column there is nothing a reset could restore, and the
		// settings that DID move are restored by platform/settings.ResetConfig
		// on the same path.
		//
		// It matters to whoever reads the tests: their assertions run against
		// a schema the overlay pack has migrated, so they exercise fork
		// columns. A fork that removes that pack makes them vacuous, and the
		// vacuity check in workspaceconfig_integration_test.go is what says so.
		return nil
	}
	assignments := make([]string, 0, len(cols))
	for _, col := range cols {
		assignments = append(assignments, pgx.Identifier{col}.Sanitize()+" = DEFAULT")
	}
	// One statement, never one per column: the row carries CHECK constraints
	// spanning two columns at once (overlay's x_overlay_iff_incumbent), and a
	// column-at-a-time restore would have to pass through the state they forbid.
	tag, err := tx.Exec(ctx, `UPDATE workspace SET `+strings.Join(assignments, ", ")+
		` WHERE id = current_setting('app.workspace_id')::uuid`)
	if err != nil {
		return fmt.Errorf("identity: restoring the workspace's configuration columns: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("identity: restoring the workspace's configuration columns: " +
			"the bound app.workspace_id names no workspace row, so nothing was restored")
	}
	return nil
}
