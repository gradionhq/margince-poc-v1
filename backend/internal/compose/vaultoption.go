// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// WithKeyvault wires the secret store: it feeds the /readyz probe and backs
// the capture connector-credential path (Authenticate seals the credential
// bundle, Sync resolves it). Without it a role that persists or resolves
// connector credentials declares that gap at wiring time rather than
// nil-derefing at Authenticate — a capture-capable role must pass this or
// fail to boot (enforced in cmd).
func WithKeyvault(vault keyvault.Vault) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.vault = vault
		// Rebuild the capture registry with the vault so the connector-
		// credential paths (Connect seals, Sync resolves) have their custodian.
		s.imapConnectHandlers = imapConnectHandlers{registry: NewCaptureRegistry(pool, vault)}
		// The standing IMAP connect rides the same registry and needs no
		// OAuth app; WithGmailCapture later replaces this with its own
		// gmail-carrying registry when the app is configured.
		if s.connectorHandlers.registry == nil {
			s.connectorHandlers = connectorHandlers{registry: NewCaptureRegistry(pool, vault)}
		}
	}
}
