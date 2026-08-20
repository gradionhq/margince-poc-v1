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

// composedChannelsFor returns the transports the named unit declared in THIS
// boot's composition, and it is composedIngressFor's twin for the same reason:
// what a unit may name at a write door is what the boot reconciliation
// validated, never a second registry that could describe a unit that is not
// serving.
func composedChannelsFor(unit string) []extension.Channel {
	for _, ext := range ComposedExtensions() {
		if string(ext.Name) == unit {
			return ext.Channels
		}
	}
	return nil
}

// declaredUserSecretKeys are the user-scoped secret keys the named unit
// declares in THIS boot's composition.
//
// The consent check reads them rather than accepting any row in the unit's
// namespace, and the difference is a real one: extension_secret keeps a
// mapping row after a unit stops declaring the key it was deposited under, so
// a credential a member gave an earlier version of a unit would go on
// authorizing a capability the current manifest does not describe. What an
// operator can read has to be what the core acts on.
func declaredUserSecretKeys(unit string) []string {
	var keys []string
	for _, ext := range ComposedExtensions() {
		if string(ext.Name) != unit {
			continue
		}
		for _, request := range ext.Secrets {
			if request.Scope == extension.SecretScopeUser {
				keys = append(keys, request.Key)
			}
		}
	}
	return keys
}

// extensionMemberConsented reports whether the member currently holds a
// user-scoped secret under one of this unit's DECLARED keys.
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
// The read is the installation's: ADR-0091 §8 phase D took the tenant column
// off extension_secret and then off app_user, and one installation serves one
// organization (ADR-0061), so the deposit either exists or it does not.
func extensionMemberConsented(ctx context.Context, pool *pgxpool.Pool, unit string, member ids.UUID) (bool, error) {
	if pool == nil {
		return false, errExtensionRuntimeUnwired
	}
	declared := declaredUserSecretKeys(unit)
	if len(declared) == 0 {
		// A unit that declares no user-scoped secret has no way for a member to
		// consent to it at all, so there is nothing to read and nobody it may
		// act for.
		return false, nil
	}
	var consented bool
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		// No join to app_user. It carried the scope until ADR-0091 §8 phase D
		// took the tenant column off that table too, and extension_secret's
		// user_id is a foreign key onto app_user(id) ON DELETE CASCADE — so a
		// row that reaches this predicate cannot fail such a join, and one kept
		// here would read as a check while eliminating nothing.
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM extension_secret s
				WHERE s.extension_name = $1
				  AND s.user_id = $2
				  AND s.key = ANY($3)
			)`, unit, member, declared).Scan(&consented)
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
// ONE snapshot for both ceilings. Read separately, a role change and a seat
// change crossing between the two reads compose an authority the member never
// held — permissions from before with a seat from after — and capture would
// then admit a write against a pair that never existed. EffectiveAuthority
// reads both inside identity's own live-user transaction.
//
// The resolver is built from the pool at the call. That is the tree's own idiom
// for a dependency the pool fully determines, and it is why the runtime binding
// needs no entry for it.
func liveMemberAuthority(ctx context.Context, pool *pgxpool.Pool, workspace, member ids.UUID) (authz.RBAC, principal.SeatType, error) {
	if pool == nil {
		return authz.RBAC{}, "", errExtensionRuntimeUnwired
	}
	return identity.NewService(pool).EffectiveAuthority(ctx, workspace, member)
}
