// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// keyvaultOptions wires the secret vault — its /readyz probe and the
// vault-backed connector-credential path — only when a root key is
// configured. Without one the vault stays absent: every connector's connect
// path (gmail, gcal, graph, imap all seal to the vault) refuses loudly rather
// than nil-deref if ever invoked; the vault is required for any standing
// connection. A key that is set but malformed is a boot error
// (keyvault.FromEnv), never a silent fallback to something weaker.
func keyvaultOptions(ctx context.Context, pool *pgxpool.Pool, stdout io.Writer, overlayBackfillLimit int) ([]compose.Option, error) {
	vault, configured, err := keyvault.FromEnv(ctx, pool, config.FromOS)
	if err != nil {
		return nil, fmt.Errorf("api: keyvault: %w", err)
	}
	// Bound BEFORE the unconfigured return, and with whatever FromEnv gave:
	// the extension tier's per-call Runtime needs the POOL for its
	// workspace-pinned transactions whether or not a custodian exists, and a
	// deployment with no keyvault should have its extension secrets refuse by
	// name rather than have its extension database access silently disabled
	// too. FromEnv returns a nil vault when unconfigured, which is the
	// ErrNoCustodian posture the store already documents.
	compose.BindExtensionRuntime(pool, vault)
	if !configured {
		return nil, nil
	}
	_, _ = fmt.Fprintln(stdout, "api connector-credential vault enabled (keyvault configured)")
	// WithOverlayBackfillLimit must precede WithKeyvault: the latter builds
	// the overlay handlers off the backfill-limit field the former sets
	// (the same documented option-ordering WithKeyvault↔WithGmailCapture
	// already relies on).
	opts := []compose.Option{compose.WithOverlayBackfillLimit(overlayBackfillLimit), compose.WithKeyvault(vault)}
	provider, err := providerOption(pool, vault, stdout)
	if err != nil {
		return nil, err
	}
	return append(opts, provider...), nil
}

// providerOption wires the licensed-data-provider surface when an adapter is
// configured (MARGINCE_PROVIDER_SURFE). It needs the vault, so it lives here
// beside the option that supplies one and rides the same ordering: the store
// seals credentials through it and the run endpoints resolve them from it.
//
// The inserter is insert-only, which is what the api role holds: QueueRun
// commits its submit job in the run row's transaction and the worker role
// executes it.
func providerOption(pool *pgxpool.Pool, vault keyvault.Vault, stdout io.Writer) ([]compose.Option, error) {
	registry, configured, err := compose.ProviderRegistryFromEnv(time.Now, config.FromOS)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	if !configured {
		return nil, nil
	}
	inserter, err := jobs.NewInserter(pool, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("api: the provider submit inserter: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "api serving the provider surface (%s)\n", strings.Join(registry.Names(), ", "))
	return []compose.Option{compose.WithProvider(registry, vault, inserter)}, nil
}
