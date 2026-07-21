// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// keyvaultOptions wires the secret vault — its /readyz probe and the
// vault-backed connector-credential path — only when a root key is
// configured. Without one the vault stays absent: the transient one-shot IMAP
// pull (which persists no credential) still works, and the persisting paths
// (Connect/Sync) refuse loudly rather than nil-deref if ever invoked. A key
// that is set but malformed is a boot error (keyvault.FromEnv), never a silent
// fallback to something weaker.
func keyvaultOptions(pool *pgxpool.Pool, stdout io.Writer, overlayBackfillLimit int) ([]compose.Option, error) {
	vault, configured, err := keyvault.FromEnv(pool)
	if err != nil {
		return nil, fmt.Errorf("api: keyvault: %w", err)
	}
	if !configured {
		return nil, nil
	}
	_, _ = fmt.Fprintln(stdout, "api connector-credential vault enabled (keyvault configured)")
	// WithOverlayBackfillLimit must precede WithKeyvault: the latter builds
	// the overlay handlers off the backfill-limit field the former sets
	// (the same documented option-ordering WithKeyvault↔WithGmailCapture
	// already relies on).
	return []compose.Option{compose.WithOverlayBackfillLimit(overlayBackfillLimit), compose.WithKeyvault(vault)}, nil
}
