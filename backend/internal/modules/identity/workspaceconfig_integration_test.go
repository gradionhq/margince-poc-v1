// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The rails under ResetWorkspaceConfig, against the schema as migrated. The
// column list is derived from the live catalog, so every way this goes wrong
// is drift the Go code cannot see, in one of two directions: a setting that
// escapes the restore, or — the destructive one — a value the deployment
// configured being restored away. The last test here is the rail for that
// second direction, and it is the reason the fixtures below bootstrap through
// the real installation path rather than inserting a row by hand.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// configBootstrap is the deployment configuration every fixture here is
// bootstrapped from. Each value that lands on the workspace row differs from
// that column's declared default — a timezone that is not UTC, a currency that
// is not the fallback — so a restore that reached one of them CHANGES the row
// instead of coincidentally rewriting it with what was already there.
var configBootstrap = InstallationBootstrap{
	BaseCurrency: "CHF", Timezone: "Europe/Berlin",
	AdminName: "Admin", AdminPassword: "a bootstrap password!",
}

// seedConfigWorkspace bootstraps one installation through createInstallation —
// the path a first boot actually takes, so "what a fresh bootstrap leaves" is
// the real thing rather than this file's idea of it — and returns its id.
//
// The database persists across binary runs, so the name (and the slug and
// admin email derived from it) carries the id's random tail: the leading bytes
// are a millisecond timestamp, and two runs inside the same minute would
// collide on workspace_slug_unique.
func seedConfigWorkspace(t *testing.T, pool *pgxpool.Pool, label string) ids.UUID {
	t.Helper()
	ctx := context.Background()
	name := "wsconfig-" + label + "-" + ids.NewV7().String()[24:]
	boot := configBootstrap
	boot.OrganizationName = name
	boot.AdminEmail = "admin@" + name + ".test"

	var ws ids.WorkspaceID
	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		ws, err = createInstallation(ctx, tx, boot, nil)
		return err
	}); err != nil {
		t.Fatalf("bootstrapping the %s installation: %v", label, err)
	}
	return ws.UUID
}

// TestResetWorkspaceConfigRestoresSettingsAndKeepsIdentity is the behavioural
// proof, over the one setting the row still carries: an installation flipped
// into overlay mode — whose two columns move together, because
// x_overlay_iff_incumbent admits no other state — comes back to exactly what a
// fresh bootstrap leaves, while the name, currency and zone bootstrap took from
// the deployment file are untouched.
//
// Those two columns come from the overlay pack's custom migration, which is
// the fork-owned namespace. Core contributes no configuration column to this
// row today, so on a core-only tree there is nothing here to restore and the
// vacuity check below is what reports it.
func TestResetWorkspaceConfigRestoresSettingsAndKeepsIdentity(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()
	ws := seedConfigWorkspace(t, pool, "settings")

	// Move every setting off its default. The two overlay columns move
	// together because x_overlay_iff_incumbent admits no other state. The
	// stamp this leaves is what the restore's own write has to move past.
	var before time.Time
	if err := owner.QueryRow(ctx, `
		UPDATE workspace
		   SET x_sor_mode = 'overlay', x_incumbent = 'hubspot'
		 WHERE id = $1 RETURNING updated_at`, ws).Scan(&before); err != nil {
		t.Fatalf("configuring the workspace away from its defaults: %v", err)
	}

	// The installation's identity as it stands BEFORE the reset. Compared
	// against itself afterwards rather than against configBootstrap, because
	// `setting` is non-tenant: several fixtures bootstrap into one database
	// per package run and Seed is ON CONFLICT DO NOTHING, so the rows belong
	// to whichever installation got there first. Which one that is has nothing
	// to do with the claim under test — that a reset wipes the DATA and leaves
	// the installation standing.
	var nameBefore, currencyBefore, zoneBefore string
	if err := owner.QueryRow(ctx, `
		SELECT coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'), ''),
		       coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.base_currency'), ''),
		       coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.timezone'), '')`).
		Scan(&nameBefore, &currencyBefore, &zoneBefore); err != nil {
		t.Fatalf("reading the installation's identity: %v", err)
	}
	if nameBefore == "" || currencyBefore == "" || zoneBefore == "" {
		t.Fatalf("the fixture bootstrapped no identity to preserve: name=%q currency=%q zone=%q",
			nameBefore, currencyBefore, zoneBefore)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
		return ResetWorkspaceConfig(wsCtx, tx)
	}); err != nil {
		t.Fatalf("ResetWorkspaceConfig: %v", err)
	}

	var mode string
	var incumbent *string
	var after time.Time
	if err := owner.QueryRow(ctx, `
		SELECT x_sor_mode, x_incumbent, updated_at
		  FROM workspace WHERE id = $1`, ws).
		Scan(&mode, &incumbent, &after); err != nil {
		t.Fatalf("reading the workspace back: %v", err)
	}
	// The installation's identity is settings rows now (0211), and this is
	// still the claim under test: a reset wipes the DATA, not the
	// installation. platform/settings.ResetConfig spares these three, so they
	// must read back exactly as bootstrap wrote them.
	var name, currency, timezone string
	if err := owner.QueryRow(ctx, `
		SELECT coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'), ''),
		       coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.base_currency'), ''),
		       coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.timezone'), '')`).
		Scan(&name, &currency, &timezone); err != nil {
		t.Fatalf("reading the installation's identity back: %v", err)
	}
	if mode != "native" {
		t.Errorf("x_sor_mode = %q, want native", mode)
	}
	if incumbent != nil {
		t.Errorf("x_incumbent = %q, want NULL", *incumbent)
	}
	if name != nameBefore || currency != currencyBefore || timezone != zoneBefore {
		t.Errorf("identity was rewritten: name=%q base_currency=%q timezone=%q, was %q/%q/%q — "+
			"a reset wipes the data, not the installation",
			name, currency, timezone, nameBefore, currencyBefore, zoneBefore)
	}

	// created_at is preserved and updated_at is not, and the difference is the
	// point: the restore assigns neither, but it does WRITE the row, so the
	// trigger moves the modification stamp. Pinned because the alternative
	// reads like a tidier invariant ("a reset touches no timestamp") and is
	// the exact appearance the unfixed bug had — a workspace row whose
	// updated_at predates the reset is a row the reset never wrote.
	if !after.After(before) {
		t.Errorf("updated_at is %s after a reset of a row last written %s — the row was written, and the stamp that records that must move",
			after, before)
	}
}

// TestResetWorkspaceConfigLeavesOtherWorkspacesAlone: workspace is the one
// table outside RLS (data-model §1.2), so nothing under this statement stops
// it from restoring every row in the table. The bound GUC is the whole
// isolation, which makes a co-tenant's settings surviving the property worth
// asserting rather than assuming.
func TestResetWorkspaceConfigLeavesOtherWorkspacesAlone(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()
	mine := seedConfigWorkspace(t, pool, "mine")
	theirs := seedConfigWorkspace(t, pool, "theirs")

	if _, err := owner.Exec(ctx,
		`UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot' WHERE id = $1`,
		theirs); err != nil {
		t.Fatalf("configuring the co-tenant: %v", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, mine)
	if err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
		return ResetWorkspaceConfig(wsCtx, tx)
	}); err != nil {
		t.Fatalf("ResetWorkspaceConfig: %v", err)
	}

	var mode string
	if err := owner.QueryRow(ctx,
		`SELECT x_sor_mode FROM workspace WHERE id = $1`, theirs).Scan(&mode); err != nil {
		t.Fatalf("reading the co-tenant back: %v", err)
	}
	if mode != "overlay" {
		t.Errorf("the co-tenant's x_sor_mode = %q, want overlay — one installation's reset reconfigured another's", mode)
	}
}

// TestPreservedWorkspaceColumnsAreRealAndExcluded is the stale-name rail: each
// preserved name must still be a column of the workspace table. A rename or a
// drop that left the set behind would not fail anything — the name simply
// stops matching, and the column it was protecting quietly joins the restore
// set on the next reset.
func TestPreservedWorkspaceColumnsAreRealAndExcluded(t *testing.T) {
	_, pool := setupIdentityDB(t)
	ctx := context.Background()

	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		// information_schema, not the pg_catalog query under test, so this
		// genuinely fails on drift rather than agreeing with itself.
		rows, err := tx.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'workspace'`)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			existing[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for name := range preservedWorkspaceColumns {
			if !existing[name] {
				t.Errorf("preserved column %q is not a current workspace column (renamed/dropped?) — a reset would start restoring what it names", name)
			}
		}
		cols, err := workspaceConfigColumns(ctx, tx)
		if err != nil {
			return err
		}
		for _, col := range cols {
			if preservedWorkspaceColumns[col] {
				t.Errorf("preserved column %q appears in the restore set", col)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("column introspection: %v", err)
	}
}

// TestEveryWorkspaceConfigColumnCanTakeItsDeclaredDefault: the restore writes
// `= DEFAULT`, which on a column declaring no default writes NULL. For a NOT
// NULL column that aborts the whole reset transaction — the sweep, the re-seed
// and the audit row with it — at runtime, on an installation an operator is
// already wiping.
//
// The behavioural tests above would fail on such a column too, since they run
// the real statement. What this adds is the diagnosis: it names the column and
// says what to do about it, instead of leaving a reader to read 23502 off a
// statement that lists every column on the row.
func TestEveryWorkspaceConfigColumnCanTakeItsDeclaredDefault(t *testing.T) {
	_, pool := setupIdentityDB(t)
	ctx := context.Background()

	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		cols, err := workspaceConfigColumns(ctx, tx)
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			t.Fatal("the workspace row carries no configuration column at all — every assertion here would hold vacuously")
		}
		for _, col := range cols {
			var notNull, hasDefault bool
			if err := tx.QueryRow(ctx, `
				SELECT a.attnotnull, a.atthasdef
				FROM pg_attribute a
				JOIN pg_class c ON c.oid = a.attrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relname = 'workspace' AND a.attname = $1`,
				col).Scan(&notNull, &hasDefault); err != nil {
				return err
			}
			if notNull && !hasDefault {
				t.Errorf("config column %q is NOT NULL with no declared default — `SET %s = DEFAULT` writes NULL and aborts the reset; give the column a default or declare it preserved",
					col, col)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("column introspection: %v", err)
	}
}

// perInstallationWorkspaceColumns are the columns two installations differ in
// by construction — the key, the name and slug each was created under, and the
// timestamps of its own creation. Everything else on the row is either the
// deployment configuration both were bootstrapped from or a setting at its
// declared default, so the comparison below can hold them equal.
var perInstallationWorkspaceColumns = map[string]bool{
	"id": true, "name": true, "slug": true, "created_at": true, "updated_at": true,
}

// TestAResetWorkspaceMatchesAFreshlyBootstrappedOne is the rail for the
// direction that loses data rather than leaking it.
//
// Restoring is the DEFAULT here: a column not named in preservedWorkspaceColumns
// is restored, which is what keeps a new setting from escaping the reset. The
// cost of that default is the mirror-image mistake — someone adds a column
// bootstrap writes from margince.yaml and does not declare it preserved, and
// the reset then quietly discards what the deployment configured. No rail over
// the restore set can see that, because nothing distinguishes such a column
// from a setting by shape.
//
// Comparing against a SECOND installation bootstrapped from the same
// configuration can: the fresh one still carries the configured value, the
// reset one has been returned to the column's default, and the two rows
// disagree on a key. That is why both fixtures run through createInstallation,
// and why configBootstrap's values are all off-default — a column bootstrap
// writes with exactly its own default is invisible here, and to every other
// test, because there is nothing to observe.
func TestAResetWorkspaceMatchesAFreshlyBootstrappedOne(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()

	// A bootstrap field added later reaches the workspace row only if
	// createInstallation writes it there, and this test can only see it if
	// configBootstrap sets it off-default. Neither is checkable from here, so
	// the field count is the tripwire that sends the next author to look.
	if got := reflect.TypeOf(InstallationBootstrap{}).NumField(); got != 6 {
		t.Errorf("InstallationBootstrap has %d fields, this test was written against 6 — if the new one lands on the workspace row, declare its column in preservedWorkspaceColumns and give it an off-default value in configBootstrap, then update this count", got)
	}

	ws := seedConfigWorkspace(t, pool, "reset")
	fresh := seedConfigWorkspace(t, pool, "fresh")
	if _, err := owner.Exec(ctx, `
		UPDATE workspace
		   SET x_sor_mode = 'overlay', x_incumbent = 'hubspot'
		 WHERE id = $1`, ws); err != nil {
		t.Fatalf("configuring the workspace away from its defaults: %v", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
		return ResetWorkspaceConfig(wsCtx, tx)
	}); err != nil {
		t.Fatalf("ResetWorkspaceConfig: %v", err)
	}

	after, freshRow := readWorkspaceRow(t, owner, ws), readWorkspaceRow(t, owner, fresh)
	for col, want := range freshRow {
		if perInstallationWorkspaceColumns[col] {
			continue
		}
		if got := after[col]; !reflect.DeepEqual(got, want) {
			t.Errorf("column %q is %v after a reset, but a freshly bootstrapped installation carries %v — if this column holds deployment configuration, declare it in preservedWorkspaceColumns; if it is a setting, its default and its bootstrap value disagree",
				col, got, want)
		}
	}
}

// readWorkspaceRow returns one workspace row as column → value. The whole row,
// not a named list: a column added later has to reach the comparison above
// without anyone remembering to add it here.
func readWorkspaceRow(t *testing.T, owner *pgx.Conn, ws ids.UUID) map[string]any {
	t.Helper()
	var row map[string]any
	if err := owner.QueryRow(context.Background(),
		`SELECT to_jsonb(w) FROM workspace w WHERE id = $1`, ws).Scan(&row); err != nil {
		t.Fatalf("reading the workspace row: %v", err)
	}
	return row
}

// The empty-column path, which core-only trees take.
//
// capture_auto_enrich was core's only configuration column on this row and it
// moved into `setting`; x_sor_mode and x_incumbent come from the overlay
// pack's custom migration, which is the fork-owned namespace upstream ships
// empty. So a vanilla tree reaches the early return, and until now nothing
// exercised it — the branch was documented as reachable and never reached.
//
// Proven by dropping the two fork columns INSIDE a transaction that is then
// rolled back, so the real schema is untouched and the assertion runs against
// the real function rather than a stand-in. That is the only way to see a
// core-only workspace row on a database the overlay pack has already migrated.
//
// It runs on the OWNER connection rather than through WithWorkspaceTx: ALTER
// TABLE needs table ownership, and the app role the helper uses deliberately
// does not have it.
func TestResetWorkspaceConfigOnARowThatIsIdentityAndNothingElse(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()
	ws := seedConfigWorkspace(t, pool, "core-only")

	var nameBefore string
	if err := owner.QueryRow(ctx,
		`SELECT slug FROM workspace WHERE id = $1`, ws).Scan(&nameBefore); err != nil {
		t.Fatalf("reading the workspace: %v", err)
	}

	// On the OWNER connection, not the app pool WithWorkspaceTx uses: ALTER
	// TABLE needs table ownership, and the app role deliberately does not have
	// it. The GUC is set by hand for the same reason — ResetWorkspaceConfig
	// scopes its UPDATE with current_setting('app.workspace_id'), which the
	// helper would normally have bound.
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the probe transaction: %v", err)
	}
	// Rolled back unconditionally: the columns come back whatever happens
	// below, including a t.Fatal that leaves this function early.
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rolling the probe back: %v — the fork columns may not have been restored", err)
		}
	}()

	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.workspace_id', $1, true)`, ws.String()); err != nil {
		t.Fatalf("binding the workspace for the probe: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`ALTER TABLE workspace DROP COLUMN x_sor_mode, DROP COLUMN x_incumbent`); err != nil {
		t.Fatalf("dropping the fork columns inside the probe: %v", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := ResetWorkspaceConfig(wsCtx, tx); err != nil {
		t.Fatalf("ResetWorkspaceConfig on an identity-only row: %v — want it to do nothing and succeed", err)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("restoring the fork columns: %v", err)
	}

	// The schema is intact and the row untouched: the probe left no trace.
	var mode string
	var nameAfter string
	if err := owner.QueryRow(ctx,
		`SELECT x_sor_mode, slug FROM workspace WHERE id = $1`, ws).Scan(&mode, &nameAfter); err != nil {
		t.Fatalf("the rollback did not restore the fork columns: %v", err)
	}
	if nameAfter != nameBefore {
		t.Errorf("slug = %q, want %q — the probe wrote to the row it only meant to read", nameAfter, nameBefore)
	}
}
