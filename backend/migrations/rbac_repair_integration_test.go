// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// The obligation: the repair migrations heal a database that already recorded
// the backfill they re-apply. Correcting a shipped migration in place reaches
// only installations migrated from scratch afterwards — an applied version never
// runs again — so without these two, every deployed installation would keep the
// grants row-level security discarded.
//
// This has to EXECUTE the repair against a document missing the grant, because
// the upgrade replay next door cannot: there the corrected 0154 has already
// written the key by the time the repair runs, so the repair is a no-op and the
// replay would pass whether or not it works.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTheRepairMigrationsHealADatabaseThatAlreadyRecordedTheLostBackfill(t *testing.T) {
	// The two objects the audit found missing on the deployed installation, with
	// the grant each repair must land for an admin.
	fullGrant := verbs{Create: true, Read: true, Update: true, Delete: true}
	repairs := []struct {
		namespace string
		version   string
		object    string
		wantAdmin verbs
	}{
		{"core", "0190", "channel_connection", fullGrant},
		{"custom", "20260806120000", "import_run", fullGrant},
	}

	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	migrateAll(t, migrator)

	// The state a deployed installation is actually in: role documents holding
	// the rest of the vocabulary, with the two lost objects absent — which is
	// what the grant read asks about, so absence is the whole defect.
	workspace := seedWorkspace(t, admin, "repair-target")
	document := []byte(`{"objects":{"person":{"create":true,"read":true,"update":true,"delete":true}},"row_scope":"all"}`)
	for _, role := range []string{"admin", "ops"} {
		seedRole(t, admin, workspace, role, true, document)
	}

	for _, repair := range repairs {
		t.Run(repair.namespace+"/"+repair.version, func(t *testing.T) {
			if grant, present := readRepairedGrant(ctx, t, admin, workspace, repair.object); present {
				t.Fatalf("the seeded document already holds %s (%v), so this proves nothing about the "+
					"repair", repair.object, grant)
			}
			// Executed directly rather than through Up: the version is already
			// recorded, and skipping applied versions is exactly the behaviour
			// that makes the repair necessary in the first place.
			if _, err := migrator.Exec(ctx, repairSQL(t, repair.namespace, repair.version)); err != nil {
				t.Fatalf("applying the %s repair: %v", repair.object, err)
			}
			grant, present := readRepairedGrant(ctx, t, admin, workspace, repair.object)
			if !present {
				t.Fatalf("the repair left admin with no grant on %s, so a deployed installation would "+
					"still 403 on every %s route after upgrading", repair.object, repair.object)
			}
			if grant != repair.wantAdmin {
				t.Errorf("the repair granted admin %+v on %s, the server seeds %+v", grant, repair.object,
					repair.wantAdmin)
			}
		})
	}
}

// repairSQL returns the up half of one migration by version.
func repairSQL(t *testing.T, namespace, version string) string {
	t.Helper()
	core, custom := loadNamespaces(t)
	source := core
	if namespace == "custom" {
		source = custom
	}
	for _, migration := range source.Migrations {
		if migration.Version == version {
			return migration.UpSQL
		}
	}
	t.Fatalf("the %s namespace holds no migration %s; the repair this test exists for is gone, and a "+
		"deployed installation would keep the grants row-level security discarded", namespace, version)
	return ""
}

// readRepairedGrant reports admin's grant on object, and whether the KEY is
// present at all — absence is the defect being repaired, and a null value would
// otherwise read the same as a zero grant.
func readRepairedGrant(ctx context.Context, t *testing.T, conn *pgx.Conn, workspaceID, object string) (verbs, bool) {
	t.Helper()
	var present bool
	var raw []byte
	if err := conn.QueryRow(ctx,
		`SELECT COALESCE(permissions->'objects' ? $2, false), permissions->'objects'->$2
		   FROM role WHERE workspace_id = $1 AND key = 'admin'`,
		workspaceID, object).Scan(&present, &raw); err != nil {
		t.Fatalf("reading admin's %s grant: %v", object, err)
	}
	if !present {
		return verbs{}, false
	}
	var grant verbs
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatalf("admin's %s grant is not a document the server could parse: %v", object, err)
	}
	return grant, true
}
