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
		connRef, err := revokeConnection(ctx, tx)
		if err != nil {
			return err
		}
		ref = connRef
		if err := purgeMirror(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE workspace SET x_sor_mode = 'native', x_incumbent = NULL
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`); err != nil {
			return fmt.Errorf("overlay: flipping the workspace back to native mode: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notifyModeFlip(ws)

	// The disconnect is already committed and authoritative here (connection
	// revoked, mirror purged, workspace flipped to native) — deleting the
	// sealed credential is best-effort cleanup AFTER that commit, not part of
	// it. Failing Disconnect on a vault error would be doubly wrong: it
	// misreports a disconnect that DID happen, and it strands the caller — a
	// retry finds no active connection (revokeConnection → ErrNotFound) and
	// could never re-attempt this delete. So on failure, log the orphaned
	// credential ref at ERROR for operational cleanup and return success. The
	// blob is inert: a revoked, unreferenced, encrypted-at-rest secret, not an
	// active exposure. The ref is a vault key, never the secret (safe to log).
	// A durable outbox-driven retry keyed off the incumbent.disconnected event
	// emitted above would remove even the manual step.
	if err := s.vault.Delete(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(ref)); err != nil {
		s.log.ErrorContext(ctx, "overlay: disconnect committed, but deleting the sealed incumbent credential failed — the orphaned (revoked, inert) blob needs cleanup",
			"workspace", ws.String(), "credential_ref", ref, "err", err)
	}
	return nil
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
// trust decision that may mirror those records again, so that flow
// clears the workspace's tombstones as part of connecting. No such
// flow exists in branch 1: Connect refuses a workspace holding any
// connection row, revoked included (hasConnection).
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
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_backfill_cursor`); err != nil {
		return fmt.Errorf("overlay: purging the backfill cursor checkpoints: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_reconcile_watermark`); err != nil {
		return fmt.Errorf("overlay: purging the reconcile watermarks: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM overlay_sync_state`); err != nil {
		return fmt.Errorf("overlay: purging the sweep backoff state: %w", err)
	}
	return nil
}
