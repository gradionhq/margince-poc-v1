// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// syncStatePendingSync is overlay_mirror.sync_state's dirty value — an
// un-drained local write (branch 2) the ingest upsert's no-clobber-dirty
// guard protects and Reconcile (reconcile.go) reads to decide whether an
// incoming incumbent change is a genuine, emit-worthy divergence or a
// protected conflict it must never overwrite.
const syncStatePendingSync = "pending_sync"

// Row is one overlay_mirror record as read back — the cache-refresh
// counterpart of Record (incumbent.go), carrying the persisted sync
// bookkeeping (sync_state, last_synced_at) a Record from the wire never
// has.
type Row struct {
	ObjectClass       string
	ExternalID        string
	Fields            map[string]any
	UpdatedAtBaseline time.Time
	OwnerExternalID   string
	SyncState         string
	LastSyncedAt      time.Time
}

// MirrorStore owns the overlay_mirror / overlay_association tables: the
// ONE idempotent ingest every sync trigger (backfill + the poller
// sweep) calls, and the plain reads the overlay.Provider read verbs
// serve from (design.md §4.4).
//
// Ingest is deliberately NOT a storekit-shaped write (no Audit/Emit): it
// is a derived-cache refresh of data HubSpot already owns, not a
// system-of-record mutation. Auditing every poller-driven upsert would
// (a) retain incumbent PII in audit_log for no compliance purpose and
// (b) fight the composite (object_class, external_id) key against
// storekit.Audit's ids.UUID-keyed signature. Sync health is a metrics
// concern, not an audit-log concern; only genuine domain events
// (mirror.conflict, mirror.budget_degraded) go through storekit.Emit.
type MirrorStore struct {
	pool   *pgxpool.Pool
	emails OwnerEmailResolver
	// fenced opts this store into the disconnect-race fence
	// (disconnectfence.go): every mutation that could resurrect
	// incumbent-derived data first asserts an active incumbent_connection,
	// returning ErrConnectionGone if the workspace has been disconnected
	// mid-sweep. Only the background reconcile sweep sets it (WithFence);
	// the read-path and on-connect stores leave it false so a write outside
	// a live connection (a test, the seed) is not gated on one.
	fenced bool
}

// NewMirrorStore constructs a MirrorStore over pool. emails resolves an
// incumbent-side owner reference to its current email — the primitive
// UpsertUserMap's and Ingest's owner-email-change re-validation
// (visibility.go, design.md §4.6 rules 3/5) verify a mirror_user_map row
// against.
func NewMirrorStore(pool *pgxpool.Pool, emails OwnerEmailResolver) *MirrorStore {
	return &MirrorStore{pool: pool, emails: emails}
}

// WithResolver returns a MirrorStore identical to s but resolving owner
// emails through r. The sync lane (Connect seeding and the reconcile
// sweep) binds this to the connection's OWN live incumbent adapter so
// SeedUserMap, UpsertUserMap, and Ingest verify against the incumbent's
// CURRENT owner emails — the process-wide store is constructed with a
// placeholder only the read path (which never resolves an owner) is meant
// to reach, since a per-workspace credential lookup is not available at
// server-construction time.
func (s *MirrorStore) WithResolver(r OwnerEmailResolver) *MirrorStore {
	c := *s
	c.emails = r
	return &c
}

// WithFence returns a MirrorStore identical to s with the disconnect-race
// fence engaged (see the fenced field and disconnectfence.go). The reconcile
// sweep binds it — ms.WithResolver(inc).WithFence() — so every sync write it
// issues aborts with ErrConnectionGone the moment the workspace is
// disconnected, instead of resurrecting purged incumbent-derived data. It is
// opt-in precisely so the read path and the many unit tests that ingest
// without standing up a connection are not forced to hold one.
func (s *MirrorStore) WithFence() *MirrorStore {
	c := *s
	c.fenced = true
	return &c
}

// ingestSQL is the in-SQL cache-refresh upsert design.md §4.4/§4.9
// specifies verbatim. Three guards, all IN the statement so concurrent
// triggers (an on-demand reconcile racing the periodic poller sweep)
// serialize on Postgres's row lock instead of racing an app-level
// read-compare-write:
//
//   - tombstone-guard (WHERE NOT EXISTS …): an erased external_id is
//     never re-created — the sweep that would otherwise resurrect it
//     never gets the chance to see the row at all.
//   - staleness (ON CONFLICT … WHERE excluded.updated_at_baseline > …):
//     an older incumbent read can never clobber a newer one, so ingest
//     is safe to call with a stale poller page racing a fresher read of
//     the same record.
//   - no-clobber-dirty (… AND sync_state <> 'pending_sync'): a row with
//     an un-drained local write (branch 2) is not blindly overwritten by
//     an inbound incumbent change; it is held for the conflict path
//     (mirror.conflict, not implemented until branch 2's write path
//     lands — see the ProjectOwnerVisibility seam note in Ingest below).
const ingestSQL = `
INSERT INTO overlay_mirror (workspace_id, object_class, external_id, fields, updated_at_baseline, owner_external_id, last_synced_at, sync_state)
SELECT NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, $3, $4, $5, now(), 'fresh'
WHERE NOT EXISTS (SELECT 1 FROM overlay_tombstone t
    WHERE t.workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid AND t.object_class=$1 AND t.external_id=$2)
ON CONFLICT (workspace_id, object_class, external_id) DO UPDATE
   SET fields=EXCLUDED.fields, updated_at_baseline=EXCLUDED.updated_at_baseline,
       owner_external_id=EXCLUDED.owner_external_id, last_synced_at=now()
   WHERE overlay_mirror.sync_state <> 'pending_sync'
     AND EXCLUDED.updated_at_baseline > overlay_mirror.updated_at_baseline`

// Ingest upserts one incumbent record into the mirror — the single entry
// point every sync trigger calls (design.md §4.4: "push and pull
// converge on ONE ingest"). It runs
// inside database.WithWorkspaceTx so the tombstone/staleness/dirty guards
// above see the same tenant's rows the RLS policies would otherwise gate
// a plain query behind.
//
// ProjectOwnerVisibility runs INLINE in this same
// transaction (design.md §4.6: "computed INLINE in the ingest upsert tx …
// NOT a trailing pass" — a hide-window between landing the row and
// deciding who may see it is exactly the un-gated-read window ADR-0044
// forbids). It only runs when the upsert actually landed a row —
// RowsAffected()==0 means the tombstone guard, the staleness guard, or the
// no-clobber-dirty guard held the row back, and there is nothing new to
// project. Before the upsert, Ingest also reads the record's PRIOR owner:
// if the incoming owner differs from it, that reassignment is the only
// signal Ingest ever sees that an incumbent user's identity is newly
// relevant (Record carries an owner id, never an owner email), so it
// re-validates that owner's email-sourced mirror_user_map row before
// projecting (design.md §4.6 rule 5).
func (s *MirrorStore) Ingest(ctx context.Context, rec Record) error {
	if rec.ObjectClass == "" || rec.ExternalID == "" {
		return fmt.Errorf("overlay: ingest requires a non-empty object class and external id")
	}
	var ownerArg any
	if rec.OwnerExternalID != "" {
		ownerArg = rec.OwnerExternalID
	}
	var landed bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertActiveConnection(ctx, tx); err != nil {
				return err
			}
		}
		// Ingest's owner projection (ProjectOwnerVisibility, and the
		// owner-change revalidation) mutates mirror_visibility, so it takes
		// the same per-workspace visibility lock every other mutator takes —
		// serializing an owner reassignment against a concurrent manual remap
		// so a record transitioning between owners can never leave a stale
		// grant. Overlay ingest is driven by the single leader-elected
		// poller, so this lock is uncontended on the hot path.
		if err := lockWorkspaceVisibility(ctx, tx); err != nil {
			return err
		}
		var priorOwner string
		if err := tx.QueryRow(
			ctx,
			`SELECT coalesce(owner_external_id, '') FROM overlay_mirror WHERE object_class = $1 AND external_id = $2`,
			rec.ObjectClass, rec.ExternalID,
		).Scan(&priorOwner); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("overlay: reading the prior owner of %s/%s: %w", rec.ObjectClass, rec.ExternalID, err)
		}

		tag, err := tx.Exec(
			ctx, ingestSQL,
			rec.ObjectClass, rec.ExternalID, rec.Fields, rec.ModifiedAt, ownerArg,
		)
		if err != nil {
			return fmt.Errorf("overlay: ingesting %s/%s: %w", rec.ObjectClass, rec.ExternalID, err)
		}
		if tag.RowsAffected() == 0 {
			// Held back by a guard (tombstone/staleness/dirty) — the
			// mirror row did not change, so there is nothing to
			// re-project.
			return nil
		}
		landed = true

		if rec.OwnerExternalID != "" && rec.OwnerExternalID != priorOwner {
			if err := s.revalidateEmailMapping(ctx, tx, s.emails, rec.OwnerExternalID); err != nil {
				return err
			}
		}
		return ProjectOwnerVisibility(ctx, tx, rec.ObjectClass, rec.ExternalID, rec.OwnerExternalID)
	})
	if err == nil && landed {
		// Count only a COMMITTED landing toward the inbound sync-rate
		// metric (metrics.go) — a row this transaction upserted but then
		// rolled back (e.g. revalidateEmailMapping/ProjectOwnerVisibility
		// failed) is not a real sync event, and counting it would drift
		// the metric ahead of what overlay_mirror actually holds.
		mirrorSyncedTotal.Add(1)
	}
	return err
}

// upsertAssocSQL keeps the direction/label/category refresh together
// with the association's identity key (workspace_id, from_type, from_id,
// to_type, to_id, type_id) — associations v4 can relabel or recategorize
// an edge without changing its identity, so a re-sync must update those
// columns in place rather than duplicate the edge.
const upsertAssocSQL = `
INSERT INTO overlay_association (workspace_id, from_type, from_id, to_type, to_id, type_id, category, label, direction)
VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (workspace_id, from_type, from_id, to_type, to_id, type_id) DO UPDATE
   SET category = EXCLUDED.category, label = EXCLUDED.label, direction = EXCLUDED.direction`

// UpsertAssoc records one incumbent association edge. Direction is
// carried through verbatim — the hubspot adapter's Associations() always
// reports "forward" (the from→to direction it queried), never inferred
// or normalized here.
func (s *MirrorStore) UpsertAssoc(ctx context.Context, a Assoc) error {
	if a.FromType == "" || a.FromID == "" || a.ToType == "" || a.ToID == "" {
		return fmt.Errorf("overlay: upsert association requires non-empty from/to type and id")
	}
	var label any
	if a.Label != "" {
		label = a.Label
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertActiveConnection(ctx, tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(
			ctx, upsertAssocSQL,
			a.FromType, a.FromID, a.ToType, a.ToID, a.TypeID, a.Category, label, a.Direction,
		); err != nil {
			return fmt.Errorf("overlay: upserting association %s/%s -> %s/%s: %w",
				a.FromType, a.FromID, a.ToType, a.ToID, err)
		}
		return nil
	})
}

// selectRawMirrorRowSQL/getRaw — the package-internal, visibility-blind
// read: the integration tests use it to assert on ingest behavior
// directly (staleness, tombstone-guard, no-clobber-dirty) without
// depending on the mirror_visibility deny-join the exported Get/List
// apply, and Reconcile (reconcile.go) uses it for the same
// reason — a background sync pass has no acting human principal to
// visibility-join against, and deciding whether an incumbent change is a
// genuine divergence is a SYSTEM decision, not a per-user read. getRaw
// must never be promoted to an EXPORTED method or reachable from any
// HTTP/MCP surface: every visibility-blind read reachable by a caller
// acting on a human's behalf is exactly the bolt-on ADR-0044 forbids;
// its only callers are this package's own system-level sync logic and
// its tests.
const selectRawMirrorRowSQL = `
SELECT object_class, external_id, fields, updated_at_baseline,
       coalesce(owner_external_id, ''), sync_state, last_synced_at
FROM overlay_mirror
WHERE object_class = $1 AND external_id = $2`

func (s *MirrorStore) getRaw(ctx context.Context, objectClass, externalID string) (Row, error) {
	var row Row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, selectRawMirrorRowSQL, objectClass, externalID).Scan(
			&row.ObjectClass, &row.ExternalID, &row.Fields, &row.UpdatedAtBaseline,
			&row.OwnerExternalID, &row.SyncState, &row.LastSyncedAt,
		)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Row{}, apperrors.ErrNotFound
		}
		return Row{}, fmt.Errorf("overlay: reading mirror row %s/%s: %w", objectClass, externalID, err)
	}
	return row, nil
}

const selectVisibleMirrorRowSQL = `
SELECT m.object_class, m.external_id, m.fields, m.updated_at_baseline,
       coalesce(m.owner_external_id, ''), m.sync_state, m.last_synced_at
FROM overlay_mirror m
%s
WHERE m.object_class = $2 AND m.external_id = $3`

// Get reads one mirror row by (objectClass, externalID), gated by the
// mirror_visibility deny-join (design.md §4.6: "joined on every overlay
// read"; can_see=false or no entry ⇒ row not returned). An unmapped ctx
// principal (no mirror_user_map row at all) answers apperrors.ErrNotFound
// before the query ever runs — zero rows, existence-hiding, never a 403.
func (s *MirrorStore) Get(ctx context.Context, objectClass, externalID string) (Row, error) {
	var row Row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		mirrorUserID, err := resolveActingMirrorUserID(ctx, tx)
		if err != nil {
			return err
		}
		joinClause, args := visibilityJoin(mirrorUserID)
		args = append(args, objectClass, externalID)
		query := fmt.Sprintf(selectVisibleMirrorRowSQL, joinClause)
		return tx.QueryRow(ctx, query, args...).Scan(
			&row.ObjectClass, &row.ExternalID, &row.Fields, &row.UpdatedAtBaseline,
			&row.OwnerExternalID, &row.SyncState, &row.LastSyncedAt,
		)
	})
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return Row{}, apperrors.ErrNotFound
		}
		return Row{}, fmt.Errorf("overlay: reading mirror row %s/%s: %w", objectClass, externalID, err)
	}
	return row, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectVisibleMirrorPageSQL = `
SELECT m.object_class, m.external_id, m.fields, m.updated_at_baseline,
       coalesce(m.owner_external_id, ''), m.sync_state, m.last_synced_at
FROM overlay_mirror m
%s
WHERE m.object_class = $2 AND m.external_id > $3
ORDER BY m.external_id
LIMIT $4`

// List pages mirror rows for one object class in external_id order, a
// stable (if not incumbent-numeric) keyset — the cursor only has to be a
// consistent Margince-side ordering, not replicate HubSpot's own paging
// scheme. Gated by the same mirror_visibility deny-join as Get (design.md
// §4.6); an unmapped ctx principal answers apperrors.ErrNotFound before
// the page query ever runs.
func (s *MirrorStore) List(ctx context.Context, objectClass, cursor string, limit int) ([]Row, string, error) {
	after, err := decodeMirrorCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}

	var rows []Row
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		mirrorUserID, err := resolveActingMirrorUserID(ctx, tx)
		if err != nil {
			return err
		}
		joinClause, args := visibilityJoin(mirrorUserID)
		args = append(args, objectClass, after, limit)
		query := fmt.Sprintf(selectVisibleMirrorPageSQL, joinClause)
		pgRows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("overlay: listing mirror rows for %s: %w", objectClass, err)
		}
		defer pgRows.Close()
		for pgRows.Next() {
			var row Row
			if err := pgRows.Scan(
				&row.ObjectClass, &row.ExternalID, &row.Fields, &row.UpdatedAtBaseline,
				&row.OwnerExternalID, &row.SyncState, &row.LastSyncedAt,
			); err != nil {
				return fmt.Errorf("overlay: scanning mirror row for %s: %w", objectClass, err)
			}
			rows = append(rows, row)
		}
		return pgRows.Err()
	})
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(rows) == limit {
		next = encodeMirrorCursor(rows[len(rows)-1].ExternalID)
	}
	return rows, next, nil
}

// encodeMirrorCursor/decodeMirrorCursor keep the List cursor opaque to
// callers (a client must never construct or edit one by hand) while
// staying a plain external_id underneath — there is no sort/direction
// variance to encode, unlike storekit's general keyset cursor.
func encodeMirrorCursor(externalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(externalID))
}

func decodeMirrorCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("overlay: malformed list cursor: %w", err)
	}
	return string(raw), nil
}
