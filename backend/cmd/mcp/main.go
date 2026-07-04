// Command mcp is the A1 local MCP process role (ADR-0054, amended §2):
// MCP over stdio, authenticated by an Agent Seat Passport token from the
// environment (never a flag — argv is world-readable in `ps`). The agent
// client config points here:
//
//	{"command": "mcp", "args": ["--workspace", "acme"],
//	 "env": {"MARGINCE_PASSPORT_TOKEN": "mgp_…", "MARGINCE_DSN": "…"}}
//
// Every tools/call re-authenticates the token and re-loads the granting
// human's RBAC, so revocation and demotion bind mid-session. Protocol
// traffic owns stdout; diagnostics go to stderr.
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

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	workspace := fs.String("workspace", os.Getenv("MARGINCE_WORKSPACE"), "workspace slug the passport belongs to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("mcp: --dsn or MARGINCE_DSN required")
	}
	if *workspace == "" {
		return errors.New("mcp: --workspace or MARGINCE_WORKSPACE required")
	}
	token := os.Getenv("MARGINCE_PASSPORT_TOKEN")
	if token == "" {
		return errors.New("mcp: MARGINCE_PASSPORT_TOKEN is not set (mint one via POST /passports)")
	}

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	auth := identity.NewService(pool)
	registry := compose.NewRegistry(pool)

	bind := func(ctx context.Context) (context.Context, error) {
		wsID, err := auth.ResolveWorkspace(ctx, *workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving workspace %q: %w", *workspace, err)
		}
		ctx = principal.WithWorkspaceID(ctx, wsID)
		agent, err := auth.AuthenticateAgent(ctx, token)
		if err != nil {
			return nil, err
		}
		ctx = principal.WithActor(ctx, agent.Principal())
		return principal.WithCorrelationID(ctx, ids.NewV7()), nil
	}

	// Fail loudly at boot on a dead token instead of on the first call —
	// an agent client shows boot errors, but may bury call errors.
	if _, err := bind(ctx); err != nil {
		return fmt.Errorf("mcp: passport check failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "mcp: serving %d tools for workspace %q over stdio\n",
		len(registry.Specs()), *workspace)
	return agents.NewStdioServer(registry, bind, "margince-crm", "0.1.0").
		Serve(ctx, os.Stdin, stdout)
}
