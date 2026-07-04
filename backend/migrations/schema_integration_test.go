//go:build integration

package migrations

// Integration lane (make test-integration): exercises the real schema on
// Postgres 16 — apply/reverse/re-apply, and the four blocking RLS gates
// from data-model §1.3: ∅-query, GUC-unset deny (read AND write), the
// version bump (§1.3a), and audit_log append-only (§11). Fails loudly when
// the database is missing rather than skipping (a skipped security gate
// looks exactly like a passing one).

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// ownerDSN administers the throwaway test database; appDSNFmt is the
// non-owner runtime role RLS must bind.
func dsns(t *testing.T) (owner string, appFmt string) {
	t.Helper()
	owner = os.Getenv("MARGINCE_TEST_DSN")
	if owner == "" {
		t.Fatal("MARGINCE_TEST_DSN is not set — run `make db-up` and try again (integration tests fail loudly, they never skip)")
	}
	return owner, os.Getenv("MARGINCE_TEST_APP_DSN")
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func migrateAll(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	custom, err := Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(context.Background(), conn, core, custom); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// resetSchema drops everything so each test run starts clean.
func resetSchema(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("re-granting schema usage: %v", err)
	}
}

func TestMigrations_applyReverseReapply(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}

	applied, err := dbmigrate.Up(ctx, conn, core)
	if err != nil {
		t.Fatalf("first up: %v", err)
	}
	if applied != len(core.Migrations) {
		t.Fatalf("applied %d, want %d", applied, len(core.Migrations))
	}

	// Idempotent: a second run applies nothing.
	applied, err = dbmigrate.Up(ctx, conn, core)
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if applied != 0 {
		t.Fatalf("re-run applied %d, want 0", applied)
	}

	// Every migration reverses (B-EP02.1b), then the schema re-applies.
	reverted, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations))
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if reverted != len(core.Migrations) {
		t.Fatalf("reverted %d, want %d", reverted, len(core.Migrations))
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("re-apply after full down: %v", err)
	}
}

func seedWorkspace(t *testing.T, conn *pgx.Conn, slug string) string {
	t.Helper()
	var id string
	err := conn.QueryRow(context.Background(),
		`INSERT INTO workspace (name, slug, base_currency) VALUES ($1, $1, 'EUR') RETURNING id`,
		slug).Scan(&id)
	if err != nil {
		t.Fatalf("seeding workspace %s: %v", slug, err)
	}
	return id
}

// withGUC runs fn in a transaction bound to a workspace, mirroring the
// production database.WithWorkspaceTx contract.
func withGUC(t *testing.T, conn *pgx.Conn, wsID string, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if wsID != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
			t.Fatalf("set_config: %v", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestRLS_tenantIsolationGates(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN is not set — the RLS gates must run as the non-owner runtime role")
	}
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)

	wsA := seedWorkspace(t, owner, "tenant-a")
	wsB := seedWorkspace(t, owner, "tenant-b")

	app := connect(t, appDSN)
	ctx := context.Background()

	insertPerson := func(wsID, name string) error {
		return withGUC(t, app, wsID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'test', 'human:test')`,
				wsID, name)
			return err
		})
	}
	if err := insertPerson(wsA, "Ada A"); err != nil {
		t.Fatalf("insert into tenant A: %v", err)
	}
	if err := insertPerson(wsB, "Ben B"); err != nil {
		t.Fatalf("insert into tenant B: %v", err)
	}

	// ∅-query: tenant A's GUC sees none of tenant B's rows.
	_ = withGUC(t, app, wsA, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person`).Scan(&n); err != nil {
			t.Fatalf("count under tenant A: %v", err)
		}
		if n != 1 {
			t.Errorf("tenant A sees %d persons, want exactly its own 1", n)
		}
		return nil
	})

	// GUC-unset: a connection with no workspace reads ZERO rows...
	_ = withGUC(t, app, "", func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person`).Scan(&n); err != nil {
			t.Fatalf("count with unset GUC: %v", err)
		}
		if n != 0 {
			t.Errorf("unset GUC sees %d rows, want 0 (deny-on-unset, never wildcard)", n)
		}
		return nil
	})

	// ...and cannot write (WITH CHECK).
	err := withGUC(t, app, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Eve', 'test', 'human:test')`,
			wsA)
		return err
	})
	if err == nil {
		t.Error("insert with unset GUC succeeded; RLS WITH CHECK must reject it")
	}

	// Cross-tenant write: tenant B's GUC cannot insert a tenant-A row.
	err = withGUC(t, app, wsB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Mallory', 'test', 'human:test')`,
			wsA)
		return err
	})
	if err == nil {
		t.Error("cross-tenant insert succeeded; WITH CHECK must reject it")
	}
}

func TestVersionBumpAndSkewSemantics(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ws := seedWorkspace(t, owner, "tenant-v")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	var version int64
	_ = withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Vera', 'test', 'human:test') RETURNING id, version`,
			ws).Scan(&id, &version)
	})
	if version != 1 {
		t.Fatalf("fresh row version = %d, want 1", version)
	}

	// The trigger bumps version on every UPDATE (data-model §1.3a).
	_ = withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE person SET title = 'CTO' WHERE id = $1 RETURNING version`, id).Scan(&version)
	})
	if version != 2 {
		t.Fatalf("version after update = %d, want 2", version)
	}

	// The If-Match write shape: a stale version matches zero rows.
	_ = withGUC(t, app, ws, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE person SET title = 'CEO' WHERE id = $1 AND version = $2`, id, int64(1))
		if err != nil {
			t.Fatalf("stale update: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Error("stale If-Match version updated a row; must affect 0 → 409 version_skew")
		}
		return nil
	})
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ws := seedWorkspace(t, owner, "tenant-audit")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	_ = withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type)
			 VALUES ($1, 'human', 'human:test', 'create', 'person') RETURNING id`, ws).Scan(&id)
	})

	for _, stmt := range []string{
		`UPDATE audit_log SET actor_id = 'tampered' WHERE id = $1`,
		`DELETE FROM audit_log WHERE id = $1`,
	} {
		err := withGUC(t, app, ws, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, id)
			return err
		})
		var pgErr *pgconn.PgError
		if err == nil {
			t.Errorf("%q succeeded; audit_log must be append-only", stmt)
		} else if !errors.As(err, &pgErr) {
			t.Errorf("%q failed with %v, want a loud database error", stmt, err)
		}
	}
}

// TestRLS_coversEveryTenantTable is the fitness function for the "RLS on
// every tenant table" invariant: 0014 enrols tables from a hand-written
// list, and a hand-written list rots — a future migration that adds a
// workspace_id table but forgets the enrolment would ship without RLS,
// silently. Here the DATABASE is the source of truth: every base table
// carrying a workspace_id column must have ENABLE + FORCE row security
// and at least one policy, or this test names the stragglers.
func TestRLS_coversEveryTenantTable(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `
		SELECT c.relname,
		       c.relrowsecurity,
		       c.relforcerowsecurity,
		       EXISTS (SELECT 1 FROM pg_policies p
		               WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace
		  AND c.relkind = 'r'
		  AND EXISTS (SELECT 1 FROM pg_attribute a
		              WHERE a.attrelid = c.oid AND a.attname = 'workspace_id' AND NOT a.attisdropped)
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("querying tenant tables: %v", err)
	}
	defer rows.Close()

	tenantTables := 0
	for rows.Next() {
		var name string
		var enabled, forced, hasPolicy bool
		if err := rows.Scan(&name, &enabled, &forced, &hasPolicy); err != nil {
			t.Fatal(err)
		}
		tenantTables++
		if !enabled || !forced {
			t.Errorf("table %s carries workspace_id but RLS is enable=%v force=%v — enrol it in the RLS migration", name, enabled, forced)
		}
		if !hasPolicy {
			t.Errorf("table %s has RLS flags but NO policy — it would deny everything, or worse, a later DISABLE would open it", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Vacuous-pass guard: the schema has dozens of tenant tables; finding
	// almost none means the detection query broke, not that the schema shrank.
	if tenantTables < 30 {
		t.Fatalf("found only %d workspace_id tables — the fitness query no longer sees the schema", tenantTables)
	}
}

// TestFK_tenantLocalReferencesAreComposite is the fitness function for the
// same-workspace-FK invariant (C4, data-model tenancy integrity): RLS
// bounds row VISIBILITY, but a plain `owner_id -> app_user(id)` FK does not
// prove the target lives in the SAME workspace — a bad app path, import
// job, or guessed UUID can plant a cross-tenant reference that passes the
// FK. Every FK from one workspace_id table to another must therefore carry
// workspace_id on both sides, so the database rejects a cross-workspace
// target by construction. Here the DATABASE is the source of truth: any
// tenant-local FK that omits workspace_id from its key is named. Exceptions
// (a FK to workspace(id) itself, the tenant root) are excluded.
func TestFK_tenantLocalReferencesAreComposite(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	// For each FK constraint whose owning table AND referenced table both
	// carry workspace_id, assert 'workspace_id' is among the referencing
	// columns. (A composite FK that includes workspace_id on the left must
	// include it on the right too — Postgres matches by position against the
	// referenced unique key — so checking the left side is sufficient.)
	rows, err := owner.Query(ctx, `
		WITH tenant_tables AS (
			SELECT c.oid, c.relname
			FROM pg_class c
			WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r'
			  AND EXISTS (SELECT 1 FROM pg_attribute a
			              WHERE a.attrelid = c.oid AND a.attname = 'workspace_id' AND NOT a.attisdropped)
		)
		SELECT con.conname, src.relname, ref.relname,
		       EXISTS (
		         SELECT 1 FROM unnest(con.conkey) AS k
		         JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k
		         WHERE a.attname = 'workspace_id'
		       ) AS includes_workspace
		FROM pg_constraint con
		JOIN tenant_tables src ON src.oid = con.conrelid
		JOIN tenant_tables ref ON ref.oid = con.confrelid
		WHERE con.contype = 'f'
		ORDER BY src.relname, con.conname`)
	if err != nil {
		t.Fatalf("querying tenant-local FKs: %v", err)
	}
	defer rows.Close()

	fks := 0
	for rows.Next() {
		var name, srcTable, refTable string
		var composite bool
		if err := rows.Scan(&name, &srcTable, &refTable, &composite); err != nil {
			t.Fatal(err)
		}
		fks++
		if !composite {
			t.Errorf("FK %s (%s -> %s) omits workspace_id — make it composite (workspace_id, <col>) REFERENCES %s(workspace_id, id) so a cross-workspace target is rejected by the database",
				name, srcTable, refTable, refTable)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Vacuous-pass guard: the schema has dozens of tenant-local FKs.
	if fks < 25 {
		t.Fatalf("found only %d tenant-local FKs — the fitness query no longer sees the schema", fks)
	}
}

// TestFK_rowScopedTargetsHaveVisibilityDecision derives the H1 obligation
// from the schema: an FK argument that names a row-scoped business record
// (person/organization/deal/lead/activity) is a READ of that record, so
// every such column must carry an explicit decision — client-supplied
// references are gated by a target-visibility probe (auth.EnsureLinkTarget
// or the activity link walk), server-derived pointers and owned child rows
// are named as such. A new FK to a row-scoped table that nobody classified
// fails here, so the decision cannot be skipped silently.
func TestFK_rowScopedTargetsHaveVisibilityDecision(t *testing.T) {
	// The classification. Values are prose for the reader; the map's
	// completeness is the invariant.
	decisions := map[string]string{
		// Client-supplied references — visibility-gated at the store:
		"deal.organization_id":          "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1)",
		"deal.partner_org_id":           "gated: auth.EnsureLinkTarget in UpdateDeal (H1)",
		"organization.parent_org_id":    "gated: auth.EnsureLinkTarget in Create/UpdateOrganization (H1)",
		"activity_link.person_id":       "gated: auth.EnsureLinkTarget in LogActivity",
		"activity_link.organization_id": "gated: auth.EnsureLinkTarget in LogActivity",
		"activity_link.deal_id":         "gated: auth.EnsureLinkTarget in LogActivity",
		// Owned child rows: the row is an attribute of its visible parent,
		// written only through the parent's own gated paths.
		"activity_link.activity_id":           "child row: written only inside LogActivity for the new activity",
		"consent_event.person_id":             "child row: written through the person's own gated paths",
		"organization_domain.organization_id": "child row: written through the organization's own gated paths",
		"person_email.person_id":              "child row: written through the person's own gated paths",
		"person_phone.person_id":              "child row: written through the person's own gated paths",
		"person_consent.person_id":            "child row: written through the person's own gated paths",
		// Server-derived pointers: stamped from an operation's outcome,
		// never accepted from the request body.
		"lead.promoted_person_id":       "server-derived: stamped by PromoteLead",
		"person.merged_into_id":         "server-derived: stamped by MergePerson",
		"organization.merged_into_id":   "server-derived: stamped by MergeOrganization",
		"person.converted_from_lead_id": "server-derived: stamped by PromoteLead",
		"deal_stage_history.deal_id":    "server-derived: appended by CreateDeal/AdvanceDeal",
		// No client-facing write path exists yet; the builder of one must
		// re-classify the column here (relationship/partner rows today are
		// written only by merge's server-side relink).
		"relationship.person_id":           "no client write path yet: merge relink only",
		"relationship.counterparty_org_id": "no client write path yet: merge relink only",
		"relationship.organization_id":     "no client write path yet: merge relink only",
		"relationship.deal_id":             "no client write path yet: merge relink only",
		"partner.organization_id":          "no client write path yet: merge relink only",
	}

	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `
		SELECT c.conrelid::regclass::text AS src_table, a.attname AS src_col,
		       c.confrelid::regclass::text AS target_table
		FROM pg_constraint c
		JOIN unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.contype = 'f'
		  AND c.confrelid::regclass::text IN ('person','organization','deal','lead','activity')
		  AND a.attname <> 'workspace_id'
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var srcTable, srcCol, target string
		if err := rows.Scan(&srcTable, &srcCol, &target); err != nil {
			t.Fatal(err)
		}
		key := srcTable + "." + srcCol
		seen[key] = true
		if _, decided := decisions[key]; !decided {
			t.Errorf("FK %s -> %s has no visibility decision: a reference to a row-scoped record is a read of it — gate it (auth.EnsureLinkTarget) or classify it here", key, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// The map must not carry dead entries either — a renamed column would
	// otherwise keep a stale decision alive forever.
	for key := range decisions {
		if !seen[key] {
			t.Errorf("decision map entry %s matches no FK in the live schema — remove or fix it", key)
		}
	}
}
