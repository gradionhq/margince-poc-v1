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

// connectFixtureConnection grants provider under ctx's actor via
// Registry.Connect, then reads back the credential_ref the write produced —
// the fixture's own precondition (a live secret exists before disconnect),
// not the code under test.
func connectFixtureConnection(ctx context.Context, t *testing.T, reg *capture.Registry, provider string) keyvault.Ref {
	t.Helper()
	if _, err := reg.Connect(ctx, provider, connector.Auth("fixture-token")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	wsUUID, ok := principal.WorkspaceID(ctx)
	if !ok {
		t.Fatal("connectFixtureConnection: ctx carries no workspace")
	}
	var status string
	var ref *string
	var auth []byte
	if err := queryConnectionRow(ctx, t, ids.From[ids.WorkspaceKind](wsUUID), provider, &status, &ref, &auth); err != nil {
		t.Fatalf("reading back the connection Connect wrote: %v", err)
	}
	if ref == nil {
		t.Fatal("Connect left credential_ref NULL — the fixture precondition (a live secret) does not hold")
	}
	return keyvault.Ref(*ref)
}

// queryConnectionRow reads one capture_connection row's disconnect-relevant
// columns straight off the owner connection — bypassing RLS on purpose, since
// the test asserts what the row actually holds, not what one workspace's
// policy exposes.
func queryConnectionRow(ctx context.Context, t *testing.T, ws ids.WorkspaceID, provider string, status *string, credentialRef **string, auth *[]byte) error {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	return owner.QueryRow(ctx,
		`SELECT status, credential_ref, auth FROM capture_connection WHERE workspace_id = $1 AND provider = $2`,
		ws.UUID, provider).Scan(status, credentialRef, auth)
}

// Disconnecting must not leave a live credential behind: the row stops being
// selectable AND the sealed secret is destroyed. Until this held, a user who
// disconnected kept a decryptable refresh token in the vault indefinitely.
func TestDisconnectDeletesTheStoredCredential(t *testing.T) {
	ctx, reg, vault, ws := newCaptureRegistryFixture(t)

	ref := connectFixtureConnection(ctx, t, reg, "gmail")
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
	if err := queryConnectionRow(ctx, t, ws, "gmail", &status, &credentialRef, &authBytes); err != nil {
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
	connectFixtureConnection(ctx, t, reg, "gmail")

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
	connectFixtureConnection(ctx, t, reg, "gmail")

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
