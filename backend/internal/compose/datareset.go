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
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
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

// resetDataResponse is the 200 body. The contract declares the shape inline
// (no generated type), so it is spelled here — and
// TestResetDataResponseMatchesTheContract (backend/resetwireshape_test.go)
// derives the two field sets from the contract and this struct so they cannot
// drift.
type resetDataResponse struct {
	Status         string `json:"status"`
	TablesCleared  int    `json:"tables_cleared"`
	JobsDeleted    int    `json:"jobs_deleted"`
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
	// vault holds the sealed credentials the swept connection rows referenced.
	// Its storage carries no workspace_id, so the sweep cannot reach it and the
	// ciphertext would otherwise outlive the installation it belonged to.
	vault keyvault.Vault
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
	return h.runQuiesced(ctx, wsID,
		func() error { return h.clearOutbox(ctx) },
		func(counts *resetCounts) error {
			return h.sweepAndReseed(ctx, wsID, confirmation, counts)
		})
}

// runQuiesced performs the reset with the job fleet held down: drain the outbox,
// purge the queue, the bus and the budget counters, sweep + re-seed Postgres in
// one transaction, clear the surfaces no transaction can reach, and announce the
// reset so every process drops its caches. clearOutbox and sweep are the two
// Postgres halves the runtime ordering separates, taken as parameters so this
// ordering can be exercised without a database.
//
// The fleet pause's lifetime is exactly this function's, and that is why the
// resume is deferred HERE rather than inside the phase that takes the pause.
// Two properties follow, both of which a resume registered further in loses:
// a Quiesce that fails with the pause ALREADY applied — it pauses first, then
// polls the drain — is still lifted; and the fleet stays down until the last
// post-commit purge is done, so no job resumes into a window where it reads
// caches for data being deleted or writes objects the prefix sweep then removes.
func (h dataResetHandlers) runQuiesced(ctx context.Context, wsID ids.UUID, clearOutbox func() error, sweep func(*resetCounts) error) (resetCounts, error) {
	logger := h.log
	if logger == nil {
		logger = slog.Default()
	}
	rt := ResetRuntime{}
	if h.runtime != nil {
		rt = *h.runtime
	}
	defer resumeResetQueues(ctx, logger, rt)

	counts, err := h.runRuntimePhase(ctx, rt, wsID, clearOutbox, sweep)
	if err != nil {
		return counts, err
	}

	// Everything from here runs detached from the request, under its own bound.
	// The sweep is COMMITTED at this point, so these purges are no longer
	// optional work the caller may abandon: a client that times out or
	// disconnects now would otherwise cancel the object and credential purges
	// and the announcement, leaving bytes, sealed secrets and stale caches
	// behind for a reset the database already recorded as done. The deferred
	// resume detaches for the same reason.
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), resetFinishTimeout)
	defer cancelFinish()

	if err := h.purgeUnjoinableSurfaces(finishCtx, logger, wsID, &counts); err != nil {
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
		if err := rt.SignalReset(finishCtx, wsID); err != nil {
			return counts, err
		}
	}
	logger.Info("data reset complete", "workspace_id", wsID,
		"tables_cleared", counts.TablesCleared, "jobs_deleted", counts.JobsDeleted,
		"streams_purged", counts.StreamsPurged, "cache_keys_deleted", counts.CacheKeys,
		"objects_deleted", counts.ObjectsDeleted, "drain_timed_out", counts.DrainTimedOut,
		"sor_mode_reverted", counts.SorModeReverted, "secrets_purged", counts.SecretsPurged)
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

// sweepAndReseed is the Postgres sweep, in ONE transaction: sweep domain +
// config data, re-seed module defaults (as bootstrap does), and record the
// reset in audit_log. The outbox is not this transaction's to clear: draining it
// is the runtime phase's first act, ahead of the stream purge, so the relay has
// nothing staged to ship into streams that were just emptied (clearOutbox).
func (h dataResetHandlers) sweepAndReseed(ctx context.Context, wsID ids.UUID, confirmation string, counts *resetCounts) error {
	return database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		if err := confirmResetOrgName(ctx, tx, wsID, confirmation); err != nil {
			return err
		}
		tables, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		// Before the sweep, while the rows naming them still exist: the
		// ciphertext lives in a table with no workspace_id, so these handles are
		// the only thing that will still connect it to this tenant afterwards.
		// They are redeemed after the commit (purgeUnjoinableSurfaces).
		secretRefs, err := collectWorkspaceSecretRefs(ctx, tx)
		if err != nil {
			return err
		}
		counts.secretRefs = secretRefs

		if err := sweepWorkspaceData(ctx, tx, tables); err != nil {
			return err
		}
		counts.TablesCleared = len(tables)

		// A first-boot installation is native, and everything overlay mode
		// depends on was just swept: the incumbent connection, the mirror, the
		// budget counters. Left in overlay mode the workspace would claim to
		// read from an incumbent it has no connection to, dispatching every
		// read at an empty mirror — an installation that looks like it works.
		//
		// overlay's own function, not a local UPDATE: these are its fork-owned
		// columns, and Disconnect flips them the same way. This is NOT that
		// teardown, though — the connection and mirror rows are already gone
		// with the sweep, the reset carries its own audit row, and
		// incumbent.disconnected would be staged into an outbox this reset just
		// drained.
		reverted, err := overlay.RevertToNative(ctx, tx)
		if err != nil {
			return err
		}
		counts.SorModeReverted = reverted

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
		"jobs_deleted":       counts.JobsDeleted,
		"streams_purged":     counts.StreamsPurged,
		"cache_keys_deleted": counts.CacheKeys,
		"drain_timed_out":    counts.DrainTimedOut,
		// Whether this reset also took the installation out of overlay mode.
		// It belongs in the permanent record because it changes where every
		// subsequent read is served from, which no other count here does.
		"sor_mode_reverted": counts.SorModeReverted,
		// Sealed credentials redeemed from the vault. Like objects_deleted this
		// is tallied after the commit, so the number this row carries is the
		// count the sweep COLLECTED — the work the reset committed itself to —
		// while the response reports what was actually redeemed. They differ
		// only when the purge failed, and then the request failed with them.
		"secrets_purged": len(counts.secretRefs),
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
	return h.purgeSealedCredentials(ctx, wsID, counts)
}

// purgeSealedCredentials redeems the credential handles the sweep collected
// before it deleted the rows naming them.
//
// It runs after the commit because the vault is a seam, not a table: the local
// provider happens to write Postgres, but a remote one has no transaction to
// join. The handles were captured inside the transaction instead, which is the
// half that has to be consistent — a sweep that rolled back leaves refs this
// never receives.
//
// A failure fails the request. The alternative is reporting an installation as
// reset while its sealed credentials are still resident, which is precisely
// the state this exists to prevent. Delete is idempotent, so re-running the
// reset finishes a partial purge.
func (h dataResetHandlers) purgeSealedCredentials(ctx context.Context, wsID ids.UUID, counts *resetCounts) error {
	if h.vault == nil {
		return nil
	}
	ws := ids.From[ids.WorkspaceKind](wsID)
	for _, ref := range counts.secretRefs {
		// The ref is never logged or returned: it is the address of a secret,
		// and an error naming it would put that address in every log sink.
		if err := h.vault.Delete(ctx, ws, keyvault.Ref(ref)); err != nil {
			return fmt.Errorf("data reset: purging a sealed credential: %w", err)
		}
		counts.SecretsPurged++
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
		JobsDeleted:    counts.JobsDeleted,
		StreamsPurged:  counts.StreamsPurged,
		CacheKeys:      counts.CacheKeys,
		ObjectsDeleted: counts.ObjectsDeleted,
		DrainTimedOut:  counts.DrainTimedOut,
	})
}
