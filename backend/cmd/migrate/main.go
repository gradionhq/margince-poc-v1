// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command migrate is the schema-migration process role (ADR-0054,
// amended §2): applies the embedded core + custom namespaces (ADR-0017)
// with the owner-role DSN. Thin main, a testable run().
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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
		return errors.New("usage: migrate <up|down> --dsn <dsn> [--steps n]")
	}
	direction := args[0]

	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (owner role)")
	steps := fs.Int("steps", 1, "migrations to revert (down only)")
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
		// the same owner DSN. See decisions/0021-river-job-queue.md.
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
	default:
		return fmt.Errorf("migrate: unknown direction %q (want up or down)", direction)
	}
}
