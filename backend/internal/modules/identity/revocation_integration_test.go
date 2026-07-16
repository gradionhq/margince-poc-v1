// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The B-EP03.10 / B-E10.4 revocation cascade and the §27 lockout, proven
// over a real migrated Postgres: a deactivated user's sessions and
// passports die in the same transaction that emits user.deactivated; a
// revoked passport is refused on its very next call (the per-call
// re-auth IS the agent-side kill — no subscriber, nothing to go stale);
// and five bad passwords lock even the correct one out until the RC-17
// window passes.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// identityDB connects once per test binary to the already-migrated
// database (`make migrate` — the integration lane's precondition, same
// as every module-local fixture); every test then bootstraps its own
// workspace, so the suites stay independent.
var identityDB struct {
	once  sync.Once
	owner *pgx.Conn
	pool  *pgxpool.Pool
	err   error
}

func setupIdentityDB(t *testing.T) (*pgx.Conn, *pgxpool.Pool) {
	t.Helper()
	identityDB.once.Do(func() {
		ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
		appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
		if ownerDSN == "" || appDSN == "" {
			identityDB.err = errors.New("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
			return
		}
		ctx := context.Background()
		owner, err := pgx.Connect(ctx, ownerDSN)
		if err != nil {
			identityDB.err = err
			return
		}
		identityDB.owner = owner
		identityDB.pool, identityDB.err = database.NewPool(ctx, appDSN)
	})
	if identityDB.err != nil {
		t.Fatal(identityDB.err)
	}
	return identityDB.owner, identityDB.pool
}

// revocationEnv is one bootstrapped workspace: an admin (with session)
// plus a plain second user with a known password.
type revocationEnv struct {
	owner  *pgx.Conn
	svc    *Service
	admin  Identity
	member Identity
}

const memberPassword = "correct horse battery staple"

func setupRevocationEnv(t *testing.T, slug string) *revocationEnv {
	t.Helper()
	owner, pool := setupIdentityDB(t)
	svc := NewService(pool)
	ctx := context.Background()
	// The database persists across binary runs; key the slug (and the
	// emails derived from it) uniquely so reruns never collide.
	slug += "-" + ids.NewV7().String()[:8]

	admin, _, err := svc.Bootstrap(ctx, BootstrapInput{
		WorkspaceName: slug, Slug: slug,
		AdminEmail: "admin@" + slug + ".test", AdminName: "Admin",
		AdminPassword: "a bootstrap password!",
	}, nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	hash, err := password.Hash(memberPassword)
	if err != nil {
		t.Fatal(err)
	}
	memberID := ids.New[ids.UserKind]()
	memberEmail := "member@" + slug + ".test"
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, password_hash, display_name)
		 VALUES ($1, $2, $3, $4, 'Member')`,
		memberID, admin.WorkspaceID, memberEmail, hash); err != nil {
		t.Fatal(err)
	}

	return &revocationEnv{
		owner: owner, svc: svc, admin: admin,
		member: Identity{UserID: memberID, WorkspaceID: admin.WorkspaceID, Email: memberEmail},
	}
}

// wsCtx binds workspace + acting human + a correlation scope — what the
// HTTP middleware binds before any service call.
func (e *revocationEnv) wsCtx(id Identity) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), id.WorkspaceID.UUID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + id.UserID.String(), UserID: id.UserID.UUID,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// identityEvents returns the identity-stream envelopes of one type,
// oldest first — the §5.6a cascade facts the outbox staged.
func (e *revocationEnv) identityEvents(t *testing.T, eventType string) []events.Envelope {
	t.Helper()
	rows, err := e.owner.Query(context.Background(),
		`SELECT envelope FROM event_outbox WHERE stream = 'gw:events:crm:identity' ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Envelope
	for _, raw := range raws {
		var env events.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("outbox envelope does not parse: %v", err)
		}
		if env.Type == eventType && env.WorkspaceID == e.admin.WorkspaceID.UUID {
			out = append(out, env)
		}
	}
	return out
}

func TestDeactivateUserRevokesSessionsAndPassportsAndEmits(t *testing.T) {
	e := setupRevocationEnv(t, "revoke-cascade")
	ctx := principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)

	_, sessionToken, err := e.svc.Login(ctx, e.member.Email, memberPassword)
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	issued, err := e.svc.IssuePassport(ctx, e.member, IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("issue passport: %v", err)
	}
	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); err != nil {
		t.Fatalf("passport must authenticate before deactivation: %v", err)
	}

	reason := "left the company"
	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{
		UserID: e.member.UserID, Reason: &reason,
	}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	if _, err := e.svc.Authenticate(ctx, sessionToken); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("deactivated user's session authenticates: err = %v, want not-found", err)
	}
	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("deactivated user's passport authenticates: err = %v, want not-found", err)
	}

	// The cascade is durable rows, not just the live re-auth refusal.
	var liveSessions, livePassports int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM session  WHERE user_id = $1 AND revoked_at IS NULL),
		        (SELECT count(*) FROM passport WHERE on_behalf_of = $1 AND revoked_at IS NULL)`,
		e.member.UserID).Scan(&liveSessions, &livePassports); err != nil {
		t.Fatal(err)
	}
	if liveSessions != 0 || livePassports != 0 {
		t.Errorf("deactivation left %d live sessions, %d live passports; want 0, 0", liveSessions, livePassports)
	}

	envs := e.identityEvents(t, "user.deactivated")
	if len(envs) != 1 {
		t.Fatalf("user.deactivated staged %d times, want exactly once", len(envs))
	}
	var payload struct {
		UserID ids.UserID `json:"user_id"`
		By     ids.UserID `json:"by"`
		Reason string     `json:"reason"`
	}
	if err := json.Unmarshal(envs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != e.member.UserID || payload.By != e.admin.UserID || payload.Reason != reason {
		t.Errorf("user.deactivated payload = %+v, want the §5.6a {user_id, by, reason} facts", payload)
	}
	if envs[0].Trace.AuditLogID.IsZero() {
		t.Error("user.deactivated carries no audit_log_id — the write shape demands the linked audit row")
	}

	// Idempotent: a second deactivation neither errors nor re-publishes.
	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: e.member.UserID}); err != nil {
		t.Fatalf("repeat deactivate: %v", err)
	}
	if again := e.identityEvents(t, "user.deactivated"); len(again) != 1 {
		t.Errorf("repeat deactivation staged a duplicate event (%d total)", len(again))
	}

	// The gate itself: a non-admin cannot deactivate anyone.
	if err := e.svc.DeactivateUser(e.wsCtx(e.member), e.member, DeactivateUserInput{UserID: e.admin.UserID}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("non-admin deactivation: err = %v, want permission denied", err)
	}
}

// TestRevokedPassportRefusedOnNextCall is the B-E10.4 cascade evidence:
// every agent call re-authenticates the passport row, so revocation
// binds on the immediately following call — structurally within one bus
// cycle, with no cache to invalidate.
func TestRevokedPassportRefusedOnNextCall(t *testing.T) {
	e := setupRevocationEnv(t, "revoke-passport")
	ctx := principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)

	issued, err := e.svc.IssuePassport(ctx, e.admin, IssuePassportInput{Scopes: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); err != nil {
		t.Fatalf("fresh passport refused: %v", err)
	}

	if err := e.svc.RevokePassport(e.wsCtx(e.admin), e.admin, issued.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("revoked passport authenticates on the next call: err = %v, want not-found", err)
	}
	if _, err := e.svc.AuthenticateAgentByID(ctx, issued.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("revoked passport resolves by id on the next call: err = %v, want not-found", err)
	}

	envs := e.identityEvents(t, "passport.revoked")
	if len(envs) != 1 {
		t.Fatalf("passport.revoked staged %d times, want exactly once", len(envs))
	}
	var payload struct {
		PassportID ids.PassportID `json:"passport_id"`
		By         ids.UserID     `json:"by"`
	}
	if err := json.Unmarshal(envs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PassportID != issued.ID || payload.By != e.admin.UserID {
		t.Errorf("passport.revoked payload = %+v, want {passport_id %s, by %s}", payload, issued.ID, e.admin.UserID)
	}
}

func TestChangeUserRoleReplacesAssignmentAndEmits(t *testing.T) {
	e := setupRevocationEnv(t, "role-change")

	if err := e.svc.ChangeUserRole(e.wsCtx(e.admin), e.admin, e.member.UserID, "rep"); err != nil {
		t.Fatalf("assign first role: %v", err)
	}
	if err := e.svc.ChangeUserRole(e.wsCtx(e.admin), e.admin, e.member.UserID, "manager"); err != nil {
		t.Fatalf("change role: %v", err)
	}
	if err := e.svc.ChangeUserRole(e.wsCtx(e.admin), e.admin, e.member.UserID, "no-such-role"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("unknown role: err = %v, want not-found", err)
	}
	if err := e.svc.ChangeUserRole(e.wsCtx(e.member), e.member, e.admin.UserID, "read_only"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("non-admin role change: err = %v, want permission denied", err)
	}

	var keys []string
	rows, err := e.owner.Query(context.Background(),
		`SELECT r.key FROM role_assignment ra JOIN role r ON r.id = ra.role_id WHERE ra.user_id = $1`,
		e.member.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if keys, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "manager" {
		t.Errorf("role assignments after change = %v, want exactly [manager]", keys)
	}

	envs := e.identityEvents(t, "role.changed")
	if len(envs) != 2 {
		t.Fatalf("role.changed staged %d times, want one per effective change", len(envs))
	}
	var payload struct {
		UserID   ids.UserID `json:"user_id"`
		FromRole string     `json:"from_role"`
		ToRole   string     `json:"to_role"`
		By       ids.UserID `json:"by"`
	}
	if err := json.Unmarshal(envs[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != e.member.UserID || payload.FromRole != "rep" || payload.ToRole != "manager" || payload.By != e.admin.UserID {
		t.Errorf("role.changed payload = %+v, want rep→manager by the admin", payload)
	}
}

func TestLoginLockoutEndToEnd(t *testing.T) {
	e := setupRevocationEnv(t, "lockout")
	ctx := principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)

	// The injected clock starts at real time (the DB stamps updated_at
	// with its own now()) and only ever moves forward.
	var offset time.Duration
	e.svc.now = func() time.Time { return time.Now().Add(offset) }

	for attempt := 1; attempt < lockoutThreshold; attempt++ {
		if _, _, err := e.svc.Login(ctx, e.member.Email, "wrong password"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("failure %d: err = %v, want bad credentials", attempt, err)
		}
	}
	// Below the threshold the correct password still works — and resets
	// the streak, so the count restarts from zero.
	if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); err != nil {
		t.Fatalf("login below threshold: %v", err)
	}
	var count int
	var lockedUntil *time.Time
	if err := e.owner.QueryRow(context.Background(),
		`SELECT failed_login_count, locked_until FROM app_user WHERE id = $1`,
		e.member.UserID).Scan(&count, &lockedUntil); err != nil {
		t.Fatal(err)
	}
	if count != 0 || lockedUntil != nil {
		t.Fatalf("success did not reset the streak: count=%d locked_until=%v", count, lockedUntil)
	}

	for attempt := 1; attempt <= lockoutThreshold; attempt++ {
		if _, _, err := e.svc.Login(ctx, e.member.Email, "wrong password"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("failure %d: err = %v, want bad credentials", attempt, err)
		}
	}
	// Locked: even the correct password refuses with the 403 sentinel.
	if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("locked account: err = %v, want permission denied", err)
	}
	var outcome string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT detail->>'outcome' FROM system_log
		 WHERE action = 'login' AND detail->>'email' = $1
		 ORDER BY id DESC LIMIT 1`, e.member.Email).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "lockout" {
		t.Errorf("last failure audited as %q, want the §27 'lockout' outcome", outcome)
	}

	// After the RC-17 duration the lock has expired and the correct
	// password opens a session again.
	offset = lockoutDuration + time.Minute
	if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); err != nil {
		t.Fatalf("login after lock expiry: %v", err)
	}
}

func TestNonActiveStatusesCannotLogIn(t *testing.T) {
	e := setupRevocationEnv(t, "status-gate")
	ctx := principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)

	for _, status := range []string{"invited", "suspended", "deactivated"} {
		t.Run(status, func(t *testing.T) {
			if _, err := e.owner.Exec(context.Background(),
				`UPDATE app_user SET status = $2 WHERE id = $1`, e.member.UserID, status); err != nil {
				t.Fatal(err)
			}
			// The correct password is refused indistinguishably from a bad
			// one — a non-active account must not even disclose it exists.
			if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("%s user logged in: err = %v, want bad credentials", status, err)
			}
		})
	}

	if _, err := e.owner.Exec(context.Background(),
		`UPDATE app_user SET status = 'active' WHERE id = $1`, e.member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); err != nil {
		t.Fatalf("reactivated user cannot log in: %v", err)
	}
}
