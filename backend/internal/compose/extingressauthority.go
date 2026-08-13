// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two facts an ingest establishes about the member it runs as, and the
// declaration lookup that decides whether the unit may ingest at all.
//
// They live beside the port rather than inside it because each is a question
// with one answer the whole tier shares: has this member asked THIS unit to act
// for them, and what may they do right now. Neither is the unit's to assert.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// composedIngressFor returns the ingress sources the named unit declared in
// THIS boot's composition.
//
// It reads the composed declarations rather than a second registry, for the
// reason ComposedExtensions exists: the set the boot reconciliation actually
// validated is the only honest answer to "what may this unit do", and a
// parallel copy could describe a unit that is not serving.
func composedIngressFor(unit string) []extension.IngressSource {
	for _, ext := range ComposedExtensions() {
		if string(ext.Name) == unit {
			return ext.Ingress
		}
	}
	return nil
}

// extensionMemberConsented reports whether the member currently holds any
// user-scoped secret in this unit's namespace.
//
// It also subsumes the refusal of the installation's own agent-runner seat,
// which is why no separate check for it exists: that seat has no password and
// no session, so it can never have deposited a credential through the screen
// that deposits them. A check that can only ever agree with this one would be
// two spellings of a single fact, which is how the two drift apart later.
//
// DEPOSITING A CREDENTIAL IS THE CONSENT ACT. A member who pastes their
// provider token into a unit's screen is saying "poll this account for me", and
// that is the fact an ingest needs before it may act as them — without it a unit
// could name any colleague and land records on their authority, which is the
// confused deputy this check exists to close.
//
// It reads the MAPPING ROW only, never the material: the question is whether a
// credential is on deposit, and unsealing one to answer it would spend the
// custodian and hand this path a secret it has no use for.
//
// Runs on a workspace-bound transaction, so the tenant policy on
// extension_secret answers for the scope rather than a predicate written here.
func extensionMemberConsented(ctx context.Context, pool *pgxpool.Pool, unit string, member ids.UUID) (bool, error) {
	if pool == nil {
		return false, errExtensionRuntimeUnwired
	}
	var consented bool
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM extension_secret
				WHERE extension_name = $1 AND user_id = $2
			)`, unit, member).Scan(&consented)
	})
	if err != nil {
		return false, fmt.Errorf("compose: reading whether a member has deposited a credential with %s: %w", unit, err)
	}
	return consented, nil
}

// liveMemberAuthority resolves what the member may do RIGHT NOW.
//
// Resolved per call rather than carried on the connection, which is what makes
// a demotion take effect immediately: a member whose grants narrowed since they
// connected lands less from the next record onward, and one who is archived,
// suspended or gone lands nothing — identity's resolver answers ErrNotFound for
// all three rather than an empty-but-valid authority, so the grant dies with
// them exactly as a connector's does.
//
// The resolver is built from the pool at the call. That is the tree's own idiom
// for a dependency the pool fully determines, and it is why the runtime binding
// needs no entry for it.
func liveMemberAuthority(ctx context.Context, pool *pgxpool.Pool, workspace, member ids.UUID) (authz.RBAC, principal.SeatType, error) {
	if pool == nil {
		return authz.RBAC{}, "", errExtensionRuntimeUnwired
	}
	resolver := identity.NewService(pool)
	rbac, err := resolver.EffectiveRBAC(ctx, workspace, member)
	if err != nil {
		return authz.RBAC{}, "", err
	}
	seat, err := resolver.SeatType(ctx, workspace, member)
	if err != nil {
		return authz.RBAC{}, "", err
	}
	return rbac, seat, nil
}
