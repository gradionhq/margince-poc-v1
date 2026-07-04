//go:build integration

package main

// The Agent Seat Passport lifecycle (data-model §2.7, ADR-0043): mint,
// authenticate, the agent≤human bound, revoke, expire — against the real
// migrated schema, through the same crmauth.Service the middleware uses.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmauth "github.com/gradionhq/margince/backend/crm-auth"
	"github.com/gradionhq/margince/backend/internal/pg"
	"github.com/gradionhq/margince/backend/internal/pgmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

// passportEnv migrates a fresh schema and bootstraps one workspace,
// returning the service and the admin identity.
func passportEnv(t *testing.T) (*crmauth.Service, crmauth.Identity) {
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
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatal(err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	pool, err := pg.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	svc := crmauth.NewService(pool)
	admin, _, err := svc.Bootstrap(ctx, crmauth.BootstrapInput{
		WorkspaceName: "Passport Test", Slug: "passport-test",
		AdminEmail: "admin@passport.test", AdminName: "Admin",
		AdminPassword: "correct-horse-battery",
	}, nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return svc, admin
}

func wsCtx(id crmauth.Identity) context.Context {
	return principal.WithWorkspaceID(context.Background(), id.WorkspaceID)
}

func TestPassportLifecycle(t *testing.T) {
	svc, admin := passportEnv(t)
	ctx := wsCtx(admin)

	label := "Claude Desktop"
	issued, err := svc.IssuePassport(ctx, admin, crmauth.IssuePassportInput{
		Label: &label, Scopes: []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(issued.Token, "mgp_") {
		t.Errorf("token %q lacks the mgp_ marker prefix", issued.Token)
	}

	agent, err := svc.AuthenticateAgent(ctx, issued.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if agent.OnBehalfOf != admin.UserID {
		t.Error("passport does not act on behalf of its issuer")
	}
	if !agent.Scopes.Has(principal.ScopeRead) || !agent.Scopes.Has(principal.ScopeWrite) || agent.Scopes.Has(principal.ScopeSend) {
		t.Errorf("scopes = %v, want exactly read+write", agent.Scopes)
	}
	// agent ≤ human: the principal carries the ISSUER's live RBAC.
	p := agent.Principal()
	if p.Type != principal.PrincipalAgent || !p.Permissions.Allows("person", principal.ActionCreate) {
		t.Error("agent principal did not inherit the granting admin's RBAC")
	}
	if p.PassportID != issued.ID || p.OnBehalfOf != admin.UserID {
		t.Error("principal lost the passport attribution the audit trail needs")
	}

	// Revocation binds at the next lookup — the kill switch.
	if err := svc.RevokePassport(ctx, admin, issued.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("revoked token authenticated: %v", err)
	}
	// Idempotent revoke.
	if err := svc.RevokePassport(ctx, admin, issued.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
}

// C5: bootstrap is atomic across identity AND cross-module defaults. A
// seed that fails must roll the whole tenant back — no workspace row, no
// admin, nothing to collide with on retry.
func TestBootstrapSeedFailureRollsBackTenant(t *testing.T) {
	svc, _ := passportEnv(t)
	ctx := context.Background()

	boom := errors.New("seed blew up")
	_, _, err := svc.Bootstrap(ctx, crmauth.BootstrapInput{
		WorkspaceName: "Atomic Test", Slug: "atomic-test",
		AdminEmail: "admin@atomic.test", AdminName: "Admin",
		AdminPassword: "correct-horse-battery",
	}, func(_ context.Context, _ pgx.Tx) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("bootstrap surfaced %v, want the seed error", err)
	}

	// The tenant must not exist: the failed seed rolled it back.
	if _, err := svc.ResolveWorkspace(ctx, "atomic-test"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("workspace survived a failed seed → %v, want ErrNotFound (partial provisioning)", err)
	}

	// And the slug is free — a retry with a working seed succeeds.
	if _, _, err := svc.Bootstrap(ctx, crmauth.BootstrapInput{
		WorkspaceName: "Atomic Test", Slug: "atomic-test",
		AdminEmail: "admin@atomic.test", AdminName: "Admin",
		AdminPassword: "correct-horse-battery",
	}, nil); err != nil {
		t.Fatalf("retry after rollback failed: %v", err)
	}
}

func TestPassportRefusesBadScopesAndExpiry(t *testing.T) {
	svc, admin := passportEnv(t)
	ctx := wsCtx(admin)

	var badScope *crmauth.InvalidScopeError
	if _, err := svc.IssuePassport(ctx, admin, crmauth.IssuePassportInput{Scopes: []string{"admin"}}); !errors.As(err, &badScope) {
		t.Fatalf("scope outside the verb vocabulary → %v", err)
	}
	if _, err := svc.IssuePassport(ctx, admin, crmauth.IssuePassportInput{Scopes: nil}); !errors.As(err, &badScope) {
		t.Fatalf("empty scopes → %v", err)
	}
	over := 91 * 24 * time.Hour
	if _, err := svc.IssuePassport(ctx, admin, crmauth.IssuePassportInput{Scopes: []string{"read"}, TTL: &over}); !errors.As(err, &badScope) {
		t.Fatalf("ttl over the 90-day cap → %v", err)
	}

	// An expired passport reads as absent.
	short := time.Second
	issued, err := svc.IssuePassport(ctx, admin, crmauth.IssuePassportInput{Scopes: []string{"read"}, TTL: &short})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expired token authenticated: %v", err)
	}
}
