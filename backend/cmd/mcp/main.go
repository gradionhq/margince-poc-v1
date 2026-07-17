// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command mcp is the MCP process role (ADR-0054, amended §2), serving
// the ONE governed tool surface over two transports:
//
// A1 (default): MCP over stdio, authenticated by an Agent Seat
// Passport token from the environment (never a flag — argv is
// world-readable in `ps`). The agent client config points here:
//
//	{"command": "mcp",
//	 "env": {"MARGINCE_PASSPORT_TOKEN": "mgp_…", "MARGINCE_DSN": "…"}}
//
// One installation serves one organization (A107/ADR-0061): the process
// resolves the singleton itself and refuses to start pre-bootstrap — a
// passport can only be minted by an already-authenticated human, and
// before bootstrap no such human exists. There is no tenant selector.
//
// A2 (--listen): the hosted streamable-HTTP server. Tokens arrive as
// Bearer credentials minted by the /oauth flow (they ARE passport
// tokens), one JSON-RPC exchange per POST /mcp.
//
// Every call on either transport re-authenticates and re-loads the
// granting human's RBAC, so revocation and demotion bind mid-session.
// Protocol traffic owns stdout; diagnostics go to stderr.
package main

import (
	// Embedded tzdata: workspace timezones must resolve on scratch
	// containers that ship no zoneinfo.
	_ "time/tzdata"

	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
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
	listen := fs.String("listen", "", "serve the hosted A2 transport on this address instead of stdio")
	logLevel := fs.String("log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "diagnostic verbosity: debug|info|warn|error")
	logFormat := fs.String("log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "diagnostic encoding: text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("mcp: --dsn or MARGINCE_DSN required")
	}

	// Diagnostics go to stderr on BOTH transports: stdout is the stdio
	// protocol channel, and the hosted transport keeps the same habit.
	logger, err := newLogger(os.Stderr, *logLevel, *logFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	auth := identity.NewService(pool)
	registry := compose.NewRegistry(pool)

	// Bind the singleton organization before serving anything: an MCP
	// process against a pre-bootstrap database is an operator error, not
	// a wait state — no human exists yet who could have granted the
	// passport this transport authenticates with.
	wsID, err := auth.InstallationWorkspace(ctx)
	if errors.Is(err, identity.ErrNotBootstrapped) {
		return errors.New("mcp: the installation is not bootstrapped — start the API with a margince.yaml first")
	}
	if err != nil {
		return err
	}

	if *listen != "" {
		return serveHosted(ctx, *listen, auth, registry, wsID, logger)
	}

	token := os.Getenv("MARGINCE_PASSPORT_TOKEN")
	if token == "" {
		return errors.New("mcp: MARGINCE_PASSPORT_TOKEN is not set (mint one via POST /passports)")
	}

	bind := func(ctx context.Context) (context.Context, error) {
		ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
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

	logger.Info("mcp: serving over stdio", "tools", len(registry.Specs()), "workspace_id", wsID.String())
	return agents.NewStdioServer(registry, bind, "margince-crm", "0.1.0").
		WithLogger(logger).
		Serve(ctx, os.Stdin, stdout)
}

// envOr keeps the flag defaults env-backed without cluttering run.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newLogger builds the diagnostic logger the flags describe; the shared
// chassis builder keeps the level/format vocabulary (and the "a typo is a
// boot error" rule) identical across every process role.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	handler, err := httpserver.LogHandler(w, level, format)
	if err != nil {
		return nil, err
	}
	return slog.New(handler), nil
}

// serveHosted is the A2 transport: POST /mcp with a Bearer passport
// token (minted by the /oauth flow or POST /passports). The singleton
// organization was bound at boot — a request never selects a tenant
// (A107/ADR-0061).
func serveHosted(ctx context.Context, addr string, auth *identity.Service, registry *agents.Registry, wsID ids.WorkspaceID, logger *slog.Logger) error {
	authenticate := func(r *http.Request) (context.Context, error) {
		reqCtx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == "" || bearer == r.Header.Get("Authorization") {
			return nil, errors.New("missing bearer token")
		}
		agent, err := auth.AuthenticateAgent(reqCtx, bearer)
		if err != nil {
			return nil, err
		}
		reqCtx = principal.WithActor(reqCtx, agent.Principal())
		return principal.WithCorrelationID(reqCtx, ids.NewV7()), nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", agents.NewHTTPHandler(registry, authenticate, "margince-crm", "0.1.0"))

	server := &http.Server{
		Addr: addr,
		// One JSON-RPC exchange per POST, bodies capped by LimitBodies.
		Handler:           httpserver.LimitBodies(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A dynamic tool call can block on a model call, which
		// modules/ai budgets at 120s per request — the write timeout
		// must outlast that budget or the slowest legitimate call dies
		// mid-response.
		WriteTimeout: 150 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	logger.Info("mcp: hosted A2 transport up", "addr", addr, "tools", len(registry.Specs()))
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
