// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// The RBAC backfill migrations are the ONLY thing that gives an existing
// installation a grant on an object added after it bootstrapped: the code-side
// seed (identity.seedSystemRoles) writes the defaults once at workspace
// creation and never re-syncs. A backfill that writes the wrong grant, targets
// the wrong roles, or silently matches no rows is therefore indistinguishable
// from a correct one until a real user gets a 403 in production.
//
// This test EXECUTES each backfill against a fixture rather than reading its
// SQL. A static check on the JSON payload would pass a migration whose WHERE
// clause targeted the wrong roles, omitted is_system, or used the wrong JSON
// path — the payload is the part least likely to be wrong.
//
// The expected grants below are hand-written on purpose. Deriving them from
// identity/internal/policy would test that package against itself; an
// independent transcription is what makes this a check rather than a mirror.
// (That package is also import-fenced: `internal/` under identity.)

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// grant is one role's CRUD row inside a permissions document.
type grant struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

var (
	crud     = grant{Create: true, Read: true, Update: true, Delete: true}
	cru      = grant{Create: true, Read: true, Update: true}
	readOnly = grant{Read: true}
)

// rbacBackfill pins one migration's intended outcome: after it runs, each
// system role holds exactly this grant on the object.
type rbacBackfill struct {
	object  string
	version string
	want    map[string]grant
}

// backfilledObjects are the objects whose grants reach existing installations
// through a migration. Each entry is proved end-to-end below.
var backfilledObjects = []rbacBackfill{
	{
		object: "saved_view", version: "0179",
		// A saved view is per-user view state, not a shared record — full
		// self-service for every role, read_only included.
		want: map[string]grant{
			"admin": crud, "ops": crud, "manager": crud, "rep": crud, "read_only": crud,
		},
	},
	{
		object: "webhook_subscription", version: "0180",
		// Outbound egress of governed events is workspace integration config:
		// admin/ops manage the fan-out, everyone reads delivery health.
		want: map[string]grant{
			"admin": crud, "ops": crud,
			"manager": readOnly, "rep": readOnly, "read_only": readOnly,
		},
	},
	{
		object: "relationship", version: "0181",
		// Record posture: a rep creates and maintains, deletion stays with
		// manager and above.
		want: map[string]grant{
			"admin": crud, "ops": crud, "manager": crud,
			"rep": cru, "read_only": readOnly,
		},
	},
	{
		object: "partner", version: "0182",
		// A partner is a business relationship, not a rep's working record —
		// the rep tier is read-only, unlike person/organization/deal.
		want: map[string]grant{
			"admin": crud, "ops": crud, "manager": crud,
			"rep": readOnly, "read_only": readOnly,
		},
	},
	{
		object: "list", version: "0183",
		// Everyday organizational surfaces: a rep creates and uses them,
		// archiving stays with manager and above.
		want: map[string]grant{
			"admin": crud, "ops": crud, "manager": crud,
			"rep": cru, "read_only": readOnly,
		},
	},
	{
		object: "tag", version: "0183",
		want: map[string]grant{
			"admin": crud, "ops": crud, "manager": crud,
			"rep": cru, "read_only": readOnly,
		},
	},
}

// systemRoleKeys are the five seeded role keys every backfill targets.
var systemRoleKeys = []string{"admin", "ops", "manager", "rep", "read_only"}

// alteredGrant is deliberately unlike every seeded default, so a fixture row
// carrying it can only survive by the migration's only-if-absent guard
// skipping it — never by coincidentally matching what the migration writes.
var alteredGrant = grant{Read: true, Delete: true}

func TestRBACBackfillsWriteTheIntendedGrants(t *testing.T) {
	ctx := context.Background()
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	// Migrated by the NON-SUPERUSER owner, because this suite's whole subject is
	// what the backfills WRITE, and under the ordinary owner FORCE row-level
	// security binds them. Run as the container superuser the backfills land by
	// bypassing the policy, and the roll-back-and-re-apply below would prove the
	// grants correct on the one executor production never uses. `conn` stays the
	// superuser for seeding and reading, which need the cross-workspace view.
	migrator := asMigrator(t, conn)
	if _, err := dbmigrate.Up(ctx, migrator, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	rollBackTo(ctx, t, migrator, core, backfilledObjects[0].version)

	// Four workspaces, one per upgrade shape the backfills must survive.
	missing := seedWorkspace(t, conn, "rbac-backfill-missing")
	present := seedWorkspace(t, conn, "rbac-backfill-present")
	custom := seedWorkspace(t, conn, "rbac-backfill-custom")
	empty := seedWorkspace(t, conn, "rbac-backfill-empty")

	for _, key := range systemRoleKeys {
		// The real upgrade path: system roles holding a populated document that
		// predates every backfilled object.
		seedRole(t, conn, missing, key, true, policyDocument(nil))
		seedRole(t, conn, present, key, true, policyDocument(backfilledObjects))
		// Non-system roles carrying the SAME five keys. A custom role named
		// something else would prove nothing: dropping `is_system` while keeping
		// `key IN ('admin', …)` would still miss it. Only a key collision makes
		// the predicate the single thing standing between the migration and
		// these rows.
		seedRole(t, conn, custom, key, false, policyDocument(nil))
	}
	// An installation whose document is a literal '{}': jsonb_set cannot create
	// the missing `objects` parent, and the `?` guard NULL-skips the row. The
	// two must stay aligned — if a future migration drops the guard, jsonb_set
	// would silently no-op and the object would be missing with nothing failing.
	seedRole(t, conn, empty, "admin", true, []byte(`{}`))
	archiveAllButOne(t, conn)

	if _, err := dbmigrate.Up(ctx, migrator, core); err != nil {
		t.Fatalf("re-applying the backfills: %v", err)
	}

	for _, bf := range backfilledObjects {
		t.Run(bf.object, func(t *testing.T) {
			// Errorf, not Fatalf: one missing role must not hide the other four,
			// nor the guard/is_system/'{}' assertions below.
			for _, role := range systemRoleKeys {
				got, found := readGrant(ctx, t, conn, missing, role, bf.object)
				if !found {
					t.Errorf("%s: role %q holds no %s grant after the backfill; an existing "+
						"installation would 403 on every %s route", bf.version, role, bf.object, bf.object)
					continue
				}
				if want := bf.want[role]; got != want {
					t.Errorf("%s: role %q got %s grant %+v, want %+v", bf.version, role, bf.object, got, want)
				}
			}

			// The only-if-absent guard must preserve a grant an operator or an
			// earlier release already set. Checked for EVERY role: these
			// migrations are multi-statement and each statement carries its own
			// guard, so an admin-only assertion would miss a guard dropped from
			// the rep or read_only statement.
			for _, role := range systemRoleKeys {
				kept, found := readGrant(ctx, t, conn, present, role, bf.object)
				if !found || kept != alteredGrant {
					t.Errorf("%s: pre-existing %s %s grant was overwritten: got %+v (found=%v), want %+v",
						bf.version, role, bf.object, kept, found, alteredGrant)
				}
			}

			// Non-system roles sharing the same keys must be untouched — this is
			// the only fixture shape that can fail when `is_system` is dropped.
			for _, role := range systemRoleKeys {
				if _, found := readGrant(ctx, t, conn, custom, role, bf.object); found {
					t.Errorf("%s: backfill wrote %s into non-system role %q; the is_system predicate "+
						"is missing or wrong", bf.version, bf.object, role)
				}
			}

			// The '{}' document stays empty — guard and jsonb_set agree.
			if _, found := readGrant(ctx, t, conn, empty, "admin", bf.object); found {
				t.Errorf("%s: %s appeared in a '{}' permissions document; jsonb_set cannot create the "+
					"missing objects parent, so the guard and the write have diverged", bf.version, bf.object)
			}
		})
	}
}

// rollBackTo unwinds core to the state immediately BEFORE the named version, so
// the backfills under test have not yet run when the fixture is seeded.
func rollBackTo(ctx context.Context, t *testing.T, conn *pgx.Conn, core dbmigrate.Namespace, version string) {
	t.Helper()
	index := -1
	for i, migration := range core.Migrations {
		if migration.Version == version {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("core migrations contain no %s", version)
	}
	if _, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations)-index); err != nil {
		t.Fatalf("down to pre-%s: %v", version, err)
	}
}

// policyDocument builds a realistic permissions document carrying an `objects`
// map — so jsonb_set has a parent to write into — seeded with `alreadyHeld`
// set to alteredGrant. Passing nil yields a document that predates every
// backfilled object, which is the real upgrade path.
func policyDocument(alreadyHeld []rbacBackfill) []byte {
	objects := map[string]grant{"person": crud}
	for _, bf := range alreadyHeld {
		objects[bf.object] = alteredGrant
	}
	raw, err := json.Marshal(map[string]any{"objects": objects, "row_scope": "team"})
	if err != nil {
		panic(fmt.Sprintf("building fixture document: %v", err))
	}
	return raw
}

func seedRole(t *testing.T, conn *pgx.Conn, workspaceID, key string, system bool, permissions []byte) {
	t.Helper()
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO role (workspace_id, key, name, is_system, permissions)
		 VALUES ($1, $2, $2, $3, $4::jsonb)`,
		workspaceID, key, system, string(permissions)); err != nil {
		t.Fatalf("seeding role %s/%s: %v", workspaceID, key, err)
	}
}

// readGrant returns the role's grant on object. found reports KEY PRESENCE,
// asked of jsonb directly rather than inferred from a null value — a migration
// that wrote `<object>: null` would otherwise read as "absent" and satisfy the
// untouched-row assertions it should fail.
func readGrant(ctx context.Context, t *testing.T, conn *pgx.Conn, workspaceID, key, object string) (grant, bool) {
	t.Helper()
	var present bool
	var raw []byte
	err := conn.QueryRow(ctx,
		`SELECT COALESCE(permissions->'objects' ? $3, false), permissions->'objects'->$3
		   FROM role WHERE workspace_id = $1 AND key = $2`,
		workspaceID, key, object).Scan(&present, &raw)
	if err != nil {
		t.Fatalf("reading %s grant for %s: %v", object, key, err)
	}
	if !present {
		return grant{}, false
	}
	var g grant
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decoding %s grant for %s (a present key must hold a valid grant): %v", object, key, err)
	}
	return g, true
}
