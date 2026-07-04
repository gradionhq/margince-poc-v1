// Command crm is the composition root (architecture/02): thin main, a
// testable run(), and explicit wiring. Adding a feature never edits this
// file — features self-register and the generated manifests import them.
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
		fmt.Fprintln(os.Stderr, "crm:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: crm <migrate|serve|mcp> [flags]")
	}
	switch args[0] {
	case "migrate":
		return runMigrate(ctx, args[1:], stdout)
	case "serve":
		return runServe(ctx, args[1:], stdout)
	case "mcp":
		return runMCP(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q (want migrate, serve or mcp)", args[0])
	}
}

func runMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: crm migrate <up|down> --dsn <dsn> [--steps n]")
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
