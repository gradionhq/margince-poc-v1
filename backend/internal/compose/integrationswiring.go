// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The provider-platform composition (ADR-0101): one place builds the
// integrations store with its cross-module edges, for the HTTP surface and
// the job workers alike, so the two roles can never disagree about what is
// bound.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WithProvider wires the licensed-data-provider surface: the connection
// lifecycle, and the run endpoints backed by the same store. Applied AFTER
// WithKeyvault (cmd orders them; the vault parameter makes the dependency
// explicit rather than read off the Server). The inserter is the api role's
// insert-only jobs.Runner — QueueRun commits its submit job through it in the
// same transaction as the run row.
func WithProvider(reg *integrations.Registry, vault keyvault.Vault, inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		store, err := integrations.NewStore(InstallationDB(pool), vault, reg, time.Now)
		if err != nil {
			panic("compose: integrations store construction failed with live dependencies: " + err.Error())
		}
		store = bindProviderDomain(store).WithSubmitEnqueue(providerSubmitEnqueue(inserter))
		s.integrationsHandlers = integrationsHandlers{store: store, runs: store}
		// The person page reads the provider's category vocabulary to say
		// what a run did NOT ask for. Bound here rather than at construction
		// because the registry arrives with this option.
		if s.person360Svc != nil {
			s.person360Svc.WithProviders(reg)
		}
	}
}

// NewProviderRunService builds the run service a role uses OUTSIDE the HTTP
// surface — the worker's event consumer, which queues on the customer's
// standing instruction rather than on a request. Same construction as
// WithProvider so the two roles cannot disagree about what is bound; it
// returns an error rather than panicking because a lane may legitimately
// decline to start where the option must not.
func NewProviderRunService(pool *pgxpool.Pool, reg *integrations.Registry, vault keyvault.Vault, inserter *jobs.Runner, now func() time.Time) (*integrations.Store, error) {
	store, err := integrations.NewStore(InstallationDB(pool), vault, reg, now)
	if err != nil {
		return nil, fmt.Errorf("compose: building the provider run service: %w", err)
	}
	return bindProviderDomain(store).WithSubmitEnqueue(providerSubmitEnqueue(inserter)), nil
}

// providerSubmitEnqueue closes over the role's inserter: the submit job and
// the run row commit together or not at all. The uniqueness policy rides the
// args type (ProviderRunSubmitArgs.InsertOpts), so nil opts here drops
// nothing.
func providerSubmitEnqueue(inserter *jobs.Runner) integrations.EnqueueSubmitFunc {
	return func(ctx context.Context, tx pgx.Tx, runID, workspaceID string) error {
		ws, err := ids.Parse(workspaceID)
		if err != nil {
			return fmt.Errorf("compose: the submit job's workspace id does not parse: %w", err)
		}
		return inserter.EnqueueTx(ctx, tx, ProviderRunSubmitArgs{Workspace: ws, RunID: runID}, nil)
	}
}

// BindProviderDomain is bindProviderDomain for a caller outside this package
// — the integration suite, which must exercise the REAL cross-module binding
// rather than a hand-assembled copy of it. A test that wired its own
// callbacks would prove the assembly it wrote, not the one production ships.
func BindProviderDomain(store *integrations.Store) *integrations.Store {
	return bindProviderDomain(store)
}

// bindProviderDomain attaches the owning domain's callbacks: people decides
// whether a subject may be enriched, which records might be the same human,
// what may leave the installation about them, and where the bought values
// land. THIS is the cross-module edge — integrations may not import people,
// so compose injects it, and it is injected in exactly one place so the api
// role and the worker role can never disagree about what is bound.
func bindProviderDomain(store *integrations.Store) *integrations.Store {
	return store.
		WithDomain(providerFence, people.DuplicateCluster, people.SubjectIdentifiers).
		WithClaimWriter(providerClaimWriter).
		WithClaimDeleter(providerClaimDeleter)
}

// providerFence adapts people's verdict to the shape integrations declares.
// The two refusals stay distinct across the seam: a suppressed subject
// objected, an ineligible one is a record we should not be buying about.
func providerFence(ctx context.Context, tx pgx.Tx, personID string) (integrations.FenceVerdict, error) {
	allowed, reason, err := people.EnrichmentFence(ctx, tx, personID)
	if err != nil {
		return integrations.FenceVerdict{}, err
	}
	return integrations.FenceVerdict{Allowed: allowed, Reason: reason}, nil
}

// providerClaimWriter lands one run's values in the table people owns.
func providerClaimWriter(ctx context.Context, tx pgx.Tx, w integrations.ClaimWrite) error {
	return people.WriteProviderClaims(ctx, tx, w.RunID, w.PersonID, w.Provider, w.Claims, w.RetrievedAt)
}

// providerClaimDeleter is the domain half of the delete-data action.
func providerClaimDeleter(ctx context.Context, tx pgx.Tx, providerName string) (int64, error) {
	return people.DeleteProviderClaims(ctx, tx, providerName)
}
