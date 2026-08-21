// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EnvRootKey names the environment variable carrying the base64 (standard
// encoding) 32-byte AES-256 root key. It is read from the environment, never
// a CLI flag — a flag leaks into the process table. The value never reaches
// a log line or an error message.
const EnvRootKey = "MARGINCE_KEYVAULT_ROOT_KEY"

// Config is the local provider's wiring, populated from operator config in
// cmd. RootKey is the workspace-agnostic master key that seals every secret;
// Pool is the shared pgxpool the vault_secret ciphertext table lives in.
// Neither the key nor any plaintext is ever logged.
type Config struct {
	RootKey []byte
	Pool    *pgxpool.Pool
}

// localVault is the config/local-backed Vault: it seals secrets with
// AES-256-GCM under a config root key and stores the ciphertext in the
// operational vault_secret table. The table carries NO workspace_id — the
// workspace lives in the ref and in the GCM AAD, so isolation is a
// cryptographic and structural property of the ref, not RLS. It never writes
// a domain row: the capture_connection row (with its credential_ref) is the
// domain mutation, committed through storekit by the calling module.
type localVault struct {
	aead cipher.AEAD
	pool *pgxpool.Pool
}

var _ Vault = (*localVault)(nil)

// New builds the local provider. It validates the root key up front (a
// missing or wrong-length key is a boot error, never a silent zero key) and
// does no I/O — readiness of the vault_secret table is reported by Health, so
// construction cannot fail on a not-yet-migrated database.
//
//nolint:ireturn // the seam has two providers (memory + local) behind one Vault; returning the interface is the design.
func New(cfg Config) (Vault, error) {
	aead, err := newAEAD(cfg.RootKey)
	if err != nil {
		return nil, err
	}
	if cfg.Pool == nil {
		return nil, errors.New("keyvault: a database pool is required for the local provider")
	}
	return &localVault{aead: aead, pool: cfg.Pool}, nil
}

// FromEnv builds a local Vault from MARGINCE_KEYVAULT_ROOT_KEY over the given
// pool. It reports configured=false with a nil Vault when the key is unset AND
// this installation has sealed nothing, so a deployment without a vault boots
// normally (a capture-capable role then declares the gap at wiring time rather
// than nil-derefing at Authenticate). A key that is set but malformed or the
// wrong length is a hard error — a misconfigured vault must fail loudly, never
// fall back to something weaker.
//
// An unset key with sealed secrets BEHIND it is the same class of error and is
// refused here rather than at each reader. A root key dropped in a redeploy
// puts every credential the installation holds out of reach at once — connector
// tokens, provider keys, the relay password, the license — and each reader
// discovering that separately describes it in its own words, the worst of which
// is the license path's, which would call an installation that has a license
// unlicensed. One question asked once, where the vault is built, answers for all
// of them. An installation that has never sealed anything is unaffected.
//
//nolint:ireturn // the seam has two providers behind one Vault; returning the interface is the design.
func FromEnv(ctx context.Context, pool *pgxpool.Pool, env config.Lookup) (vault Vault, configured bool, err error) {
	encoded := env(EnvRootKey)
	if encoded == "" {
		return nil, false, refuseIfAnythingIsSealed(ctx, pool)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// The decode error from the base64 package does not include the input,
		// but wrap it with our own message to be certain no key bytes travel.
		return nil, false, fmt.Errorf("keyvault: %s is not valid base64", EnvRootKey)
	}
	v, err := New(Config{RootKey: key, Pool: pool})
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// newAEAD builds the AES-256-GCM AEAD from the root key. The error names the
// length requirement, never the key bytes.
func newAEAD(rootKey []byte) (cipher.AEAD, error) {
	const keyLen = 32 // AES-256
	if len(rootKey) != keyLen {
		return nil, fmt.Errorf("keyvault: root key must be %d bytes for AES-256, got %d", keyLen, len(rootKey))
	}
	block, err := aes.NewCipher(rootKey)
	if err != nil {
		return nil, fmt.Errorf("keyvault: building the cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyvault: building GCM: %w", err)
	}
	return aead, nil
}

// errDecrypt is the opaque failure every decryption path returns: it never
// carries the plaintext (there is none to leak) or any hint of the key. A
// wrong key, a tampered ciphertext, and a swapped AAD are indistinguishable
// to a caller by design.
var errDecrypt = errors.New("keyvault: secret could not be decrypted")

// seal encrypts plaintext under aead, binding aad (the ref) into the GCM tag.
// The stored blob is nonce||ciphertext; the fresh random nonce is drawn from
// crypto/rand, and a failure there is surfaced rather than masked.
func seal(aead cipher.AEAD, aad, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyvault: generating a nonce: %w", err)
	}
	// Seal appends the ciphertext+tag to nonce, so the returned slice is
	// nonce||ciphertext — self-describing for open.
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// open reverses seal. It returns errDecrypt on any authentication failure
// (wrong key, tampered bytes, wrong AAD) so no failure mode leaks detail.
func open(aead cipher.AEAD, aad, sealed []byte) ([]byte, error) {
	ns := aead.NonceSize()
	if len(sealed) < ns {
		return nil, errDecrypt
	}
	nonce, ciphertext := sealed[:ns], sealed[ns:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errDecrypt
	}
	return plaintext, nil
}

func (v *localVault) Put(ctx context.Context, ws ids.WorkspaceID, secret []byte) (Ref, error) {
	if ws.IsZero() {
		return "", errors.New("keyvault: cannot store a secret for a zero workspace id")
	}
	ref, err := mintRef(ws)
	if err != nil {
		return "", err
	}
	sealed, err := seal(v.aead, []byte(ref), secret)
	if err != nil {
		return "", err
	}
	// The ref's random token makes a PK collision astronomically unlikely; an
	// INSERT (not upsert) is correct because a re-Put mints a fresh ref and
	// the old ciphertext is left orphaned rather than overwritten — encrypted,
	// unreferenced, and benign (there is no vault_secret sweeper today; if one
	// is ever added it reclaims these, but nothing depends on that).
	if _, err := v.pool.Exec(ctx,
		`INSERT INTO vault_secret (ref, ciphertext, key_version) VALUES ($1, $2, $3)`,
		string(ref), sealed, currentKeyVersion); err != nil {
		return "", fmt.Errorf("keyvault: storing secret %s: %w", refLogSafe(ref), err)
	}
	return ref, nil
}

func (v *localVault) Get(ctx context.Context, ws ids.WorkspaceID, ref Ref) ([]byte, error) {
	return v.GetOn(ctx, v.pool, ws, ref)
}

// GetOn reads through the caller's querier. The whole difference from Get is
// which connection the SELECT lands on — the decision the caller must be able
// to make, because a caller already inside a transaction cannot afford a
// second connection from the same pool (see the Vault interface).
func (v *localVault) GetOn(ctx context.Context, q Querier, ws ids.WorkspaceID, ref Ref) ([]byte, error) {
	if !ref.scopedTo(ws) {
		// Malformed, or a ref for another workspace: absent to this caller. A
		// ref naming any other key version is likewise absent — its string
		// (version included) simply matches no stored row.
		return nil, ErrNotFound
	}
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT ciphertext FROM vault_secret WHERE ref = $1`, string(ref)).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("keyvault: reading secret %s: %w", refLogSafe(ref), err)
	}
	plaintext, err := open(v.aead, []byte(ref), sealed)
	if err != nil {
		return nil, err // errDecrypt — no leak
	}
	return plaintext, nil
}

func (v *localVault) Delete(ctx context.Context, ws ids.WorkspaceID, ref Ref) error {
	if !ref.scopedTo(ws) {
		// A ref from another workspace (or malformed) addresses nothing here;
		// deleting it is a no-op, so a crash-retry is safe.
		return nil
	}
	if _, err := v.pool.Exec(ctx, `DELETE FROM vault_secret WHERE ref = $1`, string(ref)); err != nil {
		return fmt.Errorf("keyvault: deleting secret %s: %w", refLogSafe(ref), err)
	}
	return nil
}

// Health confirms the vault_secret table exists so a missing migration fails
// readiness with a named cause rather than surfacing only when a secret is
// first stored. It intentionally reads no rows.
func (v *localVault) Health(ctx context.Context) error {
	var reg *string
	if err := v.pool.QueryRow(ctx, `SELECT to_regclass('public.vault_secret')::text`).Scan(&reg); err != nil {
		return fmt.Errorf("keyvault: health: %w", err)
	}
	if reg == nil {
		return errors.New("keyvault: vault_secret table is missing — run migrations")
	}
	return nil
}

// refLogSafe renders a ref for an error/log message without its random token,
// which — while not the secret — is the unguessable capability part of the
// handle. The workspace and version are safe to show and pinpoint the row.
func refLogSafe(ref Ref) string {
	p, err := ref.parse()
	if err != nil {
		return "<malformed-ref>"
	}
	return fmt.Sprintf("mgv.%d.%s.<token>", p.keyVersion, p.workspace)
}

// ConfigItems declares this package's surface. Not Required: a deployment
// without a vault boots, and a capture-capable role then declares the gap at
// wiring time rather than nil-dereferencing at Authenticate. A key that IS set
// but malformed is a hard error, which is a different failure from absence.
func ConfigItems() []config.Item {
	return []config.Item{{
		Name: EnvRootKey, Kind: config.KindString, Secret: true,
		Roles: []string{config.RoleAPI, config.RoleWorker},
		Doc:   "base64 (standard, padded) 32-byte root key sealing connector credentials; unset disables the vault",
	}}
}

// refuseIfAnythingIsSealed turns "no vault configured" into a boot error when
// the installation is holding sealed ciphertext.
//
// Two answers are not evidence and stay silent. A caller with no pool cannot be
// asked — no process role reaches here without one, but the unit lane builds a
// vault before any database exists. And a database with no vault_secret table
// has not been migrated yet, which is a state every fresh install passes
// through and which the migration itself resolves.
//
// EVERY OTHER failure is returned. The temptation is to shrug one off as
// "something later will report this better", and on this path nothing will: a
// process that gets a nil vault here never touches the pool again on the
// license path — sealedSecret short-circuits on a nil vault precisely because
// this function has already spoken — so a swallowed error puts a production
// installation back on "no license is configured", which is the misdirection
// this whole refusal exists to end.
func refuseIfAnythingIsSealed(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return nil
	}
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.vault_secret')::text`).Scan(&reg); err != nil {
		return fmt.Errorf("keyvault: cannot tell whether this installation holds sealed secrets: %w", err)
	}
	if reg == nil {
		// Unmigrated. Nothing can have been sealed into a table that is not there.
		return nil
	}
	var sealed bool
	// vault_secret carries no workspace_id by design (the ref itself is
	// workspace-scoped), so this is an installation-wide question and needs no
	// workspace predicate to be a correct one.
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vault_secret)`).Scan(&sealed); err != nil {
		return fmt.Errorf("keyvault: cannot tell whether this installation holds sealed secrets: %w", err)
	}
	if !sealed {
		return nil
	}
	return fmt.Errorf("keyvault: this installation holds sealed secrets but %s is not set — "+
		"every credential it has sealed is unreachable, including any connector token, provider "+
		"key, outbound-mail password and license token. Restore the root key this installation "+
		"sealed with; it is not recoverable from anywhere else", EnvRootKey)
}
