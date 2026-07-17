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
		return errors.New("usage: migrate <up|down|reset-password> --dsn <dsn> [--steps n] [--email <address>]")
	}
	direction := args[0]

	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (owner role)")
	steps := fs.Int("steps", 1, "migrations to revert (down only)")
	email := fs.String("email", "", "user email (reset-password only)")
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
		applied, err := dbmigrate.Up(ctx, conn, core, custom)
		if err != nil {
			return err
		}
		// River owns its schema through its own migrator, applied as the
		// fourth namespace after core+custom (ADR-0017 order). Its migrator
		// wants a pool, not the single conn the SQL runner uses; open one on
		// the same owner DSN.
		riverPool, err := database.NewPool(ctx, *dsn)
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
	default:
		return fmt.Errorf("migrate: unknown direction %q (want up, down or reset-password)", direction)
	}
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
	_, _ = fmt.Fprint(stdout, "new password (min 12 chars): ")
	newPassword, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("migrate reset-password: reading the new password: %w", err)
	}
	newPassword = strings.TrimRight(newPassword, "\r\n")

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is checked, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()

	// Bind the installation's singleton organization (FORCE RLS applies
	// to the owner role too). More than one active workspace is the same
	// operator-led-migration refusal every process role gives.
	var wsID ids.WorkspaceID
	rows, err := tx.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL LIMIT 2`)
	if err != nil {
		return err
	}
	var found []ids.WorkspaceID
	for rows.Next() {
		var id ids.WorkspaceID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		found = append(found, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	switch len(found) {
	case 0:
		return errors.New("migrate reset-password: no active organization — bootstrap the installation first")
	case 1:
		wsID = found[0]
	default:
		return errors.New("migrate reset-password: more than one active workspace — resolve the single-organization invariant first")
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
	_, _ = fmt.Fprintf(stdout, "password reset for %s; all their sessions are revoked\n", email)
	return nil
}
