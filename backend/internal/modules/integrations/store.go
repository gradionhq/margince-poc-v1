// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// objectIntegrations is the RBAC object every entry point below gates on.
const objectIntegrations = "integrations"

// Store owns the four platform tables. Every exported method gates before it
// reads or writes: the connection is installation-wide configuration that
// spends the customer's money, so there is no ungated path to it.
type Store struct {
	// db knows the installation's singleton workspace (ADR-0061/A107), which
	// is where the vault seals this connection's credential. The connection
	// row itself carries no workspace — see doc.go.
	db *database.DB
	// vault custodies the API key. The row holds only an opaque handle.
	vault keyvault.Vault
	// registry is the closed set of adapters this build knows.
	registry *Registry
	now      func() time.Time
	// deleteClaims is the owning domain's own delete, supplied by compose.
	// Nil when no domain is bound, which is also when no claims exist.
	deleteClaims DeleteClaimsFunc

	// The owning domain's callbacks (runs.go). Nil until compose binds them,
	// and QueueRun refuses rather than guessing at a subject's consent.
	fence       FenceSubjectFunc
	cluster     DuplicateClusterFunc
	identifiers SubjectIdentifiersFunc
	// enqueueSubmit commits the submit job with the run row.
	enqueueSubmit EnqueueSubmitFunc
	// writeClaims is the owning domain's claim upsert (handoff.go). Nil until
	// compose binds it; every hand-off then waits on the sweep and exhausts
	// into claims_unwritten, the honest record for a build with no domain.
	writeClaims WriteClaimsFunc
}

// DeleteClaimsFunc is the owning domain's delete of everything one provider
// asserted, run inside a transaction integrations already holds. It is a func
// type here rather than an interface in shared/ports/provider because that
// package is stdlib-only and cannot name a pgx.Tx — the same shape
// capture.EnqueueBackfill uses. Returns how many claims went.
type DeleteClaimsFunc func(ctx context.Context, tx pgx.Tx, provider string) (int64, error)

// WithClaimDeleter binds the owning domain's claim delete. Compose calls it;
// without it, DeleteProviderData still scrubs the run ledger it owns.
func (s *Store) WithClaimDeleter(fn DeleteClaimsFunc) *Store {
	s.deleteClaims = fn
	return s
}

// NewStore builds the store. vault and registry are required: a connection
// store that cannot seal a credential or name a provider would accept a key
// it then had nowhere to put.
func NewStore(db *database.DB, vault keyvault.Vault, reg *Registry, now func() time.Time) (*Store, error) {
	if db == nil || vault == nil || reg == nil || now == nil {
		return nil, errors.New("integrations: store needs a db, a vault, a registry and a clock")
	}
	return &Store{db: db, vault: vault, registry: reg, now: now}, nil
}

// Connection is one provider's connection as the surfaces read it. It carries
// no credential material and no vault reference — only whether a key is
// present at all.
type Connection struct {
	Provider          string
	Status            string
	CredentialPresent bool
	Mode              string
	Preset            string
	AutomaticCreate   bool
	AutomaticImport   bool
	Categories        []string
	RefreshAfterDays  *int
	DailyRunLimit     *int
	Budgets           []PoolBudget
	Version           int64
	SafeStatusCode    string
	ConnectedAt       *time.Time
	LastVerifiedAt    *time.Time
	LastUsedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// PoolBudget is one credit pool's ceiling, pause threshold and last known
// balance.
type PoolBudget struct {
	Pool              string
	MonthlyCeiling    *int
	PauseBelowBalance *int
	LastKnownBalance  *int
	BalanceReadAt     *time.Time
}

// List returns every registered provider's connection state, including the
// providers that have no row yet — a card that only appeared after connecting
// would give the admin nowhere to connect FROM.
func (s *Store) List(ctx context.Context) ([]Connection, error) {
	if err := auth.Require(ctx, objectIntegrations, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []Connection
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := s.loadConnections(ctx, tx)
		if err != nil {
			return err
		}
		for _, name := range s.registry.Names() {
			if c, ok := rows[name]; ok {
				out = append(out, c)
				continue
			}
			// Never connected: report the honest zero state rather than
			// omitting the provider entirely.
			d, err := s.registry.Descriptor(name)
			if err != nil {
				return err
			}
			out = append(out, Connection{
				Provider:   name,
				Status:     "disconnected",
				Mode:       string(defaultMode),
				Preset:     d.DefaultPreset,
				Categories: categoryStrings(d.ResolvePreset(d.DefaultPreset, nil)),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get reads one provider's connection.
func (s *Store) Get(ctx context.Context, name string) (Connection, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Connection{}, err
	}
	for _, c := range all {
		if c.Provider == name {
			return c, nil
		}
	}
	return Connection{}, apperrors.ErrNotFound
}

// loadConnections reads every connection row plus its budgets, keyed by
// provider. It does NOT gate: callers above have already done so.
func (s *Store) loadConnections(ctx context.Context, tx pgx.Tx) (map[string]Connection, error) {
	rows, err := tx.Query(ctx, `
		SELECT provider, status, credential_ref IS NOT NULL, mode, preset,
		       automatic_individual_create, automatic_import, categories,
		       refresh_after_days, daily_run_limit, version, last_safe_status_code,
		       connected_at, last_verified_at, last_used_at, created_at, updated_at
		  FROM provider_connection`)
	if err != nil {
		return nil, fmt.Errorf("integrations: reading connections: %w", err)
	}
	defer rows.Close()

	out := map[string]Connection{}
	for rows.Next() {
		var c Connection
		var safeCode *string
		if err := rows.Scan(&c.Provider, &c.Status, &c.CredentialPresent, &c.Mode, &c.Preset,
			&c.AutomaticCreate, &c.AutomaticImport, &c.Categories,
			&c.RefreshAfterDays, &c.DailyRunLimit, &c.Version, &safeCode,
			&c.ConnectedAt, &c.LastVerifiedAt, &c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("integrations: scanning a connection: %w", err)
		}
		if safeCode != nil {
			c.SafeStatusCode = *safeCode
		}
		out[c.Provider] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integrations: reading connections: %w", err)
	}
	return s.attachBudgets(ctx, tx, out)
}

func (s *Store) attachBudgets(ctx context.Context, tx pgx.Tx, conns map[string]Connection) (map[string]Connection, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.provider, b.pool, b.monthly_ceiling, b.pause_below_balance,
		       b.last_known_balance, b.balance_read_at
		  FROM provider_connection_budget b
		  JOIN provider_connection c ON c.id = b.connection_id
		 ORDER BY c.provider, b.pool`)
	if err != nil {
		return nil, fmt.Errorf("integrations: reading budgets: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var b PoolBudget
		if err := rows.Scan(&name, &b.Pool, &b.MonthlyCeiling, &b.PauseBelowBalance,
			&b.LastKnownBalance, &b.BalanceReadAt); err != nil {
			return nil, fmt.Errorf("integrations: scanning a budget: %w", err)
		}
		c := conns[name]
		c.Budgets = append(c.Budgets, b)
		conns[name] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integrations: reading budgets: %w", err)
	}
	return conns, nil
}

func categoryStrings(cats []provider.Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, string(c))
	}
	return out
}

func categoriesFrom(raw []string) []provider.Category {
	out := make([]provider.Category, 0, len(raw))
	for _, c := range raw {
		out = append(out, provider.Category(c))
	}
	return out
}

// emptyCredits is the "we did not read a balance" value for a policy update:
// patching a ceiling is not a reason to call the provider.
func emptyCredits() provider.Credits { return provider.Credits{} }
