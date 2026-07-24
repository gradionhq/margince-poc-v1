// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Disconnect over a real migrated Postgres + a real Vault: the invariant
// under test — the sealed credential is actually destroyed, not just the row
// marked disconnected — cannot be proven against a mock vault (a mock proves
// the mock). Modelled on modules/identity/*_integration_test.go; there are no
// capture-module integration tests to copy from (the standing-IMAP ones live
// in compose/, which modules/* cannot import).

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// captureDB connects once per test binary to the already-migrated database
// (`make migrate` — the integration lane's precondition, same as every
// module-local fixture); every test then bootstraps its own workspace, so
// the suites stay independent.
var captureDB struct {
	once  sync.Once
	owner *pgx.Conn
	pool  *pgxpool.Pool
	err   error
}

func setupCaptureDB(t *testing.T) (*pgx.Conn, *pgxpool.Pool) {
	t.Helper()
	captureDB.once.Do(func() {
		ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
		appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
		if ownerDSN == "" || appDSN == "" {
			captureDB.err = errors.New("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
			return
		}
		ctx := context.Background()
		owner, err := pgx.Connect(ctx, ownerDSN)
		if err != nil {
			captureDB.err = err
			return
		}
		captureDB.owner = owner
		captureDB.pool, captureDB.err = database.NewPool(ctx, appDSN)
	})
	if captureDB.err != nil {
		t.Fatal(captureDB.err)
	}
	return captureDB.owner, captureDB.pool
}

// fixtureOwnerEmail is the mailbox fixtureConnector reports through
// AccountLabeler — a fixed value, since the fixture's opaque auth bytes
// ("fixture-token") carry no real account identity to parse out.
const fixtureOwnerEmail = "fixture-owner@example.test"

// fixtureConnector is the minimal connector.Connector the disconnect path
// needs registered: Disconnect never calls Sync/Normalize/HealthCheck, and
// Connect only reads Descriptor().Scopes (left empty, so the grant needs no
// scope from the fixture actor). It also implements AccountLabeler so
// TestConnectRecordsTheAccountLabel can assert the label is written at
// connect time.
type fixtureConnector struct{}

func (fixtureConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: "gmail", Version: "fixture"}
}

func (fixtureConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("fixture-token"), nil
}

func (fixtureConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (fixtureConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, nil
}

func (fixtureConnector) HealthCheck(context.Context, connector.Auth) error {
	return nil
}

func (fixtureConnector) AccountLabel(connector.Auth) (string, error) {
	return fixtureOwnerEmail, nil
}

var _ connector.AccountLabeler = fixtureConnector{}

// newCaptureRegistryFixture bootstraps a fresh workspace + human user, a
// Registry wired to a real (in-memory) Vault and the fixture connector, and
// the actor context Disconnect/Connect require — the same shape the HTTP
// middleware binds. Each call gets its own workspace, so the two tests in
// this file never collide.
func newCaptureRegistryFixture(t *testing.T) (context.Context, *capture.Registry, keyvault.Vault, ids.WorkspaceID) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()

	wsUUID := ids.NewV7()
	userUUID := ids.NewV7()
	// The full UUID, not a truncated prefix: a v7 id's leading hex digits are
	// the millisecond timestamp, so two workspaces minted in the same test
	// binary run within the same millisecond would truncate to the same
	// slug and collide on workspace_slug_unique.
	slug := "capture-disconnect-" + wsUUID.String()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Capture Disconnect', $2, 'USD')`,
		wsUUID, slug); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Fixture User')`,
		userUUID, wsUUID, "user-"+userUUID.String()+"@"+slug+".test"); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	vault := keyvault.NewMemory()
	reg := capture.NewRegistry(pool, nil, nil, vault)
	reg.Register(fixtureConnector{})

	actorCtx := principal.WithWorkspaceID(ctx, wsUUID)
	actorCtx = principal.WithActor(actorCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userUUID.String(), UserID: userUUID,
	})
	actorCtx = principal.WithCorrelationID(actorCtx, ids.NewV7())

	return actorCtx, reg, vault, ids.From[ids.WorkspaceKind](wsUUID)
}

// connectFixtureConnection grants the "gmail" provider (the only one
// fixtureConnector registers under) via Registry.Connect, then reads back the
// credential_ref the write produced — the fixture's own precondition (a live
// secret exists before disconnect), not the code under test.
func connectFixtureConnection(ctx context.Context, t *testing.T, reg *capture.Registry) keyvault.Ref {
	t.Helper()
	if _, err := reg.Connect(ctx, "gmail", connector.Auth("fixture-token")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	wsUUID, ok := principal.WorkspaceID(ctx)
	if !ok {
		t.Fatal("connectFixtureConnection: ctx carries no workspace")
	}
	var status string
	var ref *string
	var auth []byte
	if err := queryConnectionRow(ctx, t, ids.From[ids.WorkspaceKind](wsUUID), &status, &ref, &auth); err != nil {
		t.Fatalf("reading back the connection Connect wrote: %v", err)
	}
	if ref == nil {
		t.Fatal("Connect left credential_ref NULL — the fixture precondition (a live secret) does not hold")
	}
	return keyvault.Ref(*ref)
}

// queryConnectionRow reads the "gmail" capture_connection row's
// disconnect-relevant columns straight off the owner connection — bypassing
// RLS on purpose, since the test asserts what the row actually holds, not
// what one workspace's policy exposes. "gmail" is the only provider any
// fixture in this file connects.
func queryConnectionRow(ctx context.Context, t *testing.T, ws ids.WorkspaceID, status *string, credentialRef **string, auth *[]byte) error {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	return owner.QueryRow(ctx,
		`SELECT status, credential_ref, auth FROM capture_connection WHERE workspace_id = $1 AND provider = 'gmail'`,
		ws.UUID).Scan(status, credentialRef, auth)
}

// Disconnecting must not leave a live credential behind: the row stops being
// selectable AND the sealed secret is destroyed. Until this held, a user who
// disconnected kept a decryptable refresh token in the vault indefinitely.
func TestDisconnectDeletesTheStoredCredential(t *testing.T) {
	ctx, reg, vault, ws := newCaptureRegistryFixture(t)

	ref := connectFixtureConnection(ctx, t, reg)
	if _, err := vault.Get(ctx, ws, ref); err != nil {
		t.Fatalf("precondition: the secret should exist before disconnect: %v", err)
	}

	if err := reg.Disconnect(ctx, "gmail"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if _, err := vault.Get(ctx, ws, ref); err == nil {
		t.Error("the vault secret survived disconnect — a live credential outlives the user's withdrawal of consent")
	}

	var status string
	var credentialRef *string
	var authBytes []byte
	if err := queryConnectionRow(ctx, t, ws, &status, &credentialRef, &authBytes); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != "disconnected" {
		t.Errorf("status = %q, want %q", status, "disconnected")
	}
	if credentialRef != nil {
		t.Errorf("credential_ref = %q, want NULL — a dangling ref points at a destroyed secret", *credentialRef)
	}
	if authBytes != nil {
		t.Error("legacy auth column still holds credential bytes — a credential escaped erasure through the older column")
	}
}

// A second disconnect is a no-op, not an error: the operation is idempotent
// and the secret is already gone.
func TestDisconnectIsIdempotentAfterTheSecretIsGone(t *testing.T) {
	ctx, reg, _, _ := newCaptureRegistryFixture(t)
	connectFixtureConnection(ctx, t, reg)

	if err := reg.Disconnect(ctx, "gmail"); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	if err := reg.Disconnect(ctx, "gmail"); err != nil {
		t.Errorf("second Disconnect: %v — disconnect must stay idempotent", err)
	}
}

// The label must be present the instant the row exists — right after connect is
// exactly when a user asks "did I authorize the right account?". Deriving it
// from sync_cursor would leave it null until the first successful sync.
func TestConnectRecordsTheAccountLabel(t *testing.T) {
	ctx, reg, _, _ := newCaptureRegistryFixture(t)
	connectFixtureConnection(ctx, t, reg)

	views, err := reg.Connections(ctx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	for _, v := range views {
		if v.Provider != "gmail" {
			continue
		}
		if v.AccountLabel == nil || *v.AccountLabel != fixtureOwnerEmail {
			t.Errorf("AccountLabel = %v, want %q", v.AccountLabel, fixtureOwnerEmail)
		}
		return
	}
	t.Fatal("no gmail connection in the read-back")
}

// insertLegacyConnection writes a capture_connection row the way a
// pre-vault-migration row looks: the credential lives in the auth bytea
// column, credential_ref is NULL. Registry.Connect always vault-seals, so a
// legacy row can only be produced directly against the DB — this is the
// fixture's own precondition, not the code under test.
func insertLegacyConnection(ctx context.Context, t *testing.T, ws ids.WorkspaceID, userID ids.UserID, provider string, authBytes []byte) {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	if _, err := owner.Exec(ctx, `
		INSERT INTO capture_connection (workspace_id, provider, user_id, status, auth)
		VALUES ($1, $2, $3, 'connected', $4)`,
		ws.UUID, provider, userID.UUID, authBytes); err != nil {
		t.Fatalf("inserting the legacy connection fixture: %v", err)
	}
}

// A legacy row (credential in auth, credential_ref NULL — resolveCredential's
// documented fallback for a row whose vault migration never ran) must not
// survive disconnect: a credential_ref-only predicate matches no such row,
// leaving it connected with the secret still in auth column forever.
func TestDisconnectErasesALegacyAuthOnlyRow(t *testing.T) {
	ctx, reg, _, ws := newCaptureRegistryFixture(t)
	actor, _ := principal.Actor(ctx)
	insertLegacyConnection(ctx, t, ws, ids.From[ids.UserKind](actor.UserID), "gmail", []byte("legacy-refresh-token"))

	if err := reg.Disconnect(ctx, "gmail"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	var status string
	var credentialRef *string
	var authBytes []byte
	if err := queryConnectionRow(ctx, t, ws, &status, &credentialRef, &authBytes); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != "disconnected" {
		t.Errorf("status = %q, want %q — the legacy row must actually stop syncing", status, "disconnected")
	}
	if authBytes != nil {
		t.Error("legacy auth column still holds the credential bytes — disconnect was a silent no-op on this row shape")
	}
	if credentialRef != nil {
		t.Errorf("credential_ref = %q, want NULL", *credentialRef)
	}
}

// A reconnect (Connect called again for a provider that already has a live
// connection — the reauth_required → Reconnect flow) must destroy the secret
// it replaces, not just overwrite the row's pointer to it. Otherwise every
// reconnect strands the previous refresh token in the vault, live and
// decryptable, forever.
func TestReconnectDeletesThePriorSecret(t *testing.T) {
	ctx, reg, vault, ws := newCaptureRegistryFixture(t)

	firstRef := connectFixtureConnection(ctx, t, reg)
	if _, err := vault.Get(ctx, ws, firstRef); err != nil {
		t.Fatalf("precondition: the first secret should exist: %v", err)
	}

	if _, err := reg.Connect(ctx, "gmail", connector.Auth("fixture-token-2")); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	if _, err := vault.Get(ctx, ws, firstRef); err == nil {
		t.Error("the superseded secret survived the reconnect — a stale credential outlives the row that named it")
	}

	var status string
	var secondRef *string
	var authBytes []byte
	if err := queryConnectionRow(ctx, t, ws, &status, &secondRef, &authBytes); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if secondRef == nil {
		t.Fatal("credential_ref is NULL after reconnect — the row lost its credential entirely")
	}
	if keyvault.Ref(*secondRef) == firstRef {
		t.Fatal("the row still points at the first secret — the reconnect did not seal a new one")
	}
	if _, err := vault.Get(ctx, ws, keyvault.Ref(*secondRef)); err != nil {
		t.Fatalf("the new secret should resolve: %v", err)
	}
}

// A Disconnect that fails to delete the vault secret must surface the error
// and leave the row in a RECOVERABLE state: 'disconnected' (the poller
// already stops selecting it) with credential_ref still set, so a retry can
// find and finish the job. Nulling the ref anyway would point at nothing —
// the destroyed-secret half of the invariant would be a lie.
func TestDisconnectSurfacesAFailingVaultDeleteAndLeavesARecoverableRow(t *testing.T) {
	ctx, reg, vault, ws := newCaptureRegistryFixture(t)
	ref := connectFixtureConnection(ctx, t, reg)

	failing := &deleteFailsVault{Vault: vault}
	reg2 := capture.NewRegistry(poolFromFixture(t), nil, nil, failing)
	reg2.Register(fixtureConnector{})
	_ = reg // the fixture's Connect already ran against the shared pool/vault

	if err := reg2.Disconnect(ctx, "gmail"); err == nil {
		t.Fatal("Disconnect must surface a failing vault delete, not swallow it")
	}

	var status string
	var credentialRef *string
	var authBytes []byte
	if err := queryConnectionRow(ctx, t, ws, &status, &credentialRef, &authBytes); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != "disconnected" {
		t.Errorf("status = %q, want %q — capture must stop even though the delete failed", status, "disconnected")
	}
	if credentialRef == nil || keyvault.Ref(*credentialRef) != ref {
		t.Errorf("credential_ref = %v, want the still-live ref %q — a failed delete must stay retryable", credentialRef, ref)
	}
	if authBytes != nil {
		t.Error("legacy auth column should already be cleared regardless of the vault outcome")
	}
}

// A reconnect landing between Disconnect's phase 1 (flip status, read the
// old ref) and phase 3 (null the ref, after the vault delete completes) must
// not have its NEW ref nulled by the disconnect it raced. Phase 3 guards on
// "clear only the ref THIS call resolved" — without that guard the row would
// end up 'connected' with no credential_ref, and the fresh secret orphaned.
func TestDisconnectPhase3DoesNotClobberAConcurrentReconnect(t *testing.T) {
	ctx, reg, vault, ws := newCaptureRegistryFixture(t)
	firstRef := connectFixtureConnection(ctx, t, reg)

	deleteStarted := make(chan struct{})
	proceed := make(chan struct{})
	blocking := &blockingDeleteVault{Vault: vault, started: deleteStarted, proceed: proceed}
	reg2 := capture.NewRegistry(poolFromFixture(t), nil, nil, blocking)
	reg2.Register(fixtureConnector{})

	disconnectErr := make(chan error, 1)
	go func() { disconnectErr <- reg2.Disconnect(ctx, "gmail") }()

	<-deleteStarted // phase 1 has committed (status disconnected, ref = firstRef); phase 2's Delete is blocked

	// The concurrent reconnect: a fresh Registry sharing the same pool/vault,
	// exactly like a second request would land on the same process.
	reg3 := capture.NewRegistry(poolFromFixture(t), nil, nil, vault)
	reg3.Register(fixtureConnector{})
	if _, err := reg3.Connect(ctx, "gmail", connector.Auth("fixture-token-reconnect")); err != nil {
		t.Fatalf("concurrent reconnect: %v", err)
	}
	var secondRef *string
	if err := queryConnectionRow(ctx, t, ws, new(string), &secondRef, new([]byte)); err != nil {
		t.Fatalf("reading the reconnected row: %v", err)
	}
	if secondRef == nil || keyvault.Ref(*secondRef) == firstRef {
		t.Fatal("precondition: the reconnect should have sealed a new ref")
	}

	close(proceed) // let the racing Disconnect's phase 2/3 finish
	if err := <-disconnectErr; err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	var status string
	var finalRef *string
	if err := queryConnectionRow(ctx, t, ws, &status, &finalRef, new([]byte)); err != nil {
		t.Fatalf("reading the final row: %v", err)
	}
	if status != "connected" {
		t.Errorf("status = %q, want %q — the racing disconnect must not undo a reconnect that landed after it read the row", status, "connected")
	}
	if finalRef == nil || *finalRef != *secondRef {
		t.Errorf("credential_ref = %v, want the reconnect's ref %q untouched by the racing disconnect", finalRef, *secondRef)
	}
	if _, err := vault.Get(ctx, ws, keyvault.Ref(*secondRef)); err != nil {
		t.Errorf("the reconnect's secret should still resolve: %v", err)
	}
}

// deleteFailsVault wraps a real Vault and fails every Delete, so
// Disconnect's failure path (surface the error, leave the row recoverable)
// is exercised against a genuine vault failure, not a mock that only proves
// the mock.
type deleteFailsVault struct {
	keyvault.Vault
}

func (deleteFailsVault) Delete(context.Context, ids.WorkspaceID, keyvault.Ref) error {
	return errors.New("keyvault: simulated delete failure")
}

// blockingDeleteVault wraps a real Vault and pauses inside Delete until the
// test releases it — the seam that lets TestDisconnectPhase3DoesNotClobberAConcurrentReconnect
// interleave a real concurrent Connect between Disconnect's phase 1 and
// phase 3, deterministically, without a time.Sleep race.
type blockingDeleteVault struct {
	keyvault.Vault
	started chan struct{}
	proceed chan struct{}
}

func (v *blockingDeleteVault) Delete(ctx context.Context, ws ids.WorkspaceID, ref keyvault.Ref) error {
	close(v.started)
	<-v.proceed
	return v.Vault.Delete(ctx, ws, ref)
}

// poolFromFixture exposes the shared pool setupCaptureDB memoizes, so a test
// can stand up a second Registry against the SAME database (simulating a
// second request on the same process) without re-parsing DSNs.
func poolFromFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, pool := setupCaptureDB(t)
	return pool
}
