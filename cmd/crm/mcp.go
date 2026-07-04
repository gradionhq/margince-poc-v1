package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	crmagents "github.com/gradionhq/fable-poc/crm-agents"
	crmapprovals "github.com/gradionhq/fable-poc/crm-approvals"
	crmauth "github.com/gradionhq/fable-poc/crm-auth"
	crmcore "github.com/gradionhq/fable-poc/crm-core"
	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/internal/pg"
	"github.com/gradionhq/fable-poc/kernel/ids"
)

// runMCP boots the A1 local MCP server: MCP over stdio, authenticated by
// an Agent Seat Passport token from the environment (never a flag — argv
// is world-readable in `ps`). The agent client config points here:
//
//	{"command": "crm", "args": ["mcp", "--workspace", "acme"],
//	 "env": {"MARGINCE_PASSPORT_TOKEN": "mgp_…", "MARGINCE_DSN": "…"}}
//
// Every tools/call re-authenticates the token and re-loads the granting
// human's RBAC, so revocation and demotion bind mid-session. Protocol
// traffic owns stdout; diagnostics go to stderr.
func runMCP(ctx context.Context, args []string, stdout io.Writer) error {
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

	pool, err := pg.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	auth := crmauth.NewService(pool)
	provider := crmcore.NewProvider(pool)

	registry := crmagents.NewRegistry(approvalsAdapter{svc: crmapprovals.NewService(pool)})
	crmagents.RegisterCoreTools(registry, provider, provider, provider)

	bind := func(ctx context.Context) (context.Context, error) {
		wsID, err := auth.ResolveWorkspace(ctx, *workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving workspace %q: %w", *workspace, err)
		}
		ctx = crmctx.WithWorkspaceID(ctx, wsID)
		agent, err := auth.AuthenticateAgent(ctx, token)
		if err != nil {
			return nil, err
		}
		ctx = crmctx.WithActor(ctx, agent.Principal())
		return crmctx.WithCorrelationID(ctx, ids.NewV7()), nil
	}

	// Fail loudly at boot on a dead token instead of on the first call —
	// an agent client shows boot errors, but may bury call errors.
	if _, err := bind(ctx); err != nil {
		return fmt.Errorf("mcp: passport check failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "crm mcp: serving %d tools for workspace %q over stdio\n",
		len(registry.Specs()), *workspace)
	return crmagents.NewStdioServer(registry, bind, "margince-crm", "0.1.0").
		Serve(ctx, os.Stdin, stdout)
}

// approvalsAdapter maps the tool surface's staging/redemption dependency
// onto the approvals module — composed here so crm-agents never imports a
// sibling module.
type approvalsAdapter struct{ svc *crmapprovals.Service }

func (a approvalsAdapter) Stage(ctx context.Context, in crmagents.StageRequest) (ids.UUID, error) {
	return a.svc.Stage(ctx, crmapprovals.StageInput{
		Kind:           in.Tool,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		TargetVersion:  in.TargetVersion,
		Summary:        in.Summary,
	})
}

func (a approvalsAdapter) Redeem(ctx context.Context, approvalID ids.UUID, tool, diffHash string) error {
	return a.svc.Redeem(ctx, approvalID, tool, diffHash)
}
