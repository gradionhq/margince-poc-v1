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
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
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

// resetDataResponse is the 200 body. The contract declares the shape inline
// (no generated type), so it is spelled here — and
// TestResetDataResponseMatchesTheContract (backend/resetwireshape_test.go)
// derives the two field sets from the contract and this struct so they cannot
// drift.
type resetDataResponse struct {
	Status         string `json:"status"`
	TablesCleared  int    `json:"tables_cleared"`
	JobsCancelled  int    `json:"jobs_cancelled"`
	StreamsPurged  int    `json:"streams_purged"`
	CacheKeys      int    `json:"cache_keys_deleted"`
	ObjectsDeleted int    `json:"objects_deleted"`
	DrainTimedOut  bool   `json:"drain_timed_out"`
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

	// runtime POINTS AT the Server's own field rather than copying it, so
	// WithResetRuntime and WithDataReset may be applied in either order — see
	// Server.resetRuntime for what a copy would silently cost. nil is the
	// Postgres-only reset a role that wired no runtime performs.
	runtime *ResetRuntime
	// budget is the overlay budget meter whose per-workspace Redis counters
	// must not survive the install they were spent by.
	budget *overlaybudget.Meter
	// blob is the object store holding the bytes the swept rows referenced.
	blob blobstore.Store
	// flush drops this process's own caches (Server.FlushResetCaches); the
	// bus announcement reaches the rest of the fleet.
	flush func(ids.UUID)
}

// run performs the full reset for the bound workspace: refuse a mismatched
// confirmation, then hand the rest to runQuiesced, which does the work with the
// job fleet held down.
func (h dataResetHandlers) run(ctx context.Context, confirmation string) (resetCounts, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return resetCounts{}, database.ErrNoWorkspace
	}
	// Read BEFORE anything is paused or purged: a typo must cost the
	// installation nothing, and quiescing the job fleet is not nothing. The
	// sweep re-checks inside its own transaction — one row read that closes
	// the window where the organization is renamed in between.
	if err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		return confirmResetOrgName(ctx, tx, wsID, confirmation)
	}); err != nil {
		return resetCounts{}, err
	}
	return h.runQuiesced(ctx, wsID, func(counts *resetCounts) error {
		return h.sweepAndReseed(ctx, wsID, confirmation, counts)
	})
}

// runQuiesced performs the reset with the job fleet held down: purge the queue,
// the bus and the budget counters, sweep + re-seed Postgres in one transaction,
// clear the surfaces no transaction can reach, and announce the reset so every
// process drops its caches. sweep is the Postgres half, taken as a parameter so
// this ordering can be exercised without a database.
//
// The fleet pause's lifetime is exactly this function's, and that is why the
// resume is deferred HERE rather than inside the phase that takes the pause.
// Two properties follow, both of which a resume registered further in loses:
// a Quiesce that fails with the pause ALREADY applied — it pauses first, then
// polls the drain — is still lifted; and the fleet stays down until the last
// post-commit purge is done, so no job resumes into a window where it reads
// caches for data being deleted or writes objects the prefix sweep then removes.
func (h dataResetHandlers) runQuiesced(ctx context.Context, wsID ids.UUID, sweep func(*resetCounts) error) (resetCounts, error) {
	logger := h.log
	if logger == nil {
		logger = slog.Default()
	}
	rt := ResetRuntime{}
	if h.runtime != nil {
		rt = *h.runtime
	}
	defer resumeResetQueues(ctx, logger, rt)

	counts, err := h.runRuntimePhase(ctx, rt, wsID, sweep)
	if err != nil {
		return counts, err
	}
	if err := h.purgeUnjoinableSurfaces(ctx, logger, wsID, &counts); err != nil {
		return counts, err
	}
	// Caches go last, once every surface really is clear: anything that dropped
	// its cached answers earlier could have re-cached what was still being
	// purged. This process first, then the rest of the fleet over the bus.
	if h.flush != nil {
		h.flush(wsID)
	}
	if rt.SignalReset != nil {
		// A failed announcement fails the request, and that is the chosen
		// posture rather than an oversight: the sweep is committed, so every
		// OTHER process is still serving cached answers for data that no longer
		// exists, and an operator has to know the installation is in that state.
		// The deferred resume above answers the mirror-image question the other
		// way for the mirror-image reason — that pause is this process's own
		// doing, and lifting it is not part of the outcome the caller asked for.
		if err := rt.SignalReset(ctx, wsID); err != nil {
			return counts, err
		}
	}
	logger.Info("data reset complete", "workspace_id", wsID,
		"tables_cleared", counts.TablesCleared, "jobs_cancelled", counts.JobsCancelled,
		"streams_purged", counts.StreamsPurged, "cache_keys_deleted", counts.CacheKeys,
		"objects_deleted", counts.ObjectsDeleted, "drain_timed_out", counts.DrainTimedOut)
	return counts, nil
}

// confirmResetOrgName refuses the reset unless confirmation is exactly the
// organization's name.
func confirmResetOrgName(ctx context.Context, tx pgx.Tx, wsID ids.UUID, confirmation string) error {
	var orgName string
	if err := tx.QueryRow(ctx, `SELECT name FROM workspace WHERE id = $1`, wsID).Scan(&orgName); err != nil {
		return err
	}
	if confirmation != orgName {
		return errResetConfirmationMismatch
	}
	return nil
}

// sweepAndReseed is the Postgres half, in ONE transaction: sweep domain +
// config data, clear the outbox, re-seed module defaults (as bootstrap does),
// and record the reset in audit_log.
func (h dataResetHandlers) sweepAndReseed(ctx context.Context, wsID ids.UUID, confirmation string, counts *resetCounts) error {
	return database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		if err := confirmResetOrgName(ctx, tx, wsID, confirmation); err != nil {
			return err
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
		counts.TablesCleared = len(tables)

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
			resetEvidence(*counts))
		return err
	})
}

// resetEvidence is the audit row's evidence map.
//
// objects_deleted is deliberately absent: a blob store cannot join a Postgres
// transaction, so the bytes are purged after this row is already committed and
// the tally does not exist yet. It reaches the response and the completion log
// line instead.
//
// cache_keys_deleted, by contrast, is COMPLETE here, and must stay that way:
// every Redis purge that feeds it runs before this transaction opens
// (runRuntimePhase), so the number the permanent record carries is the same
// number the response and the log line report. A purge moved after the commit
// would silently make one key name mean two different totals.
func resetEvidence(counts resetCounts) map[string]any {
	return map[string]any{
		"tables_cleared":     counts.TablesCleared,
		"jobs_cancelled":     counts.JobsCancelled,
		"streams_purged":     counts.StreamsPurged,
		"cache_keys_deleted": counts.CacheKeys,
		"drain_timed_out":    counts.DrainTimedOut,
	}
}

// purgeUnjoinableSurfaces clears what the sweep's transaction cannot reach and
// so runs after it commits: the schema's cf_* columns and the stored object
// bytes whose only references the sweep just deleted. The object purge fails the
// request when it fails — an install reported as reset while a surface still
// holds the old one's state is the outcome this whole path exists to prevent.
// The cf_* drop is the one exception, for the reason its own comment gives.
//
// The Redis surfaces are NOT here: they are purged before the sweep's
// transaction so the audit row it writes can name what they cleared
// (runRuntimePhase).
func (h dataResetHandlers) purgeUnjoinableSurfaces(ctx context.Context, logger *slog.Logger, wsID ids.UUID, counts *resetCounts) error {
	// Drop custom-field columns so the schema matches a fresh bootstrap.
	// Best-effort — the definitions are already gone with the sweep, and
	// leaving an empty column behind is harmless if this can't run (no schema
	// pool configured); logged, not swallowed.
	if h.schemaPool != nil {
		if err := dropResetCustomFieldColumns(ctx, h.schemaPool); err != nil {
			logger.Error("data reset: cf_ column drop failed", "err", err)
		}
	}
	if h.blob != nil {
		// The prefix must end at the key separator or the store refuses it:
		// "<ws>" alone would reach into a sibling tenant whose id extends it.
		n, err := h.blob.DeletePrefix(ctx, wsID.String()+"/")
		if err != nil {
			return err
		}
		counts.ObjectsDeleted = n
	}
	return nil
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
	counts, err := h.run(ctx, req.Confirmation)
	if errors.Is(err, errResetConfirmationMismatch) {
		httperr.Write(w, r, httperr.Validation("confirmation", "confirmation_mismatch",
			"The typed confirmation does not match the organization name."))
		return
	}
	if err != nil {
		// The cause (e.g. an unresolved FK cycle naming tables, or a purge that
		// failed) never reaches the client — httperr.Write maps an unmapped
		// error to an opaque 500 and logs the cause server-side.
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, resetDataResponse{
		Status:         "reset",
		TablesCleared:  counts.TablesCleared,
		JobsCancelled:  counts.JobsCancelled,
		StreamsPurged:  counts.StreamsPurged,
		CacheKeys:      counts.CacheKeys,
		ObjectsDeleted: counts.ObjectsDeleted,
		DrainTimedOut:  counts.DrainTimedOut,
	})
}
