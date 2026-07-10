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
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// envRootKey names the environment variable carrying the base64 (standard
// encoding) 32-byte AES-256 root key. It is read from the environment, never
// a CLI flag — a flag leaks into the process table. The value never reaches
// a log line or an error message.
const envRootKey = "MARGINCE_KEYVAULT_ROOT_KEY"

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
// a domain row: the connector_connection row (with its credential_ref) is the
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
// pool. It reports configured=false with a nil Vault when the key is unset,
// so a deployment without a vault boots normally (a capture-capable role then
// declares the gap at wiring time rather than nil-derefing at Authenticate).
// A key that is set but malformed or the wrong length is a hard error — a
// misconfigured vault must fail loudly, never fall back to something weaker.
//
//nolint:ireturn // the seam has two providers behind one Vault; returning the interface is the design.
func FromEnv(pool *pgxpool.Pool) (vault Vault, configured bool, err error) {
	encoded := os.Getenv(envRootKey)
	if encoded == "" {
		return nil, false, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// The decode error from the base64 package does not include the input,
		// but wrap it with our own message to be certain no key bytes travel.
		return nil, false, fmt.Errorf("keyvault: %s is not valid base64", envRootKey)
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
	ref, err := mintRef(ws, currentKeyVersion)
	if err != nil {
		return "", err
	}
	sealed, err := seal(v.aead, []byte(ref), secret)
	if err != nil {
		return "", err
	}
	// The ref's random token makes a PK collision astronomically unlikely; an
	// INSERT (not upsert) is correct because a re-Put mints a fresh ref and
	// the old ciphertext is orphaned (swept later), never overwritten.
	if _, err := v.pool.Exec(ctx,
		`INSERT INTO vault_secret (ref, ciphertext, key_version) VALUES ($1, $2, $3)`,
		string(ref), sealed, currentKeyVersion); err != nil {
		return "", fmt.Errorf("keyvault: storing secret %s: %w", refLogSafe(ref), err)
	}
	return ref, nil
}

func (v *localVault) Get(ctx context.Context, ws ids.WorkspaceID, ref Ref) ([]byte, error) {
	p, err := ref.parse()
	if err != nil || p.workspace != ws {
		// Malformed, or a ref for another workspace: absent to this caller.
		return nil, ErrNotFound
	}
	if p.keyVersion != currentKeyVersion {
		// A ref sealed under a key version this build does not hold: an
		// operational condition (a partial rotation rollback / corruption),
		// not tenant absence — surface it, naming only the version number.
		return nil, fmt.Errorf("keyvault: ref names key version %d, which this vault does not hold", p.keyVersion)
	}
	var sealed []byte
	err = v.pool.QueryRow(ctx, `SELECT ciphertext FROM vault_secret WHERE ref = $1`, string(ref)).Scan(&sealed)
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
	p, err := ref.parse()
	if err != nil || p.workspace != ws {
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
