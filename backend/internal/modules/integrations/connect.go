// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// defaultMode is what an omitted configuration resolves to: enrich a person
// as they are created. It is the mode a customer who connected a provider
// almost certainly wanted, and PI-PARAM-2 pins it.
const defaultMode = provider.Trigger("automatic_on_create")

// ErrVerificationFailed reports that the provider refused the credential. The
// key is NOT stored when this happens — nothing about a rejected key reaches
// the database (PI-AC-1).
var ErrVerificationFailed = errors.New("integrations: the provider rejected that credential")

// ConnectInput is a connect or a key rotation.
type ConnectInput struct {
	Provider string
	APIKey   string
	// Config is optional. Omitted, it resolves to automatic-on-create with
	// the provider's default preset.
	Config *ConfigInput
}

// ConfigInput is the saved policy a human can set.
type ConfigInput struct {
	Mode             string
	Preset           string
	Categories       []string
	AutomaticCreate  *bool
	AutomaticImport  *bool
	RefreshAfterDays *int
	DailyRunLimit    *int
	Budgets          []PoolBudget
}

// Connect verifies a credential against the provider, seals it, and commits
// the connection — in that order, because a key that does not work must leave
// no trace. The verification call is the one provider call allowed on an HTTP
// request path, and it is what PI-AC-1 requires before any commit.
func (s *Store) Connect(ctx context.Context, in ConnectInput) (Connection, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return Connection{}, err
	}
	if err := auth.Require(ctx, objectIntegrations, principal.ActionCreate); err != nil {
		return Connection{}, err
	}
	if in.APIKey == "" {
		return Connection{}, &MissingAPIKeyError{}
	}

	adapter, err := s.registry.Adapter(in.Provider)
	if err != nil {
		return Connection{}, apperrors.ErrNotFound
	}
	desc := adapter.Descriptor()

	cfg, err := resolveConfig(desc, in.Config)
	if err != nil {
		return Connection{}, err
	}

	// Verify BEFORE anything is written. A rejected key never becomes a row,
	// never becomes a vault entry, and never appears in an audit image.
	credits, err := adapter.VerifyCredential(ctx, provider.Credential(in.APIKey))
	if err != nil {
		// The provider's own error text is deliberately dropped: it may echo
		// a fragment of the key we just sent it.
		return Connection{}, &VerificationFailedError{Provider: desc.Name, Call: desc.Verification}
	}

	ws, err := s.db.Workspace(ctx)
	if err != nil {
		return Connection{}, fmt.Errorf("integrations: resolving the installation workspace: %w", err)
	}
	ref, err := s.vault.Put(ctx, ws, []byte(in.APIKey))
	if err != nil {
		return Connection{}, fmt.Errorf("integrations: sealing the credential: %w", err)
	}

	var out Connection
	var displaced *string
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", in.Provider); err != nil {
			return err
		}
		old, err := s.replaceConnection(ctx, tx, in.Provider, ref, cfg)
		if err != nil {
			return err
		}
		displaced = old.displacedRef
		if err := s.writeBudgets(ctx, tx, in.Provider, cfg.Budgets, credits); err != nil {
			return err
		}
		// The audit image names the provider and the policy; it never carries
		// the key, the vault handle, or a balance.
		if _, err := storekit.Audit(ctx, tx, "connect", "provider_connection", uuidOf(old.id),
			nil, map[string]any{auditKeyProvider: in.Provider, auditKeyMode: cfg.Mode, auditKeyPreset: cfg.Preset}); err != nil {
			return err
		}
		conns, err := s.loadConnections(ctx, tx)
		if err != nil {
			return err
		}
		out = conns[in.Provider]
		return nil
	})
	if err != nil {
		// The row did not commit, so the sealed secret has nothing pointing
		// at it: destroy it rather than leaving an orphan in the vault.
		if delErr := s.vault.Delete(ctx, ws, ref); delErr != nil {
			return Connection{}, errors.Join(err, fmt.Errorf("integrations: abandoning the sealed credential: %w", delErr))
		}
		return Connection{}, err
	}

	// A rotation just replaced a key. Destroy the one it displaced, AFTER the
	// commit: doing it inside the transaction would destroy a secret the row
	// still names if the commit then failed. The vault delete is not
	// transactional, so the order is the only guarantee available.
	if displaced != nil {
		if err := s.vault.Delete(ctx, ws, keyvault.Ref(*displaced)); err != nil {
			return out, fmt.Errorf("integrations: destroying the rotated-out credential: %w", err)
		}
	}
	return out, nil
}

// Disconnect stops new egress, cancels work that never reached the provider,
// and destroys the key. Data already retrieved stays until retention, erasure
// or the separate delete-data action: disconnecting is not deleting.
func (s *Store) Disconnect(ctx context.Context, name string) error {
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := auth.Require(ctx, objectIntegrations, principal.ActionDelete); err != nil {
		return err
	}
	if _, err := s.registry.Adapter(name); err != nil {
		return apperrors.ErrNotFound
	}

	ws, err := s.db.Workspace(ctx)
	if err != nil {
		return fmt.Errorf("integrations: resolving the installation workspace: %w", err)
	}

	var ref keyvault.Ref
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
			return err
		}
		var id *string
		var stored *string
		if err := tx.QueryRow(ctx, `
			SELECT id::text, credential_ref FROM provider_connection WHERE provider = $1`,
			name).Scan(&id, &stored); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Idempotent: disconnecting what was never connected is a
				// no-op, not an error.
				return nil
			}
			return fmt.Errorf("integrations: reading the connection: %w", err)
		}
		if stored != nil {
			ref = keyvault.Ref(*stored)
		}

		// Bumping the epoch is what makes the cancel below honest. A worker
		// already past its own admission check would otherwise still call the
		// provider with a credential we are about to destroy; it re-reads
		// this number immediately before egress and abandons.
		if _, err := tx.Exec(ctx, `
			UPDATE provider_connection
			   SET status = 'disconnected', credential_ref = NULL,
			       execution_epoch = execution_epoch + 1
			 WHERE provider = $1`, name); err != nil {
			return fmt.Errorf("integrations: disconnecting: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run SET state = 'cancelled', completed_at = now()
			 WHERE provider = $1 AND state = 'queued'`, name); err != nil {
			return fmt.Errorf("integrations: cancelling queued runs: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "disconnect", "provider_connection", uuidOf(id),
			map[string]any{auditKeyProvider: name}, nil); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// After the commit, because a vault delete is not transactional: if it
	// failed inside the transaction we would either roll back a disconnect
	// the customer asked for, or destroy a key the row still names.
	if ref != "" {
		if err := s.vault.Delete(ctx, ws, ref); err != nil {
			return fmt.Errorf("integrations: destroying the sealed credential: %w", err)
		}
	}
	return nil
}
