// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Repairing the identity map after a crashed flip.
//
// A record lands in two steps that are NOT one transaction: the owning
// module store creates the native row (its own write shape — domain row
// + audit + outbox), then the engine's identity map records what that
// external id became. A process that dies between them leaves a created
// record the resume cannot see, and the resume would create it again.
//
// The repair is possible because the flip stamps `source` inside the
// RESERVED import namespace, which every client-facing create wire
// refuses (shared/kernel/provenance). So a native row bearing the
// reserved prefix can only have been written by an importer — which is
// what makes reading provenance back safe here, when reading the
// client-writable part of it would not be. The prefix names the
// incumbent and the class, not a run: another attempt's orphan is
// precisely what this adopts.
//
// Adoption is restricted to LIVE rows. A row that was archived or
// merged away since the crash is a tombstone: binding an external id to
// it would suppress the real estate record — the import would report it
// converged, never create it, and every activity resolving through that
// identity would attach to the tombstone's survivor. Left unadopted, it
// simply gets created properly.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ReconcileIdentities adopts records this run created but never mapped,
// so a resumed flip recognizes its own work instead of duplicating it.
func (w *flipWriters) ReconcileIdentities(ctx context.Context) error {
	// The scan reads five module tables and binds identities off what it
	// finds, so the grant is checked before the first read rather than
	// at the write that follows it. Row scope is deliberately NOT
	// applied: an orphan owned by another seat is still this run's to
	// adopt, and skipping it would duplicate the record instead.
	if err := auth.Require(ctx, "import_run", principal.ActionCreate); err != nil {
		return err
	}
	for _, object := range flipImportOrder {
		pairs, err := w.orphanedIdentities(ctx, object)
		if err != nil {
			return err
		}
		if err := w.identities.RecordIdentities(ctx, w.runID, w.incumbent, object, pairs); err != nil {
			return err
		}
		for _, p := range pairs {
			w.nativeIDs[w.cacheKey(object, p.ExternalID)] = p.NativeID
		}
	}
	return nil
}

// orphanedIdentities finds live native rows for one class whose
// reserved-namespace provenance names this incumbent and that the
// identity map does not yet know about. The prefix is scoped to
// incumbent and class, not to a run: another attempt's orphan is
// exactly what this is meant to adopt.
func (w *flipWriters) orphanedIdentities(ctx context.Context, object string) ([]migration.IdentityPair, error) {
	if !flipImportable(object) {
		// The allowlist is what keeps the class out of the format string
		// below; an unknown one is a programming error, not a query.
		return nil, fmt.Errorf("flip reconcile: %q is not an importable object", object)
	}
	prefix := w.provenance(object, "")
	byExternal := map[string]ids.UUID{}
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		// starts_with, not LIKE: the prefix carries the incumbent's name,
		// and a `_` or `%` in it would silently widen the match to other
		// incumbents while the length-based strip below took the wrong
		// number of characters. The external id is extracted ONCE, in
		// SQL, and the join reuses it — two spellings of the same strip
		// agreed only while the match was a true prefix.
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT n.id, right(n.source, -length($1::text)) AS external_id
			FROM %s n
			LEFT JOIN import_record_map m
			  ON m.workspace_id = n.workspace_id
			 AND m.source_system = $2
			 AND m.object = $3
			 AND m.external_id = right(n.source, -length($1::text))
			WHERE starts_with(n.source, $1) AND m.native_id IS NULL
			  AND %s`, object, liveClause(object)),
			prefix, w.incumbent, object)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var id ids.UUID
			var ext string
			if err := rows.Scan(&id, &ext); err != nil {
				return err
			}
			// A row whose provenance is the bare prefix names no external
			// record; it cannot be bound to one, so it is left alone
			// rather than mapped under an empty key.
			if ext == "" {
				continue
			}
			// Two live rows claiming one external id is ambiguous, and the
			// insert's conflict rule would pick whichever landed first
			// while the caller's cache kept the last. Adopt neither: the
			// duplicate is visible, and a wrong binding would not be.
			if seen[ext] {
				delete(byExternal, ext)
				continue
			}
			seen[ext] = true
			byExternal[ext] = id
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("flip reconcile: finding unmapped %s records from a previous attempt: %w", object, err)
	}
	pairs := make([]migration.IdentityPair, 0, len(byExternal))
	for ext, id := range byExternal {
		pairs = append(pairs, migration.IdentityPair{ExternalID: ext, NativeID: id})
	}
	return pairs, nil
}

// liveClause is the per-class liveness predicate the adoption scan adds.
// Only person and organization carry a merge pointer; every scanned
// class carries archived_at. A tombstone must never be adopted.
func liveClause(object string) string {
	if object == flipObjectPerson || object == flipObjectOrganization {
		return "n.archived_at IS NULL AND n.merged_into_id IS NULL"
	}
	return "n.archived_at IS NULL"
}
