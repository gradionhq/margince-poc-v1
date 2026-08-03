// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package privacy

// The Art. 15 export's identifier sections over a real migrated Postgres.
//
// All three sections export ARCHIVED rows alongside live ones, deliberately:
// Art. 15 owes what is HELD, and a retired address, number or channel binding
// is still a record the installation keeps about the subject. That is only
// honest if the export says which is which. Without the archival state the
// package hands a subject a list of identifiers that all read as current, so
// they cannot tell an address they asked to have retired from one this
// installation would still write to — and the section they would use to check
// that the retirement happened is the section that hides it.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sarIdentifierEnv is one workspace holding one subject who carries a LIVE and
// a RETIRED identifier of each kind the export projects.
type sarIdentifierEnv struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	person ids.PersonID
}

// retiredAt is the archival instant every retired row below carries. A fixed
// past timestamp, not now(): the assertions only ask whether the state reached
// the export, and a literal keeps the fixture readable.
var retiredAt = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func setupSARIdentifiers(t *testing.T) *sarIdentifierEnv {
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

	ws, user := ids.NewV7(), ids.NewV7()
	person := ids.New[ids.PersonKind]()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'SAR identifiers', $2, 'EUR')`,
		ws, "sar-"+ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Admin')`,
		user, ws, "admin-"+user.String()+"@sar.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		 VALUES ($1, $2, 'Sara Subject', 'manual', 'user:'||$3::text)`,
		person, ws, user); err != nil {
		t.Fatal(err)
	}
	seedIdentifierPairs(ctx, t, owner, ws, person)

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	return &sarIdentifierEnv{ctx: exportContext(ws, user), pool: pool, person: person}
}

// seedIdentifierPairs gives the subject one live and one retired row of each
// kind. The two values in a pair differ because the live-row uniqueness indexes
// are partial on archived_at IS NULL — reusing one value would leave the export
// unable to say which row it named.
func seedIdentifierPairs(ctx context.Context, t *testing.T, owner *pgx.Conn, ws ids.UUID, person ids.PersonID) {
	t.Helper()
	for _, insert := range []struct {
		statement     string
		live, retired string
	}{
		{
			`INSERT INTO person_email (workspace_id, person_id, email, source, captured_by, archived_at)
		  VALUES ($1, $2, $3, 'manual', 'user:test', NULL), ($1, $2, $4, 'manual', 'user:test', $5)`,
			liveEmail, retiredEmail,
		},
		{
			`INSERT INTO person_phone (workspace_id, person_id, phone, source, captured_by, archived_at)
		  VALUES ($1, $2, $3, 'manual', 'user:test', NULL), ($1, $2, $4, 'manual', 'user:test', $5)`,
			livePhone, retiredPhone,
		},
		{
			`INSERT INTO person_channel_identity
		    (workspace_id, person_id, provider, channel_user_id, username, source, captured_by, archived_at)
		  VALUES ($1, $2, 'telegram', $3, 'sara', 'connector:telegram', 'connector:telegram', NULL),
		         ($1, $2, 'telegram', $4, 'sara_old', 'connector:telegram', 'connector:telegram', $5)`,
			liveAccount, retiredAccount,
		},
	} {
		if _, err := owner.Exec(ctx, insert.statement, ws, person, insert.live, insert.retired, retiredAt); err != nil {
			t.Fatal(err)
		}
	}
}

// exportContext is the caller AssembleSAR demands: admin-mediated means the
// person.delete grant AND an unbounded row scope, since the assembly crosses
// every rep's slice on purpose.
func exportContext(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"person": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// The subject's identifiers, one live and one retired per kind.
const (
	liveEmail      = "sara.live@sar.test"
	retiredEmail   = "sara.retired@sar.test"
	livePhone      = "+493011111111"
	retiredPhone   = "+493022222222"
	liveAccount    = "770000001"
	retiredAccount = "770000002"
)

// TestTheSARExportDistinguishesARetiredBindingFromALiveOne walks all three
// identifier sections, because the obligation is the same in each: the section
// exports archived rows, so it owes the archival state that tells them apart.
func TestTheSARExportDistinguishesARetiredBindingFromALiveOne(t *testing.T) {
	e := setupSARIdentifiers(t)

	pkg, err := AssembleSAR(e.ctx, e.pool, e.person)
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	for _, section := range []struct {
		name    string
		rows    []map[string]any
		key     string
		live    string
		retired string
	}{
		{"emails", pkg.Emails, "email", liveEmail, retiredEmail},
		{"phones", pkg.Phones, "phone", livePhone, retiredPhone},
		{"channel identities", pkg.ChannelIdentities, "channel_user_id", liveAccount, retiredAccount},
	} {
		t.Run(section.name, func(t *testing.T) {
			byIdentifier := map[string]map[string]any{}
			for _, row := range section.rows {
				identifier, ok := row[section.key].(string)
				if !ok {
					t.Fatalf("a %s row carries no %s: %v", section.name, section.key, row)
				}
				byIdentifier[identifier] = row
			}

			live, ok := byIdentifier[section.live]
			if !ok {
				t.Fatalf("the live %s is missing from the export: %v", section.name, section.rows)
			}
			retired, ok := byIdentifier[section.retired]
			if !ok {
				t.Fatalf("the retired %s is missing from the export — Art. 15 owes what is held: %v", section.name, section.rows)
			}

			state, ok := retired["archived_at"]
			if !ok {
				t.Fatalf("the retired %s exports no archived_at, so it reads as reachable as the live one: %v", section.name, retired)
			}
			if state == nil {
				t.Errorf("the retired %s exports archived_at = NULL, want the retirement instant", section.name)
			}
			if state, ok := live["archived_at"]; !ok || state != nil {
				t.Errorf("the live %s exports archived_at = %v (present: %t), want a present NULL", section.name, state, ok)
			}
		})
	}
}
