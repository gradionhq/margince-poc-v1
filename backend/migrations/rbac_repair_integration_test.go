// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// The obligation: the repair migrations heal a database that already recorded the
// backfill they re-apply, and grant exactly what the server seeds — no more.
//
// Correcting a shipped migration in place reaches only installations migrated
// from scratch afterwards, because an applied version never runs again. These two
// are what reach the rest, so their effect has to be executed rather than read.
// The upgrade replay next door cannot do it: there the corrected 0154 writes the
// key before the repair runs, so the repair is a no-op and the replay would pass
// either way.
//
// Every role is asserted, not just admin. A repair that handed `rep` a write
// grant on channel_connection would be a privilege escalation on every
// installation that upgraded, and checking admin alone would not see it.

import (
	"context"
	"testing"
)

func TestTheRepairMigrationsHealADatabaseThatAlreadyRecordedTheLostBackfill(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	migrateAll(t, migrator)

	// What the two migrations grant, per role, as identity/internal/policy seeds
	// it. An absent entry means the repair must leave NO key: absence is the deny,
	// and a freshly seeded installation's zero grant reads the same way.
	for _, repair := range []struct {
		namespace string
		version   string
		object    string
		want      map[string]grant
	}{{
		namespace: "core",
		version:   "0190",
		object:    "channel_connection",
		want: map[string]grant{
			"admin": crud, "ops": crud,
			"manager": readOnly, "rep": readOnly, "read_only": readOnly,
		},
	}, {
		namespace: "custom",
		version:   "20260806120000",
		object:    "import_run",
		want:      map[string]grant{"admin": crud, "ops": crud},
	}} {
		t.Run(repair.namespace+"/"+repair.version, func(t *testing.T) {
			// A document holding some other object, so the repair has a real row
			// to patch and the missing key is the whole defect.
			workspace := seedWorkspace(t, admin, "repair-"+repair.object)
			document := []byte(`{"objects":{"person":{"create":true,"read":true,"update":true,"delete":true}},` +
				`"row_scope":"all"}`)
			for _, role := range []string{"admin", "ops", "manager", "rep", "read_only"} {
				seedRole(t, admin, workspace, role, true, document)
				if _, present := readGrant(ctx, t, admin, workspace, role, repair.object); present {
					t.Fatalf("the seeded document for %q already holds %s, so this proves nothing about "+
						"the repair", role, repair.object)
				}
			}

			// Executed directly rather than through Up: the version is already
			// recorded, and skipping recorded versions is the behaviour that makes
			// the repair necessary in the first place.
			if _, err := migrator.Exec(ctx, repairSQL(t, repair.namespace, repair.version)); err != nil {
				t.Fatalf("applying the %s repair: %v", repair.object, err)
			}

			for _, role := range []string{"admin", "ops", "manager", "rep", "read_only"} {
				held, present := readGrant(ctx, t, admin, workspace, role, repair.object)
				want, granted := repair.want[role]
				switch {
				case granted && !present:
					t.Errorf("%q holds no grant on %s after the repair, but the server seeds %+v; a "+
						"deployed installation would still 403 on every %s route", role, repair.object,
						want, repair.object)
				case granted && held != want:
					t.Errorf("%q holds %+v on %s, the server seeds %+v", role, held, repair.object, want)
				case !granted && present:
					t.Errorf("the repair gave %q a grant on %s (%+v); the server seeds none, so this "+
						"hands every upgrading installation a privilege it should not have", role,
						repair.object, held)
				}
			}
		})
	}
}

// repairSQL returns the up half of one migration by version.
func repairSQL(t *testing.T, namespace, version string) string {
	t.Helper()
	core, custom := namespaces(t)
	source := core
	if namespace == "custom" {
		source = custom
	}
	for _, migration := range source.Migrations {
		if migration.Version == version {
			return migration.UpSQL
		}
	}
	t.Fatalf("the %s namespace holds no migration %s, so the repair this test exists for is gone and a "+
		"deployed installation would keep the grants row-level security discarded", namespace, version)
	return ""
}
