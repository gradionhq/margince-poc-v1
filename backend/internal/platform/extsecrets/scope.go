// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extsecrets

// How a statement addresses rows: the shared WHERE prefix, the two scope
// predicates that complete it, the locks that make a read-then-write safe,
// and the two statements every operation is built from. store.go holds the
// port's operations; this file holds the SQL those operations are made of.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// whereScope is the prefix every row-addressing statement shares: this unit,
// this tenant. The workspace predicate duplicates what RLS already enforces —
// deliberately, because it is also what lets the planner reach the partial
// unique indexes, which are keyed (extension_name, workspace_id, …).
const whereScope = `
	WHERE extension_name = $1
	  AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
	  AND key = $2
	  `

// scoped is one operation's row scope: the predicate that completes
// whereScope, and the arguments it is issued with.
type scoped struct {
	predicate string
	args      []any
}

// scopeOf spells the two scopes as two predicates rather than one
// `IS NOT DISTINCT FROM` covering both: the partial unique indexes are
// defined WHERE user_id IS NULL and WHERE user_id IS NOT NULL, and only a
// predicate of the same shape lets Postgres prove a row is in one of them.
func (s *store) scopeOf(user *ids.UserID, key string) scoped {
	if user == nil {
		return scoped{predicate: `AND user_id IS NULL`, args: []any{s.unit, key}}
	}
	return scoped{predicate: `AND user_id = $3`, args: []any{s.unit, key, *user}}
}

// lockMode is how refFor holds the row it reads.
type lockMode string

const (
	// forShare is enough for a read, and is what lets the custodian call stay
	// meaningful: a concurrent rotation's FOR UPDATE waits on it, so no
	// rotation can commit — and so none can destroy this ref — before the
	// reading transaction ends. That only holds because the custodian call
	// happens INSIDE that transaction; see read.
	forShare lockMode = " FOR SHARE"
	// forUpdate serializes two rotations of the same key, so the loser
	// supersedes the winner's ref rather than both superseding the original
	// and one blob leaking.
	forUpdate lockMode = " FOR UPDATE"
)

// refFor resolves the mapping row for this unit, in the calling workspace, at
// the given scope. The lock clause is a constant of this package, never
// caller input.
func (s *store) refFor(ctx context.Context, tx pgx.Tx, user *ids.UserID, key string, lock lockMode) (keyvault.Ref, error) {
	scope := s.scopeOf(user, key)
	var ref string
	switch err := tx.QueryRow(ctx,
		`SELECT vault_ref FROM extension_secret `+whereScope+scope.predicate+string(lock),
		scope.args...).Scan(&ref); {
	case errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("extsecrets: %s/%s: %w", s.unit, key, extension.ErrSecretNotFound)
	case err != nil:
		return "", err
	}
	return keyvault.Ref(ref), nil
}

// lockKey serializes the read-then-write of ONE key's mapping row for the
// rest of the transaction.
//
// A row lock cannot do this job. FOR UPDATE locks a row that exists; on a
// first store there is none, so two concurrent first-stores would each read
// nothing, each seal their own material, and each insert. One wins, the
// loser's ON CONFLICT overwrites vault_ref — and the winner's blob is
// orphaned with nothing left naming it, because the loser saw no previous
// ref to supersede. Narrow and inert, but it is a leak of exactly the kind
// this store exists to prevent, so it is closed rather than documented.
//
// An advisory lock covers the absent row because it is keyed on the NAME,
// not on a tuple. Under READ COMMITTED the loser takes a fresh snapshot for
// its next statement, so once it acquires the lock its own lookup sees the
// winner's committed row and supersedes it correctly. Same idiom, same
// reason, as the boot inventory's check-and-insert guard
// (compose/extensioninventory.go).
//
// It is taken on the write path only. A concurrent remove destroys the ref
// it removes, so no interleaving with it can strand a blob: either this
// lookup runs first and the delete then waits on the row lock, or the row is
// already gone and this becomes a first store.
//
// hashtext collapses the key into the bigint the advisory-lock API takes.
// Collisions serialize two unrelated keys for the length of a transaction,
// which costs a wait and changes no outcome.
func (s *store) lockKey(ctx context.Context, tx pgx.Tx, user *ids.UserID, key string) error {
	scope := scopeWorkspace
	holder := ""
	if user != nil {
		scope = scopeUser
		holder = user.String()
	}
	_, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtext(
			'margince:extsecrets:' || current_setting('app.workspace_id', true) ||
			':' || $1 || ':' || $2 || ':' || $3 || ':' || $4)::bigint)`,
		s.unit, scope, holder, key)
	return err
}

// upsert re-points (or creates) the mapping row. ON CONFLICT rather than the
// UPDATE the preceding read would suggest: that read cannot lock a row which
// does not exist yet. lockKey above is what actually makes the read-then-write
// atomic; ON CONFLICT stays as the structural backstop, so a future caller
// that reaches upsert without the lock loses a race rather than a row. The
// conflict target repeats the partial index's predicate, which is how
// Postgres infers a partial unique index.
func (s *store) upsert(ctx context.Context, tx pgx.Tx, user *ids.UserID, key string, ref keyvault.Ref) error {
	const workspaceScoped = `
		INSERT INTO extension_secret (extension_name, workspace_id, user_id, key, vault_ref)
		VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, NULL, $2, $3)
		ON CONFLICT (extension_name, workspace_id, key) WHERE user_id IS NULL
		DO UPDATE SET vault_ref = EXCLUDED.vault_ref, updated_at = now()`
	const userScoped = `
		INSERT INTO extension_secret (extension_name, workspace_id, user_id, key, vault_ref)
		VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, $3, $4)
		ON CONFLICT (extension_name, workspace_id, user_id, key) WHERE user_id IS NOT NULL
		DO UPDATE SET vault_ref = EXCLUDED.vault_ref, updated_at = now()`

	var err error
	if user == nil {
		_, err = tx.Exec(ctx, workspaceScoped, s.unit, key, string(ref))
	} else {
		_, err = tx.Exec(ctx, userScoped, s.unit, *user, key, string(ref))
	}
	return err
}
