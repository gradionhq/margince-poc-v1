// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// The obligation: an installation that predates every backfill, upgraded to
// head, holds exactly the matrix the server seeds today. This executes it
// rather than approximating it — a check that a migration MENTIONS a JSON path
// cannot tell a backfill targeting the wrong roles, dropping is_system, or
// writing the wrong verbs from a correct one.
//
// Three mechanics carry it:
//
//   - dbmigrate.Namespace is a plain struct, so core.Migrations[:cut] is a
//     legitimate partial apply. Keeping Name means both calls share the same
//     schema_migrations_core tracking table.
//   - Up reads the applied versions and skips them, so the second call applies
//     exactly the remainder. No down migrations, nothing to unwind.
//   - Roles are seeded by APP code (identity.seedSystemRoles), not by a
//     migration, so a freshly migrated database holds no role rows at all. The
//     legacy documents must be planted before the upgrade, or there is nothing
//     to compare.
//
// The parity cases next door hold the other half — that a backfill does not
// clobber an ALTERED grant, that the is_system predicate holds, and that a '{}'
// document is never silently created. A pristine-legacy comparison reaches none
// of those.

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// The fixtures are committed because this package cannot import
// identity/internal/policy: Go's import fence restricts it to
// internal/modules/identity/**, and .go-arch-lint.yml allows `migrations` to
// depend on `platform` alone. Neither fixture can drift —
// rbac_seeded_defaults.json is held to policy.MustDefaultJSON by a gate inside
// the fence, and rbac_legacy_installs.json has both its cohort and its grants
// derived from the initial commit by a gate at the backend root.
const (
	legacyInstallsFixture = "testdata/rbac_legacy_installs.json"
	seededDefaultsFixture = "testdata/rbac_seeded_defaults.json"
)

// verbs is one object's grant, compared by value.
//
// Decoding into this rather than into map[string]any is load-bearing, not
// tidiness. policy.Parse unmarshals a stored document into exactly this shape
// when identity.loadGrants resolves a session, so a migration that wrote
// `"create": "false"` — a string where a bool belongs — locks out every user
// assigned that role. A structural decode would read the same document as an
// ordinary all-false grant and report the upgrade clean. Decoding strictly
// means this test rejects any document the server itself would reject.
type verbs struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

// replayDocument is a role.permissions document, decoded the way the server
// decodes it.
type replayDocument struct {
	Objects  map[string]verbs `json:"objects"`
	RowScope string           `json:"row_scope"`
}

// replayInstalls is the committed fixture the replay seeds. Two installations,
// because one oldest-possible document is the worst case only while every
// backfill is an unconditional additive write — true of the SQL as written
// today, but an assumption about future migrations. A mid-life document that
// already holds half the vocabulary is where a conditionally-written backfill
// has somewhere to fail.
//
// The documents stay raw: this test plants them verbatim, and the gate at the
// backend root is what holds their content to the initial commit.
type replayInstalls struct {
	LegacyCoreVersion string                                `json:"legacy_core_version"`
	Installs          map[string]map[string]json.RawMessage `json:"installs"`
}

func TestUpgradingALegacyInstallationYieldsTheSeededMatrix(t *testing.T) {
	ctx := context.Background()
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)

	// Migrations run as a NON-SUPERUSER owner, the posture a deployed
	// installation's migration role has. Applying them as this suite's ordinary
	// owner instead would prove far less than it appears to: that role is the
	// container's superuser, so it bypasses the row-level security every tenant
	// backfill has to satisfy, and a backfill that reaches nothing in production
	// would pass here. See migrationrole_integration_test.go.
	migrator := asMigrator(t, conn)

	core, custom := namespaces(t)
	installs := readReplayInstalls(t)
	want := readSeededDefaults(t)

	// An old installation: core applied only as far as it had been released.
	legacy := dbmigrate.Namespace{
		Name:       core.Name,
		Migrations: core.Migrations[:prefixThrough(t, core, installs.LegacyCoreVersion)],
	}
	if _, err := dbmigrate.Up(ctx, migrator, legacy); err != nil {
		t.Fatalf("applying core through %s: %v", installs.LegacyCoreVersion, err)
	}

	workspaces := seedInstallations(t, conn, installs)

	// Upgrade it to head — core AND custom, because two backfills live in the
	// fork-owned namespace and an assertion that modelled the split would be
	// modelling the wrong thing.
	if _, err := dbmigrate.Up(ctx, migrator, core, custom); err != nil {
		t.Fatalf("upgrading to head: %v", err)
	}

	for _, install := range slices.Sorted(maps.Keys(workspaces)) {
		t.Run(install, func(t *testing.T) {
			assertMatrix(ctx, t, conn, workspaces[install], want)
		})
	}
}

// assertMatrix compares every system role's document against what the server
// seeds today. Errorf, not Fatalf: one role's missing object must not hide the
// other four, and a reader triaging a failed upgrade wants the whole picture.
func assertMatrix(ctx context.Context, t *testing.T, conn *pgx.Conn, workspaceID string, want map[string]replayDocument) {
	t.Helper()
	for _, role := range slices.Sorted(maps.Keys(want)) {
		got := readPermissions(ctx, t, conn, workspaceID, role)
		for _, line := range diffDocuments(got, want[role]) {
			t.Errorf("role %q: %s", role, line)
		}
	}
}

// diffDocuments reports the cells that differ, one line each — a whole-document
// dump of 29 objects buries the one that moved.
//
// The two directions are deliberately NOT symmetric, because the runtime is not
// symmetric about them.
//
// An object the seed grants but the upgrade omits is compared BY VALUE, and an
// omission whose seeded grant is the zero grant passes. A freshly seeded
// installation stores every object in the vocabulary, including the ones a role
// holds nothing on; a backfill writes a key only for the roles it grants
// something to. Both mean "this role may do nothing here", and demanding key
// presence would fail twelve correct cells (manager, rep and read_only across
// embedding_reindex, fx_rate, ai_model_rate and import_run) while catching
// nothing.
//
// An object the upgrade produced but the seed does not name FAILS OUTRIGHT,
// whatever verbs it carries — including all-false. That asymmetry is the whole
// point: a backfill with a typo'd path — `{objects,import_runs}` for
// `{objects,import_run}` — writes a harmless-looking row under a name nothing
// grants, so the real `import_run` grants stay absent and every user holding
// that role 403s on the routes the upgrade was supposed to open. Comparing that
// cell by value would call it equal, because a missing key and an all-false
// grant both read as "may do nothing".
//
// The lockout this comment used to describe — policy.Parse refusing the whole
// document at login — is gone: Parse now DROPS an object outside the vocabulary
// and logs it, because a stored document outlives the code that defines the
// vocabulary (see policy.Parse, and the Task 14 UAT's F4). The typo is
// therefore quieter than it was, which makes this comparison more load-bearing
// rather than less: nothing at run time will complain about it any more.
func diffDocuments(got, want replayDocument) []string {
	var lines []string
	for _, object := range slices.Sorted(maps.Keys(got.Objects)) {
		if _, known := want.Objects[object]; !known {
			lines = append(lines, "holds a grant on "+object+", which is not in the seeded "+
				"vocabulary. policy.Parse drops an object it does not know, so this grant "+
				"authorizes nothing and the object it was meant to name has no grant at all "+
				"— check the JSON path the backfill writes")
		}
	}
	for _, object := range slices.Sorted(maps.Keys(want.Objects)) {
		held, seeded := got.Objects[object], want.Objects[object]
		if held == seeded {
			continue
		}
		if held == (verbs{}) {
			lines = append(lines, "holds no grant on "+object+" after the upgrade, but the server "+
				"seeds "+render(seeded)+"; an existing installation would 403 on every "+object+
				" route. Its backfill migration is missing or matched no rows")
			continue
		}
		lines = append(lines, "grant on "+object+" is "+render(held)+", the server seeds "+
			render(seeded)+"; the backfill writes the wrong verbs")
	}
	if got.RowScope != want.RowScope {
		lines = append(lines, "row_scope is "+got.RowScope+", the server seeds "+want.RowScope)
	}
	return lines
}

func render(grant verbs) string {
	raw, err := json.Marshal(grant)
	if err != nil {
		return "<unrenderable>"
	}
	return string(raw)
}

// seedInstallations plants each fixture installation in its own workspace and
// returns the workspace ids by installation name.
func seedInstallations(t *testing.T, conn *pgx.Conn, installs replayInstalls) map[string]string {
	t.Helper()
	workspaces := map[string]string{}
	for _, install := range slices.Sorted(maps.Keys(installs.Installs)) {
		workspaceID := seedWorkspace(t, conn, "rbac-replay-"+install)
		workspaces[install] = workspaceID
		for _, role := range slices.Sorted(maps.Keys(installs.Installs[install])) {
			seedRole(t, conn, workspaceID, role, true, installs.Installs[install][role])
		}
	}
	return workspaces
}

// prefixThrough returns the number of migrations up to AND INCLUDING version —
// the length of the prefix an installation at that release had applied.
func prefixThrough(t *testing.T, core dbmigrate.Namespace, version string) int {
	t.Helper()
	for i, migration := range core.Migrations {
		if migration.Version == version {
			return i + 1
		}
	}
	t.Fatalf("core contains no migration %s, which %s pins as the initial commit's head. "+
		"A shipped core migration cannot be renumbered or removed, so this means the fixture is wrong.",
		version, legacyInstallsFixture)
	return 0
}

func readReplayInstalls(t *testing.T) replayInstalls {
	t.Helper()
	var installs replayInstalls
	if err := json.Unmarshal(readFixture(t, legacyInstallsFixture), &installs); err != nil {
		t.Fatalf("decoding %s: %v", legacyInstallsFixture, err)
	}
	if len(installs.Installs) == 0 {
		t.Fatalf("%s declares no installations to replay", legacyInstallsFixture)
	}
	return installs
}

func readSeededDefaults(t *testing.T) map[string]replayDocument {
	t.Helper()
	want := map[string]replayDocument{}
	if err := json.Unmarshal(readFixture(t, seededDefaultsFixture), &want); err != nil {
		t.Fatalf("decoding %s: %v", seededDefaultsFixture, err)
	}
	if len(want) == 0 {
		t.Fatalf("%s declares no seeded role documents to compare against", seededDefaultsFixture)
	}
	return want
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		absolute, resolveErr := filepath.Abs(path)
		if resolveErr != nil {
			absolute = path
		}
		t.Fatalf("reading %s (resolved to %s): %v", path, absolute, err)
	}
	return raw
}

// readPermissions returns the role's whole permissions document. A missing row
// is fatal rather than an empty document: the replay seeds every system role
// before the upgrade, so a vanished role means a migration deleted it, which no
// diff of grants would explain.
func readPermissions(ctx context.Context, t *testing.T, conn *pgx.Conn, workspaceID, key string) replayDocument {
	t.Helper()
	var raw []byte
	err := conn.QueryRow(ctx,
		`SELECT permissions FROM role WHERE workspace_id = $1 AND key = $2`,
		workspaceID, key).Scan(&raw)
	if err != nil {
		t.Fatalf("reading the permissions document for role %q: %v", key, err)
	}
	var document replayDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("role %q holds a permissions document the server itself could not parse: %v\n"+
			"policy.Parse decodes into this same shape at login, so a migration that wrote a "+
			"malformed grant locks out every user assigned this role.", key, err)
	}
	return document
}
