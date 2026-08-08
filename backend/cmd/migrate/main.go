// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command migrate is the schema-migration process role (ADR-0054,
// amended §2): applies the embedded core + custom namespaces (ADR-0017)
// and the composed extension set's namespaces (ADR-0069) with the
// owner-role DSN. Thin main, a testable run().
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	// The composed extension set: build/composition/ in a composed build,
	// the committed vanilla stub otherwise. This role must wire it —
	// extension tables exist only if this process creates them, and a
	// migrate resolving the vanilla stub would report "schema is at head"
	// over a database missing every extension's schema.
	"github.com/gradionhq/margince/composition"
	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/migrations"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: migrate <up|down|reset-password|recreate-db|drop-db|db-exists> --dsn <dsn> [--steps n] [--email <address>] [--name <db>] [--template <db>]")
	}
	direction := args[0]

	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (owner role)")
	steps := fs.Int("steps", 1, "migrations to revert (down only)")
	email := fs.String("email", "", "user email (reset-password only)")
	name := fs.String("name", "", "database name (recreate-db, drop-db, db-exists only)")
	template := fs.String("template", "", "template database to copy (recreate-db only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("migrate: --dsn or MARGINCE_DSN required")
	}

	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("migrate: connecting: %w", err)
	}
	//craft:ignore swallowed-errors close at process exit after the migration outcome is decided — a failed close cannot un-apply schema changes
	defer func() { _ = conn.Close(ctx) }()

	core, err := migrations.Core()
	if err != nil {
		return err
	}
	custom, err := migrations.Custom()
	if err != nil {
		return err
	}

	switch direction {
	case "up":
		exts, err := extensionNamespaces(composition.Extensions())
		if err != nil {
			return err
		}
		return up(ctx, conn, *dsn, core, custom, exts, stdout)
	case "down":
		// Down reverts the SQL namespaces only — custom first (it sits on top
		// of core), --steps at a time. River's schema is infrastructure with
		// its own migrator; rolling it back is a separate deliberate step, not
		// folded into this counter (a plain `down` must never surprise the
		// operator by dropping a River migration). An extension's namespace is
		// excluded for the same reason and one more: its down-migration DROPs
		// the unit's tables, so a `--steps 1` aimed at the fork's last change
		// must never reach a tenant's extension data.
		reverted, err := dbmigrate.Down(ctx, conn, custom, *steps)
		if err != nil {
			return err
		}
		if reverted < *steps {
			more, err := dbmigrate.Down(ctx, conn, core, *steps-reverted)
			if err != nil {
				return err
			}
			reverted += more
		}
		if _, err := fmt.Fprintf(stdout, "reverted %d migration(s)\n", reverted); err != nil {
			return fmt.Errorf("migrate down: writing the confirmation: %w", err)
		}
		return nil
	case "reset-password":
		return resetPassword(ctx, conn, *email, os.Stdin, stdout)
	case "recreate-db":
		return recreateDB(ctx, conn, *name, *template, stdout)
	case "drop-db":
		return dropDB(ctx, conn, *name, stdout)
	case "db-exists":
		return dbExists(ctx, conn, *name, stdout)
	default:
		return fmt.Errorf("migrate: unknown direction %q (want up, down, reset-password, recreate-db, drop-db or db-exists)", direction)
	}
}

// up applies the embedded SQL namespaces and the composed extension set's,
// then River's schema. River owns its schema through its own migrator,
// applied last (ADR-0017 order puts core, then custom, then the further
// namespaces); its migrator wants a pool, not the single conn the SQL
// runner uses, so one is opened on the same owner DSN.
//
// The extension namespaces go in the SAME dbmigrate.Up call as core and
// custom, not a second one: Up holds a cluster-wide advisory lock for the
// length of one call, and splitting the lanes would open a window between
// them in which a second migrator could interleave.
func up(ctx context.Context, conn *pgx.Conn, dsn string, core, custom dbmigrate.Namespace, exts []dbmigrate.Namespace, stdout io.Writer) error {
	if err := reportExtensionNamespaces(exts, stdout); err != nil {
		return err
	}
	applied, err := dbmigrate.Up(ctx, conn, append([]dbmigrate.Namespace{core, custom}, exts...)...)
	if err != nil {
		return err
	}
	riverPool, err := database.NewPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("migrate: opening river pool: %w", err)
	}
	defer riverPool.Close()
	riverApplied, err := jobs.Migrate(ctx, riverPool)
	if err != nil {
		return err
	}
	if _, err := riverPool.Exec(ctx, riverWorkspaceArgIndex); err != nil {
		return fmt.Errorf("migrate: creating the river workspace-arg index: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, upSummaryFormat, applied, riverApplied); err != nil {
		return fmt.Errorf("migrate up: writing the confirmation: %w", err)
	}
	return nil
}

// upSummaryFormat is the LAST line `migrate up` prints, and it is a wire
// contract rather than cosmetics: scripts/lib-testdb.sh's migrate_template
// string-matches its zero-applied form to decide whether the integration
// template was stale, and reports "was behind" when it does not match. Drift
// on either side makes that check cry wolf on every single run, which is
// worse than not having it — and build_template discards the output, so
// nobody would notice. TestUpSummaryMatchesTheShellMatcher reads both sides
// and fails on drift.
//
// The extension count is folded into the same total as core+custom
// deliberately: that is what makes a template missing an extension's
// migration read as "behind" instead of passing on the core lane alone.
const upSummaryFormat = "applied %d core+custom+extension + %d river migration(s); schema is at head\n"

// riverWorkspaceArgIndex indexes River's per-job workspace argument. Jobs
// fan out per workspace and both job-health statements already scan the
// table, so the fan-out multiplies the rows they read.
//
// It lives here rather than in a migration file for two reasons that both
// come from river_job not being ours: the table does not exist while the
// core lane runs (River's own migrator creates it, on the pool opened
// above), and dbmigrate.Up wraps every migration in a transaction.
//
// Deliberately NOT CONCURRENTLY. This runs outside dbmigrate's
// per-migration transaction but alongside boot, and a plain CREATE INDEX
// on a fresh river_job is trivial. If that table ever grows large enough
// for the write lock to matter, the answer is an explicit
// non-transactional lane in the migrator — a separate change, not a flag
// on this one.
const riverWorkspaceArgIndex = `
CREATE INDEX IF NOT EXISTS river_job_workspace_arg
    ON river_job ((args ->> 'workspace_id'))`

// extensionNamespaces turns the composed extension set into migration
// namespaces — one per unit that ships a migrations layer, each tracked in
// its own schema_migrations_ext_<name>.
//
// The bytes come from the unit's own embedded FS, so this works in the
// deployed image, where there is no extensions/ tree to read (Dockerfile.api
// ships the binary alone).
//
// Sorted by unit name. No unit's schema may depend on another's — each owns
// only its ext_<name>_ tables — so the order is not a correctness
// requirement; it is that two runs of one composition must produce the same
// migration log, and the composed slice's order belongs to the generator.
func extensionNamespaces(exts []extension.Extension) ([]dbmigrate.Namespace, error) {
	ordered := make([]extension.Extension, len(exts))
	copy(ordered, exts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	namespaces := make([]dbmigrate.Namespace, 0, len(ordered))
	for _, e := range ordered {
		if e.Migrations == nil {
			continue // a unit that owns no tables, which is the common case
		}
		// NamespaceFor, not a local derivation: the tracking table, the
		// ext_<name>_ table prefix and the ext_<name> role are ONE namespace,
		// and a second spelling is how they start disagreeing. It validates
		// the unit name too, so a name that could not be a SQL identifier is
		// refused before any DDL runs.
		namespace, err := dbmigrate.NamespaceFor(string(e.Name))
		if err != nil {
			return nil, fmt.Errorf("migrate: extension %q: %w", e.Name, err)
		}
		loaded, err := dbmigrate.Load(e.Migrations, extension.MigrationsDir)
		if err != nil {
			return nil, fmt.Errorf("migrate: extension %q: %w", e.Name, err)
		}
		if len(loaded) == 0 {
			return nil, fmt.Errorf("migrate: extension %q embeds %s/ but it holds no NNNN_name.up.sql/.down.sql pair — a declared-but-empty layer reads as a schema that applied, so leave Migrations nil for a unit that owns no tables", e.Name, extension.MigrationsDir)
		}
		namespaces = append(namespaces, dbmigrate.Namespace{Name: namespace, Migrations: loaded})
	}
	return namespaces, nil
}

// reportExtensionNamespaces names the extension lanes BEFORE they are
// applied, and says so explicitly when there are none.
//
// Printing nothing for the empty set would be the failure this whole wiring
// exists to prevent: a migrate built against the vanilla stub, or run
// without the composed workspace, applies zero extension migrations and is
// otherwise indistinguishable from a correct run over a composition with no
// schema. One line either way makes the difference visible in a log.
func reportExtensionNamespaces(exts []dbmigrate.Namespace, stdout io.Writer) error {
	if len(exts) == 0 {
		_, err := fmt.Fprintln(stdout, "extension migration namespaces: none in the composed set")
		if err != nil {
			return fmt.Errorf("migrate up: writing the extension namespaces: %w", err)
		}
		return nil
	}
	named := make([]string, 0, len(exts))
	for _, ns := range exts {
		named = append(named, fmt.Sprintf("%s (%d declared)", ns.Name, len(ns.Migrations)))
	}
	if _, err := fmt.Fprintf(stdout, "extension migration namespaces: %s\n", strings.Join(named, ", ")); err != nil {
		return fmt.Errorf("migrate up: writing the extension namespaces: %w", err)
	}
	return nil
}

// The database-lifecycle verbs below serve the integration lane's
// clone-per-package shape (scripts/lib-testdb.sh): they run over the same
// owner DSN the migrations and tests use, so the lane needs no psql — and an
// overridden MARGINCE_TEST_DSN targets ONE cluster for clone, migrate, and
// test alike. The --dsn must name a maintenance database (`postgres`):
// CREATE/DROP DATABASE cannot run inside the database being dropped.

// fitsIdentifier rejects a value longer than the server's identifier limit
// (63 bytes on stock builds). Postgres silently TRUNCATES longer identifiers
// — quoted or not, with only a NOTICE a script never sees — so an unchecked
// long name would make recreate-db/drop-db act on a database the caller
// never named, while db-exists (an exact datname compare, and datname can
// never hold a longer name) answers for one that cannot exist. Rejecting up
// front, before any destructive statement, keeps the three verbs consistent.
func fitsIdentifier(ctx context.Context, conn *pgx.Conn, what, value string) error {
	var limit int
	if err := conn.QueryRow(ctx, "SELECT current_setting('max_identifier_length')::int").Scan(&limit); err != nil {
		return fmt.Errorf("%s: reading the server's identifier limit: %w", what, err)
	}
	if len(value) > limit {
		return fmt.Errorf("%s: %q is %d bytes, over the server's %d-byte identifier limit — Postgres would silently truncate it and act on a different database; pick a shorter name", what, value, len(value), limit)
	}
	return nil
}

// recreateDB drops the named database if present and creates it fresh —
// from --template when given (CREATE DATABASE ... TEMPLATE, a fast file
// copy that needs no session connected to the template). The drop is WITH
// (FORCE): a stale clone left by a crashed run may still hold sessions, and
// starting over is exactly the caller's intent.
func recreateDB(ctx context.Context, conn *pgx.Conn, name, template string, stdout io.Writer) error {
	if name == "" {
		return errors.New("migrate recreate-db: --name is required")
	}
	if err := fitsIdentifier(ctx, conn, "migrate recreate-db: --name", name); err != nil {
		return err
	}
	if template != "" {
		if err := fitsIdentifier(ctx, conn, "migrate recreate-db: --template", template); err != nil {
			return err
		}
		// The drop runs before the create: recreating a database from itself
		// would destroy the template first and then copy from nothing. The
		// compare uses the sanitized forms — the same normalization the DDL
		// below splices — so two spellings of one datname cannot slip past.
		if (pgx.Identifier{template}).Sanitize() == (pgx.Identifier{name}).Sanitize() {
			return fmt.Errorf("migrate recreate-db: --template %q is the database being recreated — the drop would destroy the template before the create can copy it; pass a distinct template", template)
		}
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("migrate recreate-db: dropping %q: %w", name, err)
	}
	create := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		create += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	if _, err := conn.Exec(ctx, create); err != nil {
		return fmt.Errorf("migrate recreate-db: creating %q: %w", name, err)
	}
	if _, err := fmt.Fprintf(stdout, "recreated %s\n", name); err != nil {
		return fmt.Errorf("migrate recreate-db: writing the confirmation: %w", err)
	}
	return nil
}

// dropDB drops the named database if present — WITH (FORCE), terminating
// lingering sessions: the verb tears down throwaway clones right after a
// test process exits, when its backends may not have noticed yet, and a
// teardown that can lose that race would fail flakily. Dropping an absent
// database succeeds (IF EXISTS), so teardown paths need no pre-check.
func dropDB(ctx context.Context, conn *pgx.Conn, name string, stdout io.Writer) error {
	if name == "" {
		return errors.New("migrate drop-db: --name is required")
	}
	if err := fitsIdentifier(ctx, conn, "migrate drop-db: --name", name); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("migrate drop-db: dropping %q: %w", name, err)
	}
	if _, err := fmt.Fprintf(stdout, "dropped %s\n", name); err != nil {
		return fmt.Errorf("migrate drop-db: writing the confirmation: %w", err)
	}
	return nil
}

// dbExists prints "true" or "false" — output, not exit code, so callers can
// tell "absent" apart from "could not ask" (a connection failure still exits
// non-zero).
func dbExists(ctx context.Context, conn *pgx.Conn, name string, stdout io.Writer) error {
	if name == "" {
		return errors.New("migrate db-exists: --name is required")
	}
	if err := fitsIdentifier(ctx, conn, "migrate db-exists: --name", name); err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return fmt.Errorf("migrate db-exists: probing %q: %w", name, err)
	}
	if _, err := fmt.Fprintf(stdout, "%t\n", exists); err != nil {
		return fmt.Errorf("migrate db-exists: writing the answer: %w", err)
	}
	return nil
}

// resetPassword is the operator-only recovery path (A107/ADR-0061 §9.1):
// reset a named user's password directly against the database — the
// fallback when outbound email is not configured, and the way back in
// when the administrator is locked out. The new password arrives on
// stdin, never argv (the process table is world-readable). This binary
// is the operator surface: the schema role that runs migrations is the
// authority the recovery path requires, and no HTTP route exists for it.
func resetPassword(ctx context.Context, conn *pgx.Conn, email string, stdin io.Reader, stdout io.Writer) error {
	if email == "" {
		return errors.New("migrate reset-password: --email is required")
	}
	if _, err := fmt.Fprint(stdout, "new password (min 12 chars): "); err != nil {
		return fmt.Errorf("migrate reset-password: writing the prompt: %w", err)
	}
	newPassword, err := readPassword(stdin, stdout)
	if err != nil {
		return err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is checked, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()

	// Bind the installation's singleton organization (FORCE RLS applies
	// to the owner role too). More than one active workspace is the same
	// operator-led-migration refusal every process role gives.
	wsID, err := singletonWorkspace(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
		return err
	}
	if err := identity.OperatorResetPassword(ctx, tx, wsID, email, newPassword); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "password reset for %s; all their sessions are revoked\n", email); err != nil {
		return fmt.Errorf("migrate reset-password: writing the confirmation: %w", err)
	}
	return nil
}

// singletonWorkspace resolves the one active organization — the same
// 0/1/>1 state machine every process role applies (A107/ADR-0061).
func singletonWorkspace(ctx context.Context, tx pgx.Tx) (ids.WorkspaceID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL LIMIT 2`)
	if err != nil {
		return ids.WorkspaceID{}, err
	}
	defer rows.Close()
	var found []ids.WorkspaceID
	for rows.Next() {
		var id ids.WorkspaceID
		if err := rows.Scan(&id); err != nil {
			return ids.WorkspaceID{}, err
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return ids.WorkspaceID{}, err
	}
	switch len(found) {
	case 0:
		return ids.WorkspaceID{}, errors.New("migrate reset-password: no active organization — bootstrap the installation first")
	case 1:
		return found[0], nil
	default:
		return ids.WorkspaceID{}, errors.New("migrate reset-password: more than one active workspace — resolve the single-organization invariant first")
	}
}

// readPassword takes the new password from stdin — hidden (no echo, no
// terminal recording) when stdin is a real terminal, plain reads for
// pipes and tests.
func readPassword(stdin io.Reader, stdout io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		raw, err := term.ReadPassword(int(f.Fd()))
		if err != nil {
			return "", fmt.Errorf("migrate reset-password: reading the new password: %w", err)
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return "", fmt.Errorf("migrate reset-password: writing the prompt newline: %w", err)
		}
		return string(raw), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("migrate reset-password: reading the new password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
