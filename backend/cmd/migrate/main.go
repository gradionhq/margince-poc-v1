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

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
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
		_, _ = fmt.Fprintf(stdout, "applied %d migration(s); schema is at head\n", applied)
		return nil
	case "down":
		// Down is namespace-scoped and deliberate: custom first (it sits
		// on top of core), and only --steps at a time.
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
