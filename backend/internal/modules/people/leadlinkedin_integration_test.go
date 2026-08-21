// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The lead's LinkedIn identity key over a real migrated Postgres
// (E12.11): create normalizes the profile URL ONCE so every spelling of
// the same profile stores one canonical form, the exact-match dedupe
// probe finds that form from any messy input, and — because the probe
// returns a record — it is a read: an out-of-scope or foreign-workspace
// match reads as no match, and a non-URL refuses as the caller's fault.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// linkedinEnv is this suite's fixture over the already-migrated
// database: one fresh workspace, two teamless users, and the store.
type linkedinEnv struct {
	owner      *pgx.Conn
	store      *Store
	ws         ids.UUID
	rep1, rep2 ids.UUID
}

func setupLeadLinkedIn(t *testing.T) *linkedinEnv {
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
	// To head before anything else touches this database: testdb.Pool refuses
	// until EnsureSchema has run, and EnsureSchema still REBUILDS whenever it
	// cannot prove the database is a fresh lane clone — so a seed written
	// before it would be dropped rather than reset.
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// Every test in this package seeds its own workspace into ONE database,
	// and what used to keep their rows apart was deny-on-unset RLS. With
	// tenant isolation retired (ADR-0091 §8 phase A) the separation has to be
	// real: reset before seeding, as compose/integration's harness does.
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &linkedinEnv{owner: owner, ws: ids.NewV7(), rep1: ids.NewV7(), rep2: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		e.ws, "li-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	for i, user := range []ids.UUID{e.rep1, e.rep2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, email, display_name) VALUES ($1, $2, 'Rep')`, user, "rep"+string(rune('1'+i))+"-"+user.String()+"@li.test"); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := testdb.Pool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Registered where the pool is handed out, before the test adds any cleanup
	// of its own, so it runs last and sees a package that has genuinely stopped.
	// The pool outlives the test now, so a goroutine still holding a connection
	// would go on writing into the database the NEXT test just reset.
	t.Cleanup(func() { testdb.AssertPoolsQuiesced(t) })
	e.store = NewStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](e.ws)))
	return e
}

// as binds a lead-granted principal at the given row scope.
func (e *linkedinEnv) as(user ids.UUID, scope principal.RowScope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"lead": {Create: true, Read: true, Update: true},
			},
			RowScope: scope,
		},
	})
}

func strPtr(s string) *string { return &s }

func TestCreateLeadNormalizesTheLinkedInKeyOnce(t *testing.T) {
	e := setupLeadLinkedIn(t)
	ctx := e.as(e.rep1, principal.RowScopeAll)

	// One profile, four spellings: uppercase host, tracking query,
	// trailing slash, surrounding noise.
	messy := "  https://WWW.LinkedIn.com/in/vera-vp/?utm_source=share  "
	canonical := "https://www.linkedin.com/in/vera-vp"

	created, wasCreated, err := e.store.CreateLead(ctx, CreateLeadInput{
		FullName: strPtr("Vera VP"), LinkedInURL: strPtr(messy), Source: "manual",
	})
	if err != nil || !wasCreated {
		t.Fatalf("create lead: created=%v err=%v", wasCreated, err)
	}
	if created.LinkedinUrl == nil || *created.LinkedinUrl != canonical {
		t.Fatalf("stored linkedin_url = %v, want the one canonical spelling %q", created.LinkedinUrl, canonical)
	}
	fetched, err := e.store.GetLead(ctx, ids.From[ids.LeadKind](ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if fetched.LinkedinUrl == nil || *fetched.LinkedinUrl != canonical {
		t.Fatalf("read-back linkedin_url = %v, want %q", fetched.LinkedinUrl, canonical)
	}

	// The dedupe probe finds the canonical row from ANOTHER messy spelling.
	match, err := e.store.FindLeadByLinkedInURL(ctx, "http://www.linkedin.com:443/in/vera-vp#about")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if match == nil || match.Id != created.Id {
		t.Fatalf("probe found %v, want the created lead %s", match, created.Id)
	}

	// A different profile is honestly absent.
	miss, err := e.store.FindLeadByLinkedInURL(ctx, "https://www.linkedin.com/in/someone-else")
	if err != nil {
		t.Fatalf("probe for an absent profile: %v", err)
	}
	if miss != nil {
		t.Fatalf("probe for an absent profile returned %s", miss.Id)
	}
}

func TestLeadLinkedInRefusesNonURLsAsTheCallersFault(t *testing.T) {
	e := setupLeadLinkedIn(t)
	ctx := e.as(e.rep1, principal.RowScopeAll)

	var parseErr *values.ParseError
	if _, _, err := e.store.CreateLead(ctx, CreateLeadInput{
		FullName: strPtr("No Profile"), LinkedInURL: strPtr("ftp://linkedin.com/in/x"), Source: "manual",
	}); !errors.As(err, &parseErr) {
		t.Fatalf("create with a non-http URL → %v, want a values.ParseError (422)", err)
	}
	if n := e.count(t, `SELECT count(*) FROM lead`); n != 0 {
		t.Fatalf("a refused create left %d lead rows", n)
	}
	if _, err := e.store.FindLeadByLinkedInURL(ctx, "://nope"); !errors.As(err, &parseErr) {
		t.Fatalf("probe with a non-URL → %v, want a values.ParseError", err)
	}
}

func (e *linkedinEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}
