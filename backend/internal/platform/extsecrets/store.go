// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package extsecrets is the extension tier's secret namespace: the one
// implementation of the published extension.Secrets port (ADR-0069).
//
// It sits between two things that each own half the problem and neither of
// which can own the other's. platform/keyvault is the custodian of secret
// MATERIAL: it seals bytes, hands back an opaque workspace-scoped Ref, and
// has no key/value namespace, no user scope, and no notion of an extension.
// An extension, on the other hand, addresses secrets by its own bare key
// names ("signing", "token") and must never see a Ref — a Ref is a
// capability, and one that reached extension code could be persisted
// somewhere the core cannot revoke. extension_secret is the mapping between
// the two, and this package is its only writer.
//
// The namespace wall is structural rather than checked: For closes over the
// invoking unit's name and every statement carries it, so there is no method
// on the port through which a unit could name another unit — reaching a
// sibling's namespace is not something the surface can express.
package extsecrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

var (
	// ErrUserOutsideWorkspace refuses a user-scoped operation naming somebody
	// who is not a member of the calling workspace. The composite FK on
	// extension_secret refuses such a row too, but as a constraint violation
	// nobody can read; the store asks first and answers by name.
	ErrUserOutsideWorkspace = errors.New("extsecrets: that user is not a member of this workspace")

	// ErrInvalidUserID refuses a UserID that is not a canonical UUID. The
	// published type is a string (the surface is stdlib-only), so this is
	// where its shape is actually established.
	ErrInvalidUserID = errors.New("extsecrets: the user id is not a canonical UUID")

	// ErrInvalidKey refuses a key name that could not be read back honestly:
	// empty, over the length bound, or carrying a control character. The key
	// is echoed into the system_log detail an operator reads, and a name with
	// an embedded newline has no honest rendering there.
	ErrInvalidKey = errors.New("extsecrets: the secret key name is unusable")

	// ErrNoCustodian refuses every operation on a deployment that configured
	// no keyvault. Failing by name beats a nil dereference, and beats writing
	// a mapping row pointing at material nothing could ever unseal.
	ErrNoCustodian = errors.New("extsecrets: no keyvault is configured for this installation, so no extension secret can be stored or read")
)

// maxKeyLength bounds a key name. The column is unbounded text and nothing
// breaks at 4KB, but a key is a NAME an operator reads in the audit ledger
// next to the unit that used it; anything longer is a payload wearing a
// name's clothes.
const maxKeyLength = 128

// store is one extension's view of the secret namespace, in whichever
// workspace the calling context is pinned to. unit is closed over at
// construction and is never a parameter: see the package doc.
type store struct {
	unit  string
	pool  *pgxpool.Pool
	vault keyvault.Vault
	log   *slog.Logger
}

var _ extension.Secrets = (*store)(nil)

// For builds the secrets port for one extension unit. The unit name is fixed
// here, at the one place that knows which unit is being invoked — the core —
// rather than anywhere the extension can reach.
//
// vault may be nil on a deployment that configured no custodian; every method
// then refuses with ErrNoCustodian rather than writing a mapping row naming
// material that does not exist.
//
// There is no logger parameter. The only thing this package logs is a
// detached vault cleanup that failed after its transaction committed — a
// condition the caller cannot act on and must not be failed for (see
// keyvault.DeleteDetached) — and threading a logger for that alone would put
// it in the signature of every Runtime constructed per tool call.
//
//nolint:ireturn // returning the published port IS the seam: callers hold extension.Secrets, never this type.
func For(unit string, pool *pgxpool.Pool, vault keyvault.Vault) extension.Secrets {
	return &store{unit: unit, pool: pool, vault: vault, log: slog.Default()}
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	return s.read(ctx, nil, key)
}

func (s *store) Put(ctx context.Context, key string, secret []byte) error {
	return s.write(ctx, nil, key, secret)
}

func (s *store) Delete(ctx context.Context, key string) error {
	return s.remove(ctx, nil, key)
}

func (s *store) GetUser(ctx context.Context, userID extension.UserID, key string) ([]byte, error) {
	user, err := parseUser(userID)
	if err != nil {
		return nil, err
	}
	return s.read(ctx, &user, key)
}

func (s *store) PutUser(ctx context.Context, userID extension.UserID, key string, secret []byte) error {
	user, err := parseUser(userID)
	if err != nil {
		return err
	}
	return s.write(ctx, &user, key, secret)
}

func (s *store) DeleteUser(ctx context.Context, userID extension.UserID, key string) error {
	user, err := parseUser(userID)
	if err != nil {
		return err
	}
	return s.remove(ctx, &user, key)
}

// read resolves the mapping row, then asks the custodian for the material. A
// nil user is the workspace scope.
//
// The audit row is written in the same transaction as the lookup, so a read
// that resolved a secret is recorded even if the custodian then fails — the
// fact an operator is looking for is that this unit ASKED for this key.
func (s *store) read(ctx context.Context, user *ids.UserID, key string) ([]byte, error) {
	ws, err := s.prepare(ctx, key)
	if err != nil {
		return nil, err
	}
	var ref keyvault.Ref
	if err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := requireMember(ctx, tx, user); err != nil {
			return err
		}
		found, err := s.refFor(ctx, tx, user, key, forShare)
		if err != nil {
			return err
		}
		ref = found
		return s.audit(ctx, tx, actionRead, user, key)
	}); err != nil {
		return nil, err
	}

	secret, err := s.vault.Get(ctx, ws, ref)
	if errors.Is(err, keyvault.ErrNotFound) {
		// The mapping row names material the custodian no longer holds — a
		// torn state rather than an ordinary absence, so it is logged. The
		// caller is still told what an absent key gets, because there is
		// nothing different it could do.
		s.log.ErrorContext(ctx, "extsecrets: a mapping row names a secret the custodian does not hold",
			"extension", s.unit, "key", key, "workspace", ws.String(), "vault_ref", string(ref))
		return nil, fmt.Errorf("extsecrets: %s/%s: %w", s.unit, key, extension.ErrSecretNotFound)
	}
	if err != nil {
		return nil, err
	}
	return secret, nil
}

// write seals the new material, re-points the mapping row at it, and destroys
// what the row named before.
//
// The order is forced. The row must never name material that is not durable
// yet, so the seal comes first (put-then-commit, the posture capture's
// connection stores already document). The destroy must never happen before
// the replacement is durable, so it comes after the commit — at which point
// the superseded blob is unreferenced by construction.
func (s *store) write(ctx context.Context, user *ids.UserID, key string, secret []byte) error {
	ws, err := s.prepare(ctx, key)
	if err != nil {
		return err
	}
	newRef, err := s.vault.Put(ctx, ws, secret)
	if err != nil {
		return err
	}

	var oldRef keyvault.Ref
	committing := false
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := requireMember(ctx, tx, user); err != nil {
			return err
		}
		existing, err := s.refFor(ctx, tx, user, key, forUpdate)
		switch {
		case err == nil:
			oldRef = existing
		case errors.Is(err, extension.ErrSecretNotFound):
			// First store under this key; nothing to supersede.
		default:
			return err
		}
		if err := s.upsert(ctx, tx, user, key, newRef); err != nil {
			return err
		}
		action := actionStored
		if oldRef != "" {
			action = actionRotated
		}
		if err := s.audit(ctx, tx, action, user, key); err != nil {
			return err
		}
		committing = true
		return nil
	})
	if err != nil {
		if !committing {
			// The closure failed, so the transaction definitely did not
			// commit and nothing names the material just sealed. An error
			// raised after the closure SUCCEEDED is a commit failure, whose
			// outcome is ambiguous — destroying then could strip a live
			// mapping row of its secret, so that blob is left orphaned
			// (inert, encrypted, unreferenced) instead.
			keyvault.DeleteDetached(ctx, s.vault, s.log, ws.UUID, newRef, "ext-secret-put-rolled-back")
		}
		return err
	}
	keyvault.DeleteDetached(ctx, s.vault, s.log, ws.UUID, oldRef, "ext-secret-rotated")
	return nil
}

// remove drops the mapping row and destroys the material it named. Deleting a
// key that holds nothing is ErrSecretNotFound rather than a silent success:
// the caller asked to revoke a specific credential, and "there was nothing
// there" is an answer it may well need to act on.
func (s *store) remove(ctx context.Context, user *ids.UserID, key string) error {
	ws, err := s.prepare(ctx, key)
	if err != nil {
		return err
	}
	scope := s.scopeOf(user, key)
	var ref keyvault.Ref
	if err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := requireMember(ctx, tx, user); err != nil {
			return err
		}
		var stored string
		switch err := tx.QueryRow(ctx,
			`DELETE FROM extension_secret `+whereScope+scope.predicate+` RETURNING vault_ref`,
			scope.args...).Scan(&stored); {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("extsecrets: %s/%s: %w", s.unit, key, extension.ErrSecretNotFound)
		case err != nil:
			return err
		}
		ref = keyvault.Ref(stored)
		return s.audit(ctx, tx, actionDeleted, user, key)
	}); err != nil {
		return err
	}
	keyvault.DeleteDetached(ctx, s.vault, s.log, ws.UUID, ref, "ext-secret-deleted")
	return nil
}

// prepare runs the checks every method shares and yields the workspace the
// custodian calls are scoped to.
func (s *store) prepare(ctx context.Context, key string) (ids.WorkspaceID, error) {
	if s.vault == nil {
		return ids.WorkspaceID{}, ErrNoCustodian
	}
	if err := validateKey(key); err != nil {
		return ids.WorkspaceID{}, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		// The same refusal WithWorkspaceTx would give, raised before a
		// connection is taken from the pool.
		return ids.WorkspaceID{}, database.ErrNoWorkspace
	}
	return ids.From[ids.WorkspaceKind](ws), nil
}

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
	// forShare is enough for a read: it keeps a concurrent rotation from
	// destroying the material between the lookup and the custodian call.
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

// upsert re-points (or creates) the mapping row. ON CONFLICT rather than the
// UPDATE the preceding read would suggest: that read cannot lock a row which
// does not exist yet, so two concurrent first-stores would both insert. The
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

// requireMember asks whether the named user belongs to the calling workspace;
// a nil user is the workspace scope and has nobody to check.
//
// The workspace predicate is explicit rather than left to app_user's RLS:
// this is the check that stops a cross-tenant attachment, and it should not
// be readable as correct only by knowing another table's policy.
func requireMember(ctx context.Context, tx pgx.Tx, user *ids.UserID) error {
	if user == nil {
		return nil
	}
	var member bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM app_user
			 WHERE id = $1
			   AND workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		)`, *user).Scan(&member); err != nil {
		return err
	}
	if !member {
		return fmt.Errorf("extsecrets: user %s: %w", user, ErrUserOutsideWorkspace)
	}
	return nil
}

// parseUser establishes the shape of the published UserID string.
func parseUser(userID extension.UserID) (ids.UserID, error) {
	user, err := ids.ParseAs[ids.UserKind](string(userID))
	if err != nil {
		return ids.UserID{}, fmt.Errorf("extsecrets: %q: %w", string(userID), ErrInvalidUserID)
	}
	if user.IsZero() {
		return ids.UserID{}, fmt.Errorf("extsecrets: the zero uuid names no user: %w", ErrInvalidUserID)
	}
	return user, nil
}

// validateKey holds the key-name rule. It is deliberately permissive about
// WHICH characters a unit uses — the key is the extension's own vocabulary
// and the store never builds an identifier out of it — and strict about the
// two things that would make the audit ledger lie: emptiness and control
// characters.
func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("extsecrets: the key name is empty: %w", ErrInvalidKey)
	}
	if len(key) > maxKeyLength {
		return fmt.Errorf("extsecrets: the key name is %d bytes, over the %d-byte bound: %w", len(key), maxKeyLength, ErrInvalidKey)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("extsecrets: the key name carries a control character: %w", ErrInvalidKey)
		}
	}
	return nil
}
