// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns Disconnect's teardown (design.md §4.9, OVA-AC-1): revoke
// the connection, purge the mirror replica, tombstone what it purges, and
// flip the workspace back to native mode — all in ONE transaction so a
// crash mid-teardown can never leave the workspace half in overlay mode
// with its mirror gone (or vice versa). The vault delete runs after
// commit: it has no transaction to join, and deleting the credential
// before the row that names it is durably revoked would risk stranding a
// "connected" row with no resolvable secret.
//
// Scoping note on design.md §4.9's "scrub/redact incumbent-derived
// content from retained augmentation": audit_log is immutable BY
// CONSTRUCTION (migrations/core/0012_audit_log.up.sql's
// trg_audit_no_mutate trigger RAISEs on every UPDATE/DELETE, for every
// role, no exception) — the P12 spine, not a policy this module may
// carve an exception into. privacy.Eraser (the Art. 17 engine) already
// establishes the pattern this follows: redaction targets MUTABLE
// domain rows (there, activity/lead columns via redactSubjectTimeline);
// its own erasure tombstone carries counts in evidence, never touches
// an existing row's before/after. Branch 1 is read-only — no
// activity/approval/agent-output row ever copies a mirrored field into
// its own domain columns — so there is currently no mutable row for
// this teardown to scrub. This is the honest state of branch 1, not a
// gap: when branch 2 (or a note-taking surface) lands a path that
// copies incumbent data into a domain row, THAT path's own erase/redact
// support is what closes this, the same way privacy's redaction covers
// activities today.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Disconnect revokes the workspace's active incumbent connection and
// tears down everything incumbent-derived: the mirror replica, its
// associations, the owner-identity map, the visibility projection
// over them, and the sync checkpoints (backfill cursor + reconcile
// watermark) — see the file comment above for the audit-scrub scoping.
// Gated by auth.Require("overlay_connection", ActionDelete): disconnect
// is as destructive as connect (it purges tenant data and flips
// sor_mode for every seat), so it is admin/ops-only, the same as
// Connect. The connection lifecycle audit trail
// (entity_type=incumbent_connection) survives untouched — disconnecting
// is itself a governed action, not an erasure of its own record.
// apperrors.ErrNotFound answers a workspace with no active connection
// (never connected, or already disconnected).
func (s *Service) Disconnect(ctx context.Context) error {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionDelete); err != nil {
		return err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("overlay: disconnect called outside a workspace context")
	}

	var ref string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// A flip RUN in flight is the one thing disconnect must not race:
		// tearing the mirror down mid-import would migrate a vanishing
		// estate. A merely SEALED snapshot is NOT enough to refuse on —
		// disconnect is the only path that revokes the incumbent's
		// credential and purges the mirrored PII, so it stays an escape
		// hatch from a frozen-but-idle workspace rather than becoming a
		// latch that strands one. The predicate is injected (compose owns
		// the migration-module edge) and defaults to "no run in flight"
		// for a Service built without it. Checked in the same transaction
		// as the revoke, so a concurrent flip and disconnect serialize on
		// the connection row.
		if s.flipImportRunning != nil {
			running, err := s.flipImportRunning(ctx, tx)
			if err != nil {
				return fmt.Errorf("overlay: checking for an in-progress flip before disconnect: %w", err)
			}
			if running {
				return fmt.Errorf("overlay: a flip is in progress for this workspace; let it finish (or fail) before disconnecting: %w", apperrors.ErrConflict)
			}
		}
		connRef, err := revokeConnection(ctx, tx)
		if err != nil {
			return err
		}
		ref = connRef
		if err := purgeMirror(ctx, tx); err != nil {
			return err
		}
		if _, err := RevertToNative(ctx, tx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notifyModeFlip(ws)

	// The disconnect is already committed and authoritative here (connection
	// revoked, mirror purged, workspace flipped to native) — deleting the sealed
	// credential is best-effort cleanup AFTER that commit, with the caller's
	// cancellation detached and a failure logged rather than returned. See
	// deleteUnreferencedRef (connection.go) for why both of those are required,
	// and why reconnect's superseded blob shares the same path. A durable
	// outbox-driven retry keyed off the incumbent.disconnected event emitted
	// above would remove even the manual cleanup step.
	s.deleteUnreferencedRef(ctx, ws, keyvault.Ref(ref), "disconnect")
	return nil
}

// RevertToNative returns the bound workspace to native mode inside the
// caller's transaction, reporting whether it had to. It is the ONE spelling of
// that flip: Disconnect's teardown takes it, and so does the non-production
// data reset, which sweeps every table overlay mode depends on and would
// otherwise leave the workspace claiming to read from an incumbent it no
// longer has a connection to.
//
// Both columns move in one statement because the schema admits no intermediate
// state (the x_overlay_iff_incumbent CHECK: a mode of 'overlay' and a non-NULL
// x_incumbent are the same fact). It is exported because these are overlay's
// own fork-owned columns on a table identity owns — the write belongs to this
// module wherever it is called from, which is what
// TestEveryPackageOnlyWritesTablesItOwns holds.
//
// Idempotent by predicate: a workspace already native reports false and is not
// written, so a caller need not ask the mode first.
func RevertToNative(ctx context.Context, tx pgx.Tx) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE workspace SET x_sor_mode = 'native', x_incumbent = NULL
		WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		  AND x_sor_mode <> 'native'`)
	if err != nil {
		return false, fmt.Errorf("overlay: flipping the workspace back to native mode: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// incumbentDisconnectedPayload builds the incumbent.disconnected wire
// payload. Unlike the mirror.* events, this event's subject is always
// the incumbent_connection row itself — a fixed type — so it is emitted
// via the plain storekit.EmitEvent.
func incumbentDisconnectedPayload(incumbent, region, status string) crmcontracts.PublicEventIncumbentDisconnected {
	return crmcontracts.PublicEventIncumbentDisconnected{
		Incumbent: incumbent,
		Region:    region,
		Status:    status,
	}
}

// revokeConnection selects the workspace's active incumbent_connection
// row FOR UPDATE, flips it to revoked, and writes the write-shape
// Audit+Emit pair — the first half of Disconnect's transaction.
// apperrors.ErrNotFound means no active connection exists.
func revokeConnection(ctx context.Context, tx pgx.Tx) (credentialRef string, err error) {
	var connID ids.UUID
	var incumbent, region string
	if scanErr := tx.QueryRow(
		ctx, `
		SELECT id, incumbent, region, credential_ref
		FROM incumbent_connection
		WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		  AND status = 'active'
		FOR UPDATE`,
	).Scan(&connID, &incumbent, &region, &credentialRef); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return "", apperrors.ErrNotFound
		}
		return "", scanErr
	}

	if _, err := tx.Exec(ctx,
		`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now() WHERE id = $1`,
		connID); err != nil {
		return "", fmt.Errorf("overlay: revoking the incumbent connection: %w", err)
	}

	before := map[string]any{auditFieldIncumbent: incumbent, auditFieldRegion: region, auditFieldStatus: statusActive}
	after := map[string]any{auditFieldIncumbent: incumbent, auditFieldRegion: region, auditFieldStatus: statusRevoked}
	auditID, auditErr := storekit.Audit(ctx, tx, "archive", "incumbent_connection", connID, before, after)
	if auditErr != nil {
		return "", fmt.Errorf("overlay: auditing the disconnect: %w", auditErr)
	}
	if emitErr := storekit.EmitEvent(ctx, tx, auditID, connID,
		incumbentDisconnectedPayload(incumbent, region, statusRevoked)); emitErr != nil {
		return "", fmt.Errorf("overlay: emitting incumbent.disconnected: %w", emitErr)
	}
	return credentialRef, nil
}

// purgeMirror tombstones then deletes every incumbent-derived tenant
// table (design.md §4.9's teardown purge list): the mirror replica, its
// association edges, the visibility projection over them, and
// mirror_user_map — its incumbent_user_id column is incumbent-derived
// (the HubSpot owner id), so it is exactly the "no incumbent-derived
// data remains queryable" surface OVA-AC-1 names, even though it holds
// no mirror row itself. The sync checkpoints (overlay_backfill_cursor +
// overlay_reconcile_watermark + overlay_sync_state's sweep backoff) purge for the same reason plus a
// behavioral one: each is a position into the incumbent's own record
// stream, so a checkpoint that survived disconnect would make a later
// connection resume mid-stream — a retained done backfill cursor
// short-circuits Backfill (backfill.go) into skipping the initial
// mirror load outright, and a stale watermark resumes the incremental
// sweep past everything it never saw. A disconnected workspace's sync
// state must read exactly as a never-connected one's does ("", not
// started, epoch). The OVB budget window is NOT purged here: it lives in
// Redis now (overlay-budget chapter), not a workspace-scoped Postgres
// table, and its fixed-window counters expire on their own TTL — there is
// no PG row for this teardown to touch. No
// embeddings/context-graph/FTS tables exist
// yet in this build (the search module's retrieval store is a later
// work package) — nothing here to purge on their behalf until that
// lands.
//
// The tombstones written below deliberately OUTLIVE the connection:
// they are what keeps a stray in-flight sweep from resurrecting a
// purged row after this transaction lands. Clearing them belongs to
// the reconnect flow — establishing a NEW connection is the fresh
// trust decision that may mirror those records again, so
// Connect.reconnectConnection (connection.go) clears the workspace's
// overlay_tombstone rows as part of reviving a revoked connection.
// Connect's pre-flight (existingConnectionStatus) still refuses a
// second connect while the existing row is active, but a revoked row
// is exactly what that reconnect flow revives in place.
func purgeMirror(ctx context.Context, tx pgx.Tx) error {
	// Tombstone every row the mirror currently holds BEFORE purging it —
	// the same in-SQL discipline as the ingest upsert (mirrorstore.go):
	// the tombstone must exist before the row it would otherwise let a
	// stray in-flight sweep resurrect is gone, never after.
	if _, err := tx.Exec(ctx, `
		INSERT INTO overlay_tombstone (workspace_id, object_class, external_id)
		SELECT workspace_id, object_class, external_id FROM overlay_mirror
		ON CONFLICT (workspace_id, object_class, external_id) DO NOTHING`); err != nil {
		return fmt.Errorf("overlay: tombstoning the mirror before purge: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_mirror`); err != nil {
		return fmt.Errorf("overlay: purging the mirror: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_association`); err != nil {
		return fmt.Errorf("overlay: purging associations: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mirror_visibility`); err != nil {
		return fmt.Errorf("overlay: purging the visibility projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mirror_user_map`); err != nil {
		return fmt.Errorf("overlay: purging the owner-identity map: %w", err)
	}
	// The auto-map block is a decision about THIS connection's visibility, so
	// it dies with the connection: purgeMirror's invariant is that a
	// disconnected workspace reads exactly as a never-connected one, and a
	// surviving block would hide records after a reconnect (possibly to a
	// different portal of the same incumbent, since the block is keyed by
	// incumbent name) with nothing left to explain why.
	if _, err := tx.Exec(ctx, `DELETE FROM mirror_user_automap_block`); err != nil {
		return fmt.Errorf("overlay: purging the auto-map blocks: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_backfill_cursor`); err != nil {
		return fmt.Errorf("overlay: purging the backfill cursor checkpoints: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_reconcile_watermark`); err != nil {
		return fmt.Errorf("overlay: purging the reconcile watermarks: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_sync_state`); err != nil {
		return fmt.Errorf("overlay: purging the sweep backoff state: %w", err)
	}
	// The our-write ledger holds incumbent property values (OVA-DDL-6); purge it
	// with the rest so teardown leaves no mirrored incumbent data behind. Its
	// producer (OpenEntries) is disconnect-fenced, so an in-flight write cannot
	// repopulate it after this delete commits (the fence aborts that write).
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_write_ledger`); err != nil {
		return fmt.Errorf("overlay: purging the write ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_mirror_halt`); err != nil {
		return fmt.Errorf("overlay: purging the mirror-halt flag: %w", err)
	}
	return nil
}
