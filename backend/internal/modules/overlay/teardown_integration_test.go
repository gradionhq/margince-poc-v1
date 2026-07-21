// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Teardown is security-relevant (PII purge/scrub on disconnect) and gets
// its OWN real-Postgres coverage rather than riding piggyback on a later
// task's end-to-end test — a silently-skipped security gate looks
// exactly like a passing one.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestDisconnectPurgesTheMirrorTombstonesAndRetainsTheConnectionAudit(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store)

	const token = "pat-teardown-secret"
	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: token}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Seed a mirror row + association edge + an owner mapping directly
	// through the store — the same real path the sync engine would use,
	// and the same fixture shape as
	// provider_integration_test.go's TestProviderReadServesFromTheMirror:
	// UpsertUserMap BEFORE Ingest (with a matching OwnerExternalID) is
	// what actually lands a mirror_visibility row — Ingest's
	// null-owner rule (visibility.go's ProjectOwnerVisibility) writes
	// NO visibility row at all for an unowned record, which would make
	// a post-teardown "visibility count == 0" assertion vacuously true
	// even without Disconnect ever running.
	const objectClass = "person"
	const externalID = "5551234"
	const incumbentOwnerID = "owner-1"
	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("testWorkspaceCtx did not bind an actor")
	}
	if err := store.UpsertUserMap(ctx, ids.From[ids.UserKind](actor.UserID), "hubspot", incumbentOwnerID, "manual"); err != nil {
		t.Fatalf("seeding the owner-identity map fixture: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass:     objectClass,
		ExternalID:      externalID,
		Fields:          map[string]any{"firstname": "Ada", "email": "ada@incumbent.example"},
		ModifiedAt:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: incumbentOwnerID,
	}); err != nil {
		t.Fatalf("seeding the mirror fixture: %v", err)
	}
	if err := store.UpsertAssoc(ctx, Assoc{
		FromType: "person", FromID: externalID, ToType: "deal", ToID: "999",
		TypeID: 1, Category: "HUBSPOT_DEFINED", Direction: "forward",
	}); err != nil {
		t.Fatalf("seeding the association fixture: %v", err)
	}
	// Sync checkpoints, exactly as a connected workspace accrues them: a
	// converged backfill cursor and an advanced reconcile watermark — the
	// state whose survival past disconnect would make a later connection
	// skip its initial mirror load / resume the sweep mid-stream.
	const incumbentClass = "contacts"
	if err := store.SaveBackfillCursor(ctx, incumbentClass, "", true); err != nil {
		t.Fatalf("seeding the converged backfill-cursor fixture: %v", err)
	}
	if err := store.SaveReconcileWatermark(ctx, incumbentClass, time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seeding the reconcile-watermark fixture: %v", err)
	}

	// Confirm the fixture actually landed rows in every table the
	// post-teardown assertion checks — otherwise a bug that makes
	// Ingest/UpsertUserMap/SaveBackfillCursor/SaveReconcileWatermark
	// silently no-op would make that assertion vacuously pass, exactly
	// the gap being closed here.
	var seededVisibility, seededUserMap, seededCursor, seededWatermark int
	queryRowWS(ctx, t, pool, `SELECT count(*) FROM mirror_visibility WHERE workspace_id = $1`, []any{ws}, &seededVisibility)
	queryRowWS(ctx, t, pool, `SELECT count(*) FROM mirror_user_map WHERE workspace_id = $1`, []any{ws}, &seededUserMap)
	queryRowWS(ctx, t, pool, `SELECT count(*) FROM overlay_backfill_cursor WHERE workspace_id = $1`, []any{ws}, &seededCursor)
	queryRowWS(ctx, t, pool, `SELECT count(*) FROM overlay_reconcile_watermark WHERE workspace_id = $1`, []any{ws}, &seededWatermark)
	if seededVisibility == 0 || seededUserMap == 0 || seededCursor == 0 || seededWatermark == 0 {
		t.Fatalf("fixture is broken: seeded mirror_visibility=%d mirror_user_map=%d overlay_backfill_cursor=%d overlay_reconcile_watermark=%d, want all > 0",
			seededVisibility, seededUserMap, seededCursor, seededWatermark)
	}

	// A second audit_log row, unrelated to overlay entirely (a plain
	// person create), proves teardown does not reach for audit_log at
	// all — it is immutable by construction
	// (migrations/core/0012_audit_log.up.sql's trg_audit_no_mutate), so
	// this row's survival untouched is the negative-space proof that
	// Disconnect never attempts what the trigger would reject anyway.
	var unrelatedAuditID ids.UUID
	var unrelatedBefore, unrelatedAfter []byte
	queryRowWS(ctx, t, pool, `
		INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, action, entity_type, entity_id, before, after)
		VALUES ($1, $2, 'human', 'human:test', 'create', 'person', $3, NULL, '{"first_name":"Grace"}'::jsonb)
		RETURNING id, before, after`,
		[]any{ids.NewV7(), ws, ids.NewV7()}, &unrelatedAuditID, &unrelatedBefore, &unrelatedAfter)

	// The connection lifecycle audit row (from Connect above) must survive
	// teardown untouched — find it now so the post-teardown assertion has
	// something to compare against.
	var connectionAuditID ids.UUID
	var beforeConnect, afterConnect []byte
	queryRowWS(ctx, t, pool, `
		SELECT id, before, after FROM audit_log
		WHERE workspace_id = $1 AND entity_type = 'incumbent_connection' AND action = 'create'`,
		[]any{ws}, &connectionAuditID, &beforeConnect, &afterConnect)
	if len(afterConnect) == 0 {
		t.Fatal("connect audit row has no after image to begin with — fixture is broken")
	}

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// The connection row itself: revoked, not deleted.
	var status string
	var revokedAt *time.Time
	queryRowWS(ctx, t, pool,
		`SELECT status, revoked_at FROM incumbent_connection WHERE workspace_id = $1`, []any{ws}, &status, &revokedAt)
	if status != "revoked" || revokedAt == nil {
		t.Errorf("connection = (status=%s, revoked_at=%v), want (revoked, non-nil)", status, revokedAt)
	}

	// The workspace flip reverses: back to native, incumbent cleared.
	var sorMode string
	var incumbentCol *string
	queryRowWS(ctx, t, pool,
		`SELECT x_sor_mode, x_incumbent FROM workspace WHERE id = $1`, []any{ws}, &sorMode, &incumbentCol)
	if sorMode != "native" || incumbentCol != nil {
		t.Errorf("workspace mode = (%s, %v), want (native, nil)", sorMode, incumbentCol)
	}

	// The vault secret is gone: resolving the connection's own
	// credential_ref now answers ErrNotFound.
	var credentialRef string
	queryRowWS(ctx, t, pool,
		`SELECT credential_ref FROM incumbent_connection WHERE workspace_id = $1`, []any{ws}, &credentialRef)
	if _, err := vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(credentialRef)); !errors.Is(err, keyvault.ErrNotFound) {
		t.Errorf("vault.Get after Disconnect = %v, want keyvault.ErrNotFound (the secret must be deleted)", err)
	}

	// EVERY workspace-scoped table the overlay migrations own is empty
	// for this workspace — the table list is DERIVED from the live
	// catalog (overlayWorkspaceTables), not hand-enumerated, so a future
	// overlay migration cannot add an incumbent-derived table that
	// teardown silently leaves behind: a new table must either purge on
	// disconnect or be added to retainedByDesign with a written reason.
	// overlay_write_ledger is deliberately NOT retained: reserved for
	// branch 2, its branch-1 emptiness is asserted rather than assumed,
	// and the branch that first populates it must decide purge-or-retain
	// here.
	retainedByDesign := map[string]string{
		"incumbent_connection": "the revoked lifecycle row IS the retention (asserted above: status=revoked, never deleted)",
		"overlay_tombstone":    "teardown WRITES tombstones — PII-free erasure markers (asserted non-empty below)",
	}
	tables := overlayWorkspaceTables(ctx, t, pool)
	for _, seeded := range []string{
		"overlay_mirror", "overlay_association", "mirror_visibility",
		"mirror_user_map", "overlay_backfill_cursor", "overlay_reconcile_watermark",
	} {
		if !slices.Contains(tables, seeded) {
			t.Fatalf("catalog derivation missed %s (derived %v) — the purge assertion below would be vacuous for it", seeded, tables)
		}
	}
	for _, table := range tables {
		if _, retained := retainedByDesign[table]; retained {
			continue
		}
		var count int
		queryRowWS(ctx, t, pool,
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE workspace_id = $1`, pgx.Identifier{table}.Sanitize()),
			[]any{ws}, &count)
		if count != 0 {
			t.Errorf("%s holds %d row(s) for the workspace after teardown, want 0 — every incumbent-derived table purges on disconnect", table, count)
		}
	}

	// A tombstone was written for the purged mirror row — a later sweep
	// can never resurrect it.
	var tombstoneCount int
	queryRowWS(ctx, t, pool,
		`SELECT count(*) FROM overlay_tombstone WHERE workspace_id = $1 AND object_class = $2 AND external_id = $3`,
		[]any{ws, objectClass, externalID}, &tombstoneCount)
	if tombstoneCount != 1 {
		t.Errorf("tombstone count = %d, want exactly 1 for the purged mirror row", tombstoneCount)
	}

	// The unrelated audit row is untouched, byte-for-byte.
	var reReadBefore, reReadAfter []byte
	queryRowWS(ctx, t, pool,
		`SELECT before, after FROM audit_log WHERE id = $1`, []any{unrelatedAuditID}, &reReadBefore, &reReadAfter)
	if string(reReadAfter) != string(unrelatedAfter) || string(reReadBefore) != string(unrelatedBefore) {
		t.Errorf("unrelated audit row changed: before=%s after=%s, want before=%s after=%s",
			reReadBefore, reReadAfter, unrelatedBefore, unrelatedAfter)
	}

	// The connection lifecycle audit row is RETAINED byte-for-byte — the
	// one record OVA-AC-1 requires teardown to keep.
	var retainedBefore, retainedAfter []byte
	queryRowWS(ctx, t, pool,
		`SELECT before, after FROM audit_log WHERE id = $1`, []any{connectionAuditID}, &retainedBefore, &retainedAfter)
	if string(retainedAfter) != string(afterConnect) {
		t.Errorf("connect audit row's after image changed: got %s, want %s (it must be retained untouched)",
			retainedAfter, afterConnect)
	}

	// A disconnect audit row was written for THIS disconnect too, and it
	// is retained (audit_log is append-only for every actor — nothing
	// overlay does could touch it even if it tried).
	var disconnectCount int
	queryRowWS(ctx, t, pool,
		`SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND entity_type = 'incumbent_connection' AND action = 'archive' AND after IS NOT NULL`,
		[]any{ws}, &disconnectCount)
	if disconnectCount != 1 {
		t.Errorf("disconnect audit rows with a retained after image = %d, want 1", disconnectCount)
	}
}

// TestFencedSyncWritesAbortOnceTheConnectionIsRevoked proves the
// disconnect-race fence: a MirrorStore bound WithFence (the sweep's store)
// serializes every sync write against Disconnect on the incumbent_connection
// row, so once that connection is revoked+purged a stray in-flight sweep
// write aborts with ErrConnectionGone and resurrects nothing. It closes the
// backfill.go re-population race (PR #91 review) for the tables the mirror
// tombstone cannot cover — associations, the backfill cursor, the reconcile
// watermark, the owner-identity map are not record-keyed — AND for a
// brand-new mirror row that never had a tombstone at all.
func TestFencedSyncWritesAbortOnceTheConnectionIsRevoked(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store)
	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-fence-secret"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fenced := store.WithFence()

	// While the connection is active the fence is transparent: a fenced write
	// behaves exactly as an unfenced one, so the sweep's normal operation is
	// unaffected.
	if err := fenced.SaveBackfillCursor(ctx, "contacts", "cur-live", false); err != nil {
		t.Fatalf("fenced write on a live connection = %v, want success", err)
	}

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("testWorkspaceCtx did not bind an actor")
	}
	// Every fenced sync write now aborts with ErrConnectionGone — the
	// connection row is revoked, so the FOR SHARE fence finds no active row.
	// "person/new" was NEVER in the mirror, so no tombstone guards it: only
	// the fence stops Ingest from landing a fresh incumbent-derived row into
	// the now-native workspace.
	fencedWrites := map[string]func() error{
		"Ingest": func() error {
			return fenced.Ingest(ctx, Record{ObjectClass: "person", ExternalID: "new", Fields: map[string]any{"firstname": "Nope"}, ModifiedAt: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)})
		},
		"UpsertAssoc": func() error {
			return fenced.UpsertAssoc(ctx, Assoc{FromType: "person", FromID: "new", ToType: "deal", ToID: "1", TypeID: 1, Category: "HUBSPOT_DEFINED", Direction: "forward"})
		},
		"SaveBackfillCursor": func() error { return fenced.SaveBackfillCursor(ctx, "contacts", "cur-stray", true) },
		"SaveReconcileWatermark": func() error {
			return fenced.SaveReconcileWatermark(ctx, "contacts", time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
		},
		"UpsertUserMap": func() error {
			return fenced.UpsertUserMap(ctx, ids.From[ids.UserKind](actor.UserID), "hubspot", "owner-stray", "manual")
		},
	}
	for name, w := range fencedWrites {
		if err := w(); !errors.Is(err, ErrConnectionGone) {
			t.Errorf("fenced %s after disconnect = %v, want ErrConnectionGone", name, err)
		}
	}

	// Nothing landed: every incumbent-derived table Disconnect purged is
	// still empty for the workspace — the fenced writes added nothing back.
	for _, tbl := range []string{
		"overlay_mirror", "overlay_association", "overlay_backfill_cursor",
		"overlay_reconcile_watermark", "mirror_user_map",
	} {
		var n int
		queryRowWS(ctx, t, pool,
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE workspace_id = $1`, pgx.Identifier{tbl}.Sanitize()),
			[]any{ws}, &n)
		if n != 0 {
			t.Errorf("%s holds %d row(s) after fenced writes on a disconnected workspace, want 0 — the fence must resurrect nothing", tbl, n)
		}
	}
}

func TestDisconnectWithNoActiveConnectionAnswersNotFound(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	if err := svc.Disconnect(ctx); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("Disconnect with no connection = %v, want apperrors.ErrNotFound", err)
	}
}

// overlayWorkspaceTables derives, from the live catalog, every
// workspace-scoped table the overlay migrations own — the overlay_% and
// mirror_% clusters plus incumbent_connection, the same name set
// backend/tableownership_test.go pins to internal/modules/overlay — so
// the teardown purge assertion's coverage grows with the schema instead
// of trailing it as a hand-kept list.
func overlayWorkspaceTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	var tables []string
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT table_name FROM information_schema.columns
			WHERE table_schema = 'public' AND column_name = 'workspace_id'
			  AND (table_name LIKE 'overlay\_%' OR table_name LIKE 'mirror\_%' OR table_name = 'incumbent_connection')
			ORDER BY table_name`)
		if err != nil {
			return err
		}
		tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatalf("deriving the overlay-owned workspace-scoped tables from the catalog: %v", err)
	}
	return tables
}

// countingIncumbent wraps backfill_integration_test.go's pagingCompanies
// to count Backfill list calls — the observable that separates "the done
// cursor short-circuited the run" from "the run genuinely re-listed the
// incumbent".
type countingIncumbent struct {
	pagingCompanies
	lists int
}

var _ Incumbent = (*countingIncumbent)(nil)

func (c *countingIncumbent) Backfill(ctx context.Context, objectClass, cursor string) (Page, error) {
	c.lists++
	return c.pagingCompanies.Backfill(ctx, objectClass, cursor)
}

// TestDisconnectResetsSyncCheckpointsSoAFreshBackfillRelistsFromTheStart
// proves the sync-checkpoint half of the purge with behavior, not row
// counts: a converged cursor short-circuits Backfill (backfill.go) into
// a no-op, so a checkpoint surviving disconnect would make a later
// connection skip its initial mirror load, and a stale watermark would
// resume the incremental sweep mid-stream. After Disconnect, both must
// read exactly as a never-connected workspace's do, and a fresh Backfill
// must actually list the incumbent again.
func TestDisconnectResetsSyncCheckpointsSoAFreshBackfillRelistsFromTheStart(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store)

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-reset-secret"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A real backfill converges: the persisted cursor lands done=true.
	inc := &countingIncumbent{pagingCompanies: pagingCompanies{
		records: []Record{{
			ExternalID:  "1",
			ObjectClass: "organization",
			Fields:      map[string]any{"display_name": "Org 1"},
			ModifiedAt:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		}},
		pageSize: 100,
	}}
	if err := Backfill(ctx, inc, store, "companies"); err != nil {
		t.Fatalf("initial Backfill: %v", err)
	}
	if err := store.SaveReconcileWatermark(ctx, "companies", time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("checkpointing the reconcile watermark: %v", err)
	}

	// The short-circuit under test is real on the still-connected
	// workspace: a second run lists nothing — without this, the
	// post-disconnect "it listed again" assertion below could pass even
	// if the done cursor never short-circuited anything.
	listsBefore := inc.lists
	if err := Backfill(ctx, inc, store, "companies"); err != nil {
		t.Fatalf("Backfill over the converged cursor: %v", err)
	}
	if inc.lists != listsBefore {
		t.Fatalf("a converged backfill listed %d page(s), want 0 (the done cursor short-circuits the run)", inc.lists-listsBefore)
	}

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Both checkpoints answer exactly what a never-connected workspace
	// answers: not started.
	cursor, done, err := store.LoadBackfillCursor(ctx, "companies")
	if err != nil {
		t.Fatalf("LoadBackfillCursor after Disconnect: %v", err)
	}
	if cursor != "" || done {
		t.Errorf("backfill cursor after Disconnect = (%q, done=%v), want (\"\", false) — a retained done cursor skips the next connection's initial mirror load", cursor, done)
	}
	watermark, err := store.LoadReconcileWatermark(ctx, "companies")
	if err != nil {
		t.Fatalf("LoadReconcileWatermark after Disconnect: %v", err)
	}
	if !watermark.IsZero() {
		t.Errorf("reconcile watermark after Disconnect = %v, want the zero time — a retained watermark resumes the sweep mid-stream", watermark)
	}

	// The behavior itself: a fresh Backfill lists the incumbent from the
	// start again — the checkpoint reset restores the LOAD.
	listsBefore = inc.lists
	if err := Backfill(ctx, inc, store, "companies"); err != nil {
		t.Fatalf("Backfill after Disconnect: %v", err)
	}
	if inc.lists == listsBefore {
		t.Error("Backfill after Disconnect listed no pages — the initial mirror load must run again, not resume a purged connection's converged cursor")
	}
	// What that load may NOT do is resurrect the purged rows: the
	// teardown tombstones hold against re-ingest until the reconnect
	// flow clears them while establishing a NEW connection (purgeMirror's
	// contract; Connect today refuses a workspace with any connection
	// row, so no such flow exists yet to exercise).
	var relanded int
	queryRowWS(ctx, t, pool, `SELECT count(*) FROM overlay_mirror WHERE workspace_id = $1`, []any{ws}, &relanded)
	if relanded != 0 {
		t.Errorf("the post-disconnect backfill re-landed %d purged row(s) — the teardown tombstone guard must hold until a reconnect flow clears it", relanded)
	}
}
