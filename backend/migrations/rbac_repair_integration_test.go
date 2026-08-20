// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// The obligation: the repair migrations heal a database that already recorded the
// backfills they re-apply, and grant exactly what those backfills granted — no
// more.
//
// Correcting a shipped migration in place reaches only installations migrated
// from scratch afterwards, because an applied version never runs again. The
// repairs are what reach the rest, so their effect has to be executed rather than
// read. The upgrade replay next door cannot do it: there the corrected backfills
// write the keys before the repair runs, so the repair is a no-op and the replay
// would pass either way.
//
// What to expect is derived from the repair SQL itself, so this test grows with
// the repair instead of being a second list to keep in step; that the derived set
// is COMPLETE — that it covers every backfill a deployed database could have lost
// — is the separate, static obligation of
// TestTheRepairsCoverEveryGuardedRBACBackfill.
//
// Every role is asserted, not just the granted ones. A repair that handed `rep` a
// write grant on channel_connection would be a privilege escalation on every
// installation that upgraded, and checking only the roles the repair names would
// not see it.

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestTheRepairMigrationsHealADatabaseThatAlreadyRecordedTheLostBackfill(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	core, custom := namespaces(t)
	for _, namespace := range []dbmigrate.Namespace{core, custom} {
		for _, repair := range repairMigrations(namespace) {
			t.Run(namespace.Name+"/"+repair.Version, func(t *testing.T) {
				// A schema of its own per repair, and the two fixture shapes
				// below replayed on it one at a time. Role keys are unique
				// across the installation since ADR-0091 §8 phase B, so the
				// defective document and the narrowed one cannot sit in two
				// workspaces side by side — each is a different installation's
				// state, which is what they always modelled.
				resetSchema(t, admin)
				migrator := asMigrator(t, admin)
				migrateAll(t, migrator)
				// Then back ONE core step, because the newest core migration is
				// ADR-0091 §8 phase D's drop of role.workspace_id and these
				// repairs name it. The migration is correct in its own position
				// — core runs in order, so core/0192 executes while the column
				// is still there — and replaying it at head asks it to run in an
				// era it never belonged to. One step back IS that era, and the
				// suite migrates forward again below so every assertion is read
				// at head.
				rollBackThePhaseDDrop(t, admin)

				want := grantsWrittenBy(t, repair)
				objects := objectsIn(want)

				// A document holding some other object, so the repair has a real row
				// to patch and the missing keys are the whole defect.
				workspace := seedWorkspace(t, admin, "repair-"+namespace.Name+"-"+repair.Version)
				document := []byte(`{"objects":{"person":{"create":true,"read":true,"update":true,` +
					`"delete":true}},"row_scope":"all"}`)
				for _, role := range systemRoleKeys {
					seedRole(t, admin, workspace, role, true, document)
					for _, object := range objects {
						if _, present := readGrant(ctx, t, admin, workspace, role, object); present {
							t.Fatalf("the seeded document for %q already holds %s, so this proves nothing "+
								"about the repair", role, object)
						}
					}
				}

				// A second workspace where EVERY system role already holds every one of
				// these objects, deliberately NARROWED. The repair's only-if-absent
				// guard is the single thing standing between it and overwriting an
				// operator's decision on every installation that upgrades, and a guard
				// is only proved by a row it has to skip.
				//
				// All five roles, not just admin: each object is repaired by two or
				// three statements split by role, so a guard dropped from the
				// manager/rep/read_only arm would leave the assertions above passing —
				// those roles are absent in the main workspace, so the repair writing
				// them is the expected outcome there.
				// Executed directly rather than through Up: the version is already
				// recorded, and skipping recorded versions is the behaviour that makes
				// the repair necessary in the first place.
				if _, err := migrator.Exec(ctx, repair.UpSQL); err != nil {
					t.Fatalf("applying the %s/%s repair: %v", namespace.Name, repair.Version, err)
				}
				// Forward again: every assertion below reads the schema an
				// installation actually runs on, not the era the replay needed.
				migrateAll(t, migrator)

				for _, object := range objects {
					for _, role := range systemRoleKeys {
						held, present := readGrant(ctx, t, admin, workspace, role, object)
						expected, granted := want[rbacGrant{object: object, role: role}]
						switch {
						case granted && !present:
							t.Errorf("%q holds no grant on %s after the repair, but the backfill grants "+
								"%+v; a deployed installation would still 403 on every %s route",
								role, object, expected, object)
						case granted && held != expected:
							t.Errorf("%q holds %+v on %s, the backfill grants %+v", role, held, object, expected)
						case !granted && present:
							t.Errorf("the repair gave %q a grant on %s (%+v); no backfill grants it one, so "+
								"this hands every upgrading installation a privilege it should not have",
								role, object, held)
						}
					}

				}

				// The repair patches one key per object. A jsonb_set on the wrong path,
				// or a SET that replaced the document instead of patching it, would
				// take the rest of the row with it and lock every user out of person.
				for _, role := range systemRoleKeys {
					if held, present := readGrant(ctx, t, admin, workspace, role, "person"); !present ||
						held != crud {
						t.Errorf("%q lost its person grant (%+v, present=%v) to a repair that should only "+
							"have added keys; the repair is rewriting documents, not patching them",
							role, held, present)
					}
				}

				// The second shape, on its own schema: an installation where EVERY
				// system role already holds every one of these objects, deliberately
				// NARROWED. The repair's only-if-absent guard is the single thing
				// standing between it and overwriting an operator's decision on every
				// installation that upgrades, and a guard is only proved by a row it
				// has to skip.
				//
				// All five roles, not just admin: each object is repaired by two or
				// three statements split by role, so a guard dropped from the
				// manager/rep/read_only arm would leave the assertions above passing —
				// those roles are absent in the first shape, so the repair writing
				// them is the expected outcome there.
				resetSchema(t, admin)
				narrowMigrator := repointed(t, admin, migrator)
				migrateAll(t, narrowMigrator)
				// Same one step back as above, for the same reason: the replay
				// below runs a migration from before role.workspace_id was
				// dropped, so the fixture is seeded in that era too.
				rollBackThePhaseDDrop(t, admin)
				narrowed := seedWorkspace(t, admin, "repair-narrowed-"+namespace.Name+"-"+repair.Version)
				for _, role := range systemRoleKeys {
					seedRole(t, admin, narrowed, role, true, documentGranting(t, objects, alteredGrant))
				}
				if _, err := narrowMigrator.Exec(ctx, repair.UpSQL); err != nil {
					t.Fatalf("applying the %s/%s repair to the narrowed installation: %v",
						namespace.Name, repair.Version, err)
				}
				for _, object := range objects {
					for _, role := range systemRoleKeys {
						if held, present := readGrant(ctx, t, admin, narrowed, role, object); !present ||
							held != alteredGrant {
							t.Errorf("the repair rewrote %q's existing %s grant to %+v; it was %+v, which an "+
								"operator set deliberately. The only-if-absent guard is what keeps a repair "+
								"from overruling every installation it touches", role, object, held, alteredGrant)
						}
					}
				}
			})
		}
	}
}

// grantsWrittenBy reads one repair's blocks into the comparable grant shape.
func grantsWrittenBy(t *testing.T, repair dbmigrate.Migration) map[rbacGrant]grant {
	t.Helper()
	raw := map[rbacGrant]string{}
	collectGrants(t, repair, raw)
	if len(raw) == 0 {
		t.Fatalf("%s_%s writes no RBAC grant this test can read, so executing it proves nothing. Either "+
			"the repair is empty or its blocks moved to a shape the derivation no longer matches.",
			repair.Version, repair.Name)
	}
	decoded := make(map[rbacGrant]grant, len(raw))
	for key, payload := range raw {
		var verbs grant
		if err := json.Unmarshal([]byte(payload), &verbs); err != nil {
			t.Fatalf("%s_%s grants %q on %s as %s, which is not a permission document: %v",
				repair.Version, repair.Name, key.role, key.object, payload, err)
		}
		decoded[key] = verbs
	}
	return decoded
}

// documentGranting builds a permissions document holding every object at the
// same grant — the fixture the repair's only-if-absent guard has to skip.
func documentGranting(t *testing.T, objects []string, held grant) []byte {
	t.Helper()
	document := struct {
		Objects  map[string]grant `json:"objects"`
		RowScope string           `json:"row_scope"`
	}{Objects: make(map[string]grant, len(objects)), RowScope: "all"}
	for _, object := range objects {
		document.Objects[object] = held
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("building the narrowed fixture document: %v", err)
	}
	return raw
}

// objectsIn lists the distinct objects a repair touches, in a stable order.
func objectsIn(grants map[rbacGrant]grant) []string {
	seen, objects := map[string]bool{}, []string{}
	for key := range grants {
		if !seen[key.object] {
			seen[key.object] = true
			objects = append(objects, key.object)
		}
	}
	sort.Strings(objects)
	return objects
}

// repointed makes a migrator connection usable again after resetSchema.
//
// DROP SCHEMA public takes the migrator's grants with it, and a role that may
// not create in any schema on its path fails with Postgres's rather opaque "no
// schema has been selected to create in". Re-granting and re-issuing the path
// is all it needs.
//
// The connection is REUSED rather than a second role minted, which matters more
// than it looks: `asMigrator` drops and recreates its role, and the role owns
// the objects the reset just dropped — recreating it mid-test fails on
// dependencies that are cleared only when the schema goes.
func repointed(t *testing.T, admin, conn *pgx.Conn) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `GRANT CREATE, USAGE ON SCHEMA public TO `+migratorRole); err != nil {
		t.Fatalf("re-granting the migrator's schema privileges: %v", err)
	}
	// The extensions the operator installs go with the schema too, and the
	// migrator may not create them itself — the same division a deployed
	// installation has (scripts/deploy/db-bootstrap.sql installs them as the
	// operator; migrations assume they are there).
	for _, extension := range extensionsTheOperatorInstalls {
		if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+extension); err != nil {
			t.Fatalf("reinstalling the %s extension after the reset: %v", extension, err)
		}
	}
	if _, err := conn.Exec(ctx, `SET search_path TO public`); err != nil {
		t.Fatalf("re-pointing the migrator at the recreated schema: %v", err)
	}
	return conn
}

// phaseDDropName is the core migration these repairs need rolled back: ADR-0091
// §8 phase D's drop of role.workspace_id. Named, not counted — see below.
const phaseDDropName = "role_drops_the_tenant_column"

// rollBackThePhaseDDrop takes core back to the era these repairs belong to and
// proves the step landed where it was aimed.
//
// How far back that is, is DERIVED from where the phase D drop sits in the
// namespace, never assumed to be one step. It was one step when this suite was
// written, and stopped being one the moment a later core migration landed on top
// — after which the rollback reverted that migration instead and left
// role.workspace_id dropped, so every repair replayed in exactly the era this
// helper exists to avoid. A count is a guess about which migration is newest;
// the name is the thing actually meant, and it fails loudly when it is gone.
func rollBackThePhaseDDrop(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	core, _ := namespaces(t)
	steps := coreStepsBackToPhaseD(t, core)
	if _, err := dbmigrate.Down(context.Background(), conn, core, steps); err != nil {
		t.Fatalf("rolling core back %d step(s) to the era these repairs belong to: %v", steps, err)
	}
	var present bool
	if err := conn.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                WHERE table_name = 'role' AND column_name = 'workspace_id')`).Scan(&present); err != nil {
		t.Fatalf("checking whether the rollback restored role.workspace_id: %v", err)
	}
	if !present {
		t.Fatalf("rolling core back %d step(s) to %s did not restore role.workspace_id — the repairs "+
			"below would replay in an era they never belonged to", steps, phaseDDropName)
	}
}

// coreStepsBackToPhaseD counts the core migrations from the newest down to and
// including the phase D drop, which is how many Down must revert to put
// role.workspace_id back. Down reverts newest first, so the count is the drop's
// distance from the end.
func coreStepsBackToPhaseD(t *testing.T, core dbmigrate.Namespace) int {
	t.Helper()
	for i := len(core.Migrations) - 1; i >= 0; i-- {
		if core.Migrations[i].Name == phaseDDropName {
			return len(core.Migrations) - i
		}
	}
	t.Fatalf("no core migration named %q — these repairs name role.workspace_id and need the era "+
		"before it was dropped; if the drop was renamed, rename phaseDDropName with it", phaseDDropName)
	return 0
}
