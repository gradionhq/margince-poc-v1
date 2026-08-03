// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// errResetConfirmationMismatch means the caller's typed confirmation did not
// match the workspace's organization name — the reset is refused before any
// data is touched.
var errResetConfirmationMismatch = errors.New("data reset: confirmation does not match the organization name")

// preservedResetTables are the workspace_id tables a reset must NOT delete:
// the identity/auth layer (so every user, including the admin, stays logged in
// and the org survives) and the append-only ledgers (their immutability trigger
// forbids DELETE, and operational history should outlive a data reset). Every
// other workspace_id table is domain/config data and is swept, then re-seeded.
var preservedResetTables = map[string]bool{
	"app_user": true, "role": true, "role_assignment": true,
	"team": true, "team_membership": true,
	"session": true, "passport": true, "auth_token": true,
	"audit_log": true, "system_log": true,
}

// resetTargetTables lists every public base table carrying a workspace_id
// column that is not preserved — derived from the catalog so a newly added
// tenant table is swept automatically rather than escaping a hand-kept list.
func resetTargetTables(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND c.relname NOT LIKE 'schema_migrations_%'
		  AND a.attname = 'workspace_id'
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !preservedResetTables[name] {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// sweepWorkspaceData deletes every row of the target tables for the bound
// workspace. Running as the non-superuser app role, it cannot disable FK
// triggers, so it discovers a safe order at runtime: each pass tries every
// still-populated table behind a savepoint and defers the ones a child FK still
// blocks to the next pass, until all are clear. A pass with no progress means an
// unbreakable FK cycle — surfaced explicitly, never silently skipped.
func sweepWorkspaceData(ctx context.Context, tx pgx.Tx, tables []string) error {
	remaining := append([]string(nil), tables...)
	for len(remaining) > 0 {
		var stuck []string
		progressed := false
		for _, t := range remaining {
			if _, err := tx.Exec(ctx, "SAVEPOINT reset_sp"); err != nil {
				return err
			}
			_, delErr := tx.Exec(ctx,
				`DELETE FROM `+pgx.Identifier{t}.Sanitize()+
					` WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
			if delErr == nil {
				if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT reset_sp"); err != nil {
					return err
				}
				progressed = true
				continue
			}
			if !isForeignKeyViolation(delErr) {
				return delErr
			}
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT reset_sp"); err != nil {
				return err
			}
			// A rollback leaves the savepoint defined but unusable for a
			// subsequent SAVEPOINT of the same name until it's released —
			// without this, repeated passes over a slow-to-clear table
			// would pile up shadowed savepoints for the life of the tx.
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT reset_sp"); err != nil {
				return err
			}
			stuck = append(stuck, t)
		}
		if !progressed {
			return fmt.Errorf("data reset: unresolved foreign-key cycle among %v", stuck)
		}
		remaining = stuck
	}
	return nil
}

// clearWorkspaceOutbox removes this workspace's staged events. event_outbox is
// infra-owned and has no workspace_id column — tenancy lives in the envelope —
// so the reset must not leave events that point at rows it just deleted.
func clearWorkspaceOutbox(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx,
		`DELETE FROM event_outbox WHERE envelope->>'workspace_id' = current_setting('app.workspace_id')`)
	return err
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// resetSummary is what the handler reports back after a reset.
type resetSummary struct{ TablesCleared int }

// resetDataResponse is the 200 body. The contract declares the shape inline
// (no generated type), so it is spelled here.
type resetDataResponse struct {
	Status        string `json:"status"`
	TablesCleared int    `json:"tables_cleared"`
}

// dataResetHandlers is the callable the non-production "reset data" HTTP
// handler invokes. schemaPool is the owner-privileged pool the
// cf_* column finalize runs on; nil skips that step (no schema pool
// configured — the reset itself still succeeds, only the DDL cleanup is
// skipped). log defaults to slog.Default() when nil.
type dataResetHandlers struct {
	pool       *pgxpool.Pool
	schemaPool *pgxpool.Pool
	seeds      deployconfig.Seeds
	env        runtimeenv.Environment
	log        *slog.Logger
}

// run performs the full reset for the bound workspace: validate the typed
// confirmation, sweep domain + config data, clear the outbox, re-seed module
// defaults (as bootstrap does), and record the reset in audit_log — all in
// one transaction. cf_* column drops run after commit (separate owner
// connection) since DDL cannot share the app-role transaction.
func (h dataResetHandlers) run(ctx context.Context, confirmation string) (resetSummary, error) {
	logger := h.log
	if logger == nil {
		logger = slog.Default()
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return resetSummary{}, database.ErrNoWorkspace
	}
	var cleared int
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		var orgName string
		if err := tx.QueryRow(ctx, `SELECT name FROM workspace WHERE id = $1`, wsID).Scan(&orgName); err != nil {
			return err
		}
		if confirmation != orgName {
			return errResetConfirmationMismatch
		}
		tables, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		if err := sweepWorkspaceData(ctx, tx, tables); err != nil {
			return err
		}
		if err := clearWorkspaceOutbox(ctx, tx); err != nil {
			return err
		}
		cleared = len(tables)

		// Re-seed under a system principal + a fresh correlation id, exactly as
		// bootstrap does (identity/installation.go), so the seeders' own
		// audit+outbox writes trace to one originating operation.
		seedCtx := principal.WithActor(principal.WithWorkspaceID(ctx, wsID), principal.Principal{
			Type: principal.PrincipalSystem, ID: "system",
		})
		seedCtx = principal.WithCorrelationID(seedCtx, ids.NewV7())
		if err := configuredSeed(h.seeds, deals.NewHandlers(h.pool))(seedCtx, tx); err != nil {
			return err
		}

		// Record the reset under the invoking admin principal.
		_, err = storekit.AuditWithEvidence(ctx, tx, "reset_data", "workspace", wsID, nil, nil,
			map[string]any{"tables_cleared": cleared})
		return err
	})
	if err != nil {
		return resetSummary{}, err
	}

	// Finalize: drop custom-field columns so the schema matches a fresh
	// bootstrap. Best-effort — the definitions are already gone with the
	// sweep, and leaving an empty column behind is harmless if this can't
	// run (no schema pool configured); logged, not swallowed.
	if h.schemaPool != nil {
		if err := dropResetCustomFieldColumns(ctx, h.schemaPool); err != nil {
			logger.Error("data reset: cf_ column drop failed", "err", err)
		}
	}
	return resetSummary{TablesCleared: cleared}, nil
}

// dropResetCustomFieldColumns drops every runtime cf_* column via the owner
// pool (the ONE sanctioned ALTER TABLE chokepoint). DROP COLUMN CASCADE also
// removes each column's generated cf_<slug>_check constraint.
func dropResetCustomFieldColumns(ctx context.Context, schemaPool *pgxpool.Pool) error {
	rows, err := schemaPool.Query(ctx, `
		SELECT quote_ident(table_name), quote_ident(column_name)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE 'cf\_%'`)
	if err != nil {
		return err
	}
	type col struct{ table, name string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.name); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cols {
		if _, err := schemaPool.Exec(ctx, `ALTER TABLE `+c.table+` DROP COLUMN `+c.name+` CASCADE`); err != nil {
			return err
		}
	}
	return nil
}

// ResetData wipes a non-production installation to its first-boot state.
// Gate order, fail-closed: environment first (production has no such
// endpoint, checked before any auth so a misconfigured deployment never
// leaks that the operation exists) → human-only (an agent never wipes
// tenant data) → admin-only → the typed confirmation run enforces.
func (h dataResetHandlers) ResetData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.pool == nil || !h.env.IsNonProduction() {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	if err := auth.RequireHuman(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := auth.RequireAdmin(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.ResetDataJSONRequestBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	summary, err := h.run(ctx, req.Confirmation)
	if errors.Is(err, errResetConfirmationMismatch) {
		httperr.Write(w, r, httperr.Validation("confirmation", "confirmation_mismatch",
			"The typed confirmation does not match the organization name."))
		return
	}
	if err != nil {
		// The cause (e.g. an unresolved FK cycle naming tables) never reaches
		// the client — httperr.Write maps an unmapped error to an opaque 500
		// and logs the cause server-side.
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, resetDataResponse{
		Status:        "reset",
		TablesCleared: summary.TablesCleared,
	})
}
