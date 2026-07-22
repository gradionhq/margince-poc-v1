// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command migrate is the schema-migration process role (ADR-0054,
// amended §2): applies the embedded core + custom namespaces (ADR-0017)
// with the owner-role DSN. Thin main, a testable run().
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
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/migrations"
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
		return up(ctx, conn, *dsn, core, custom, stdout)
	case "down":
		// Down reverts the SQL namespaces only — custom first (it sits on top
		// of core), --steps at a time. River's schema is infrastructure with
		// its own migrator; rolling it back is a separate deliberate step, not
		// folded into this counter (a plain `down` must never surprise the
		// operator by dropping a River migration).
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
		_, _ = fmt.Fprintf(stdout, "reverted %d migration(s)\n", reverted)
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

// up applies the embedded SQL namespaces, then River's schema. River owns
// its schema through its own migrator, applied as the fourth namespace after
// core+custom (ADR-0017 order); its migrator wants a pool, not the single
// conn the SQL runner uses, so one is opened on the same owner DSN.
func up(ctx context.Context, conn *pgx.Conn, dsn string, core, custom dbmigrate.Namespace, stdout io.Writer) error {
	applied, err := dbmigrate.Up(ctx, conn, core, custom)
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
	_, _ = fmt.Fprintf(stdout, "applied %d core+custom + %d river migration(s); schema is at head\n", applied, riverApplied)
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
