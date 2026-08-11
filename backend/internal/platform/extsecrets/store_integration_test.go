// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package extsecrets_test

// The store's whole job is a wall between namespaces, and every wall it
// builds is made of SQL predicates and RLS — so the suite is an integration
// suite by necessity: a fake pool would test the wall's drawing, not the
// wall. The fixture is package-local (its own owner connection and app pool
// off MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN, the same pair every
// integration package reads) rather than a shared helper: promoting one
// would be a change to how every suite in the tree is set up, which is not
// this package's call to make.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/extsecrets"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// env is the fixture: a migrated database holding TWO workspaces, each with
// one member. Two tenants rather than one because half of what this store
// promises is about the other tenant — a secret it must not read, a user it
// must not attach.
type env struct {
	owner *pgx.Conn
	pool  *pgxpool.Pool
	vault keyvault.Vault

	ws, user           ids.UUID
	otherWS, otherUser ids.UUID
}

// setup migrates once per process, resets the data, and seeds the two
// tenants. Integration tests fail loudly without a database — they never
// skip.
func setup(t *testing.T) *env {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &env{
		owner:     owner,
		vault:     keyvault.NewMemory(),
		ws:        ids.NewV7(),
		user:      ids.NewV7(),
		otherWS:   ids.NewV7(),
		otherUser: ids.NewV7(),
	}
	seed := func(ws, user ids.UUID, slug string) {
		if _, err := owner.Exec(ctx,
			`INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, slug); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Member')`,
			user, ws, slug+"@extsecrets.test"); err != nil {
			t.Fatal(err)
		}
	}
	seed(e.ws, e.user, "extsecrets")
	seed(e.otherWS, e.otherUser, "extsecrets-other")

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	return e
}

// ctxFor binds the workspace and an actor: the store writes a system_log row
// on every operation, and storekit.LogSystem needs both.
func (e *env) ctxFor(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem,
		ID:   "system:extsecrets-test",
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// currentRef reads the ref a mapping row names, through the owner connection
// with the tenant GUC bound — extension_secret FORCEs row level security, so
// even the schema owner sees nothing without it.
func (e *env) currentRef(t *testing.T, ws ids.UUID, unit, key string) keyvault.Ref {
	t.Helper()
	ctx := context.Background()
	if _, err := e.owner.Exec(ctx, `SELECT set_config('app.workspace_id', $1, false)`, ws.String()); err != nil {
		t.Fatal(err)
	}
	var ref string
	err := e.owner.QueryRow(ctx, `
		SELECT vault_ref FROM extension_secret
		 WHERE extension_name = $1 AND workspace_id = $2 AND key = $3 AND user_id IS NULL`,
		unit, ws, key).Scan(&ref)
	if err != nil {
		t.Fatalf("reading the mapping row for %s/%s: %v", unit, key, err)
	}
	return keyvault.Ref(ref)
}

// rowsVisibleFrom counts the mapping rows a SELECT pinned to ws can see for
// this unit and key — regardless of which workspace actually owns them. It
// deliberately does NOT filter on workspace_id: the whole question is what
// the database hands back to a session bound to ws, which is the tenant
// policy's answer and nobody else's.
//
// It rides the APP pool, not the owner connection. The integration cluster's
// owner role bypasses row level security (BYPASSRLS/superuser), which FORCE
// does not override — so the same probe on e.owner returns the other
// tenant's row and would report a policy failure that is not one. margince_app
// is the role the policy actually binds, and the role every runtime path uses.
func (e *env) rowsVisibleFrom(t *testing.T, ws ids.UUID, unit, key string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.ctxFor(ws), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM extension_secret WHERE extension_name = $1 AND key = $2`,
			unit, key).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// systemLogActions returns the ledger entries for ws, oldest first, each as
// "<action>" or "<action>/<outcome>" where an outcome was recorded.
func (e *env) systemLogActions(t *testing.T, ws ids.UUID) []string {
	t.Helper()
	ctx := context.Background()
	if _, err := e.owner.Exec(ctx, `SELECT set_config('app.workspace_id', $1, false)`, ws.String()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.owner.Query(ctx, `
		SELECT action, coalesce('/' || (detail->>'outcome'), '')
		  FROM system_log WHERE workspace_id = $1 ORDER BY occurred_at, id`, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, outcome string
		if err := rows.Scan(&action, &outcome); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action+outcome)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return actions
}

// TestSecretsNamespaceIsolation is the wall this whole table exists to
// build: the unit name is closed over, never supplied, so two units may use
// the same bare key name and neither resolves the other's secret.
func TestSecretsNamespaceIsolation(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	a := extsecrets.For("notes", e.pool, e.vault)
	b := extsecrets.For("other-unit", e.pool, e.vault)

	if err := a.Put(ctx, "signing", []byte("s3cret")); err != nil {
		t.Fatal(err)
	}
	// b addresses the same bare key name; it must not resolve a's secret.
	if _, err := b.Get(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("unit b read unit a's secret under the same key name: err=%v", err)
	}
	// The delete path is the wall's most destructive side, and it is walled
	// separately: b deleting under a's key name must find nothing, and must
	// leave both a's mapping row AND a's ciphertext standing. Dropping
	// extension_name from the DELETE passes every read-path assertion above.
	if err := b.Delete(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("unit b deleted under unit a's key name: err=%v", err)
	}
	got, err := a.Get(ctx, "signing")
	if err != nil {
		t.Fatalf("unit b's delete destroyed unit a's secret: %v", err)
	}
	if string(got) != "s3cret" {
		t.Fatalf("unit a read back %q, want %q", got, "s3cret")
	}
}

// TestSecretsRotationDestroysThePreviousMaterial: keyvault.Put is
// INSERT-only and mints a fresh ref each time, so without an explicit
// destroy a credential rotated on a schedule grows vault_secret without
// bound — and leaves every superseded credential decryptable at rest.
func TestSecretsRotationDestroysThePreviousMaterial(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)

	if err := s.Put(ctx, "signing", []byte("one")); err != nil {
		t.Fatal(err)
	}
	first := e.currentRef(t, e.ws, "notes", "signing")
	if err := s.Put(ctx, "signing", []byte("two")); err != nil {
		t.Fatal(err)
	}
	second := e.currentRef(t, e.ws, "notes", "signing")
	if second == first {
		t.Fatal("rotation reused the ref: keyvault.Put mints a new one on every call")
	}
	if _, err := e.vault.Get(ctx, ids.From[ids.WorkspaceKind](e.ws), first); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("rotation left the previous ciphertext in the vault: err=%v", err)
	}
	got, err := s.Get(ctx, "signing")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("read back %q after rotation, want %q", got, "two")
	}
}

// TestSecretsUserScopeRefusesAForeignMember: the workspace FK and the user
// FK are one COMPOSITE constraint precisely so a cross-tenant attachment is
// impossible in the schema, but the store checks membership itself so the
// refusal reaches the caller as a named error rather than as a constraint
// violation nobody can branch on.
func TestSecretsUserScopeRefusesAForeignMember(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)

	foreign := extension.UserID(e.otherUser.String())
	if err := s.PutUser(ctx, foreign, "token", []byte("nope")); !errors.Is(err, extsecrets.ErrUserOutsideWorkspace) {
		t.Fatalf("PutUser accepted a member of another workspace: err=%v", err)
	}
	if _, err := s.GetUser(ctx, foreign, "token"); !errors.Is(err, extsecrets.ErrUserOutsideWorkspace) {
		t.Fatalf("GetUser accepted a member of another workspace: err=%v", err)
	}
	if err := s.DeleteUser(ctx, foreign, "token"); !errors.Is(err, extsecrets.ErrUserOutsideWorkspace) {
		t.Fatalf("DeleteUser accepted a member of another workspace: err=%v", err)
	}
}

// TestSecretsScopesAreIndependent: a workspace key and a user key of the
// same name are two secrets, not one seen two ways.
func TestSecretsScopesAreIndependent(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)
	member := extension.UserID(e.user.String())

	if err := s.Put(ctx, "token", []byte("installation")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(ctx, member, "token", []byte("personal")); err != nil {
		t.Fatal(err)
	}

	ws, err := s.Get(ctx, "token")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.GetUser(ctx, member, "token")
	if err != nil {
		t.Fatal(err)
	}
	if string(ws) != "installation" || string(user) != "personal" {
		t.Fatalf("scopes bled: workspace=%q user=%q", ws, user)
	}

	// Deleting one leaves the other standing.
	if err := s.Delete(ctx, "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "token"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("the workspace secret survived its delete: err=%v", err)
	}
	if _, err := s.GetUser(ctx, member, "token"); err != nil {
		t.Fatalf("deleting the workspace secret took the user's with it: %v", err)
	}
	if err := s.DeleteUser(ctx, member, "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, member, "token"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("the user secret survived its delete: err=%v", err)
	}
}

// TestSecretsDeleteDestroysTheMaterial: the mapping row and the ciphertext
// go together, or a "deleted" credential is still decryptable at rest.
func TestSecretsDeleteDestroysTheMaterial(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)

	if err := s.Put(ctx, "signing", []byte("one")); err != nil {
		t.Fatal(err)
	}
	ref := e.currentRef(t, e.ws, "notes", "signing")
	if err := s.Delete(ctx, "signing"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.vault.Get(ctx, ids.From[ids.WorkspaceKind](e.ws), ref); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("delete left the ciphertext in the vault: err=%v", err)
	}
	if err := s.Delete(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("deleting an absent key: err=%v, want ErrSecretNotFound", err)
	}
}

// TestSecretsAreWorkspaceScoped: the same unit, in another tenant, is
// another namespace.
//
// The port-level assertion alone would be worthless here: keyvault refuses a
// ref presented under the wrong workspace all by itself (Ref.scopedTo), so
// Get would answer ErrSecretNotFound even with the tenant predicate AND the
// RLS policy both removed — it would be testing keyvault, not this store and
// not the database. So the database layer is pinned first and directly: the
// mapping row must be invisible to a session bound to the other tenant.
func TestSecretsAreWorkspaceScoped(t *testing.T) {
	e := setup(t)
	s := extsecrets.For("notes", e.pool, e.vault)

	if err := s.Put(e.ctxFor(e.ws), "signing", []byte("tenant-a")); err != nil {
		t.Fatal(err)
	}
	if n := e.rowsVisibleFrom(t, e.ws, "notes", "signing"); n != 1 {
		t.Fatalf("the owning workspace sees %d mapping rows, want 1 — the fixture is not proving anything", n)
	}
	if n := e.rowsVisibleFrom(t, e.otherWS, "notes", "signing"); n != 0 {
		t.Fatalf("a session bound to another workspace sees %d of this one's mapping rows, want 0", n)
	}
	if _, err := s.Get(e.ctxFor(e.otherWS), "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("another workspace read this one's secret: err=%v", err)
	}
}

// TestSecretsRefuseUnusableInput: the failures a caller can fix, named.
func TestSecretsRefuseUnusableInput(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)

	if err := s.Put(ctx, "", []byte("x")); !errors.Is(err, extsecrets.ErrInvalidKey) {
		t.Fatalf("empty key: err=%v, want ErrInvalidKey", err)
	}
	if _, err := s.Get(ctx, "line\nbreak"); !errors.Is(err, extsecrets.ErrInvalidKey) {
		t.Fatalf("control character in key: err=%v, want ErrInvalidKey", err)
	}
	if _, err := s.GetUser(ctx, extension.UserID("not-a-uuid"), "token"); !errors.Is(err, extsecrets.ErrInvalidUserID) {
		t.Fatalf("malformed user id: err=%v, want ErrInvalidUserID", err)
	}
	if _, err := s.Get(context.Background(), "signing"); !errors.Is(err, database.ErrNoWorkspace) {
		t.Fatalf("unbound workspace: err=%v, want ErrNoWorkspace", err)
	}
	if err := extsecrets.For("notes", e.pool, nil).Put(ctx, "signing", []byte("x")); !errors.Is(err, extsecrets.ErrNoCustodian) {
		t.Fatalf("no vault configured: err=%v, want ErrNoCustodian", err)
	}
}

// TestSecretsLeaveAnAuditTrail: a secret changing hands is an operator-
// visible event even though no domain row moved, so each operation appends
// to the non-entity ledger.
//
// The read that finds NOTHING is the load-bearing entry. A unit probing key
// names it does not own is the thing an operator asks the ledger about after
// a unit misbehaves, and a miss that left no trace would answer that question
// with silence — so it commits its row and the refusal is raised afterwards.
func TestSecretsLeaveAnAuditTrail(t *testing.T) {
	e := setup(t)
	ctx := e.ctxFor(e.ws)
	s := extsecrets.For("notes", e.pool, e.vault)

	if _, err := s.Get(ctx, "never-stored"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("probing an absent key: err=%v, want ErrSecretNotFound", err)
	}
	if err := s.Put(ctx, "signing", []byte("sekrit-alpha")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "signing", []byte("sekrit-beta")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "signing"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "signing"); err != nil {
		t.Fatal(err)
	}
	// A refused write is deliberately NOT recorded: its transaction rolled
	// back, and a row for it would assert a secret changed hands when none
	// did. Nothing is appended between the delete above and the assertion.
	if err := s.Delete(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("second delete: err=%v, want ErrSecretNotFound", err)
	}

	want := []string{
		"extension.secret_read/missing",
		"extension.secret_stored",
		"extension.secret_rotated",
		"extension.secret_read/resolved",
		"extension.secret_deleted",
	}
	got := e.systemLogActions(t, e.ws)
	if len(got) != len(want) {
		t.Fatalf("system_log actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("system_log actions = %v, want %v", got, want)
		}
	}

	// The ledger records WHAT changed hands, never the secret itself.
	if _, err := e.owner.Exec(context.Background(),
		`SELECT set_config('app.workspace_id', $1, false)`, e.ws.String()); err != nil {
		t.Fatal(err)
	}
	var leaked bool
	if err := e.owner.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM system_log WHERE workspace_id = $1 AND detail::text LIKE '%sekrit%')`,
		e.ws).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("a system_log detail carries the secret's plaintext")
	}
}
