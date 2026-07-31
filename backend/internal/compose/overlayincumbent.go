// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Resolving the live incumbent behind an overlay workspace. It lives beside
// the server rather than inside it because three process roles need it and
// only one of them has a Server: the api resolves lazily through its
// (later-wired) vault, while the standalone MCP server and the worker's
// Surface-B runner each pass their own.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// resolveOverlayIncumbent builds the per-request live-incumbent resolver
// FreshnessReader's force-fresh lane reads through: for the request's
// workspace it reads the active incumbent_connection and unseals its
// private-app token, returning a live HubSpot adapter. It reads s.vault
// LAZILY (at request time), not at construction, because WithKeyvault
// installs the vault AFTER newServer builds the dispatch — so before a
// vault is wired, or on a role that never wires one, it returns a nil
// adapter and force-fresh degrades to the mirror honestly. A workspace
// with no active connection (ErrNotFound) or a non-HubSpot incumbent is
// the same honest nil degrade, not an error; only a genuine connection-read
// or vault failure surfaces as an error (which FreshnessReader logs and
// then degrades on, never faking authority).
func (s *Server) resolveOverlayIncumbent(pool *pgxpool.Pool) func(context.Context) (overlay.Incumbent, error) {
	// s.vault is read LAZILY (per call) because WithKeyvault installs it after
	// newServer builds the dispatch — so delegate to OverlayIncumbentResolver
	// at request time with whatever vault is then wired.
	return func(ctx context.Context) (overlay.Incumbent, error) {
		return OverlayIncumbentResolver(pool, s.vault)(ctx)
	}
}

// OverlayIncumbentResolver builds the per-request live-incumbent resolver from
// a KNOWN vault: for the request's workspace it reads the active
// incumbent_connection and unseals its private-app token, returning a live
// HubSpot adapter. A nil vault, no active connection (ErrNotFound), or a
// non-HubSpot incumbent all degrade honestly to a nil adapter (force-fresh
// falls back to the mirror; write-back answers errNoWriteIncumbent) — never a
// faked authority. Only a genuine connection-read or vault failure surfaces as
// an error. The api server passes its (lazily-wired) vault via
// resolveOverlayIncumbent; the standalone MCP server and the worker's Surface-B
// runner pass their own FromEnv vault so those agent surfaces reach write-back too.
func OverlayIncumbentResolver(pool *pgxpool.Pool, vault keyvault.Vault) func(context.Context) (overlay.Incumbent, error) {
	return func(ctx context.Context) (overlay.Incumbent, error) {
		if vault == nil {
			return nil, nil
		}
		conn, err := overlay.ActiveConnection(ctx, pool)
		if err != nil {
			if errors.Is(err, apperrors.ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if conn.Incumbent != incumbentHubSpot {
			return nil, nil
		}
		token, err := vault.Get(ctx, conn.Workspace, conn.CredentialRef)
		if err != nil {
			return nil, err
		}
		return hubspotIncumbentFactory(conn.Region, string(token)), nil
	}
}
