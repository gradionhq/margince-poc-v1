// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The keyvault seam end to end: a connector credential is
// sealed in the vault at Connect and resolved from it at Sync, so the
// connector_connection row carries an opaque credential_ref, never the
// credential bytes. Proven on real Postgres with the local (AES-256-GCM)
// provider: the round-trip, cross-workspace ref isolation, a wrong root key
// failing without leaking plaintext, and the additive backfill of a legacy
// auth-bytea row onto the vault.

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// newTestKeyvault builds the local (config-backed) provider over the test
// pool with a fresh random root key. The vault_secret table exists because
// setupSearch migrated the schema.
func newTestKeyvault(t *testing.T, e *searchEnv) keyvault.Vault {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating a test root key: %v", err)
	}
	v, err := keyvault.New(keyvault.Config{RootKey: key, Pool: e.Pool})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	return v
}

// authAssertingFake records the Auth it is handed at Sync so a test can prove
// the credential the vault resolved is exactly the one granted. It emits no
// records: this suite is about credential resolution, not capture semantics.
type authAssertingFake struct {
	gotAuth connector.Auth
}

func (f *authAssertingFake) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "authfake", Version: "1.0.0",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen,
	}
}

func (f *authAssertingFake) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("granted-token"), nil
}

func (f *authAssertingFake) Sync(_ context.Context, auth connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	f.gotAuth = auth
	return cursor, nil
}

func (f *authAssertingFake) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (f *authAssertingFake) HealthCheck(context.Context, connector.Auth) error { return nil }

func TestConnectSealsCredentialInVaultNotOnTheRow(t *testing.T) {
	e := setupSearch(t)
	vault := newTestKeyvault(t, e)
	registry := newTestCaptureRegistry(e, vault)
	registry.Register(&authAssertingFake{})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "authfake", connector.Auth("granted-token"))
	if err != nil {
		t.Fatal(err)
	}

	var credentialRef *string
	var authBytes []byte
	err = database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref, auth FROM connector_connection WHERE id = $1`, connID).
			Scan(&credentialRef, &authBytes)
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentialRef == nil || *credentialRef == "" {
		t.Fatal("Connect did not record a credential_ref on the row")
	}
	if authBytes != nil {
		t.Fatalf("Connect left the credential bytes on the row (auth is not NULL): %q", authBytes)
	}
	// The vault holds the sealed credential under the row's ref.
	got, err := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(*credentialRef))
	if err != nil {
		t.Fatalf("resolving the recorded ref: %v", err)
	}
	if !bytes.Equal(got, []byte("granted-token")) {
		t.Fatalf("vault holds %q, want the granted credential", got)
	}
}

func TestSyncResolvesCredentialFromVault(t *testing.T) {
	e := setupSearch(t)
	vault := newTestKeyvault(t, e)
	registry := newTestCaptureRegistry(e, vault)
	fake := &authAssertingFake{}
	registry.Register(fake)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "authfake", connector.Auth("granted-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	if string(fake.gotAuth) != "granted-token" {
		t.Fatalf("Sync received %q, want the vault-resolved granted credential", fake.gotAuth)
	}
}

// The local provider on real Postgres must honour the same isolation the
// memory fake does, plus surface a wrong-root-key decrypt as an error (not
// absence) without leaking the plaintext.
func TestLocalVaultIsolationAndWrongKeyOnRealPostgres(t *testing.T) {
	e := setupSearch(t)
	vault := newTestKeyvault(t, e)
	ctx := context.Background()
	wsA := ids.From[ids.WorkspaceKind](e.WS)
	wsB := ids.New[ids.WorkspaceKind]()

	ref, err := vault.Put(ctx, wsA, []byte("tenant-a-credential"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := vault.Get(ctx, wsA, ref)
	if err != nil || !bytes.Equal(got, []byte("tenant-a-credential")) {
		t.Fatalf("round-trip failed: got %q err %v", got, err)
	}
	if _, err := vault.Get(ctx, wsB, ref); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("cross-workspace Get: got %v, want ErrNotFound", err)
	}

	// A second vault over the SAME table with a DIFFERENT root key finds the
	// row (same ref) but cannot decrypt it — a surfaced error, no plaintext.
	other := newTestKeyvault(t, e)
	_, err = other.Get(ctx, wsA, ref)
	if err == nil {
		t.Fatal("Get under the wrong root key must fail")
	}
	if errors.Is(err, keyvault.ErrNotFound) {
		t.Fatal("a wrong-key decrypt must surface an error, not masquerade as ErrNotFound")
	}
	if bytes.Contains([]byte(err.Error()), []byte("tenant-a-credential")) {
		t.Fatalf("decrypt error leaks the plaintext: %v", err)
	}

	// Delete removes it and is idempotent; Health sees the vault_secret table.
	if err := vault.Delete(ctx, wsA, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := vault.Get(ctx, wsA, ref); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
	}
	if err := vault.Delete(ctx, wsA, ref); err != nil {
		t.Fatalf("second Delete must be a no-op: %v", err)
	}
	if err := vault.Health(ctx); err != nil {
		t.Fatalf("Health against the migrated schema must pass: %v", err)
	}
}

func TestBackfillMigratesLegacyAuthRowOntoTheVault(t *testing.T) {
	e := setupSearch(t)
	vault := newTestKeyvault(t, e)
	registry := newTestCaptureRegistry(e, vault)
	fake := &authAssertingFake{}
	registry.Register(fake)

	// A legacy row: the credential lives in the auth bytea column, no ref.
	connID := e.seed(t, `
		INSERT INTO connector_connection (id, workspace_id, connector, granted_by, scopes, auth)
		VALUES ($1, $2, 'authfake', $3, $4, $5)`,
		e.Rep1, []string{string(principal.ScopeRead)}, []byte("granted-token"))

	migrated, err := registry.BackfillCredentials(context.Background())
	if err != nil {
		t.Fatalf("BackfillCredentials: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("backfill migrated %d rows, want 1", migrated)
	}

	// The row now carries a ref and the legacy bytes are cleared.
	var credentialRef *string
	var authBytes []byte
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref, auth FROM connector_connection WHERE id = $1`, connID).
			Scan(&credentialRef, &authBytes)
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentialRef == nil || *credentialRef == "" {
		t.Fatal("backfill did not record a credential_ref")
	}
	if authBytes != nil {
		t.Fatalf("backfill left the legacy bytes on the row: %q", authBytes)
	}
	got, err := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(*credentialRef))
	if err != nil || !bytes.Equal(got, []byte("granted-token")) {
		t.Fatalf("vault does not hold the migrated credential: got %q err %v", got, err)
	}

	// A second backfill is a no-op: the row already carries a ref.
	migrated, err = registry.BackfillCredentials(context.Background())
	if err != nil {
		t.Fatalf("second BackfillCredentials: %v", err)
	}
	if migrated != 0 {
		t.Fatalf("idempotent backfill migrated %d rows on the second run, want 0", migrated)
	}

	// Sync now resolves the migrated credential through the vault.
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	if string(fake.gotAuth) != "granted-token" {
		t.Fatalf("Sync after backfill received %q, want the migrated credential", fake.gotAuth)
	}
}
