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

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestTheRepairMigrationsHealADatabaseThatAlreadyRecordedTheLostBackfill(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	migrateAll(t, migrator)

	core, custom := namespaces(t)
	for _, namespace := range []dbmigrate.Namespace{core, custom} {
		for _, repair := range repairMigrations(namespace) {
			t.Run(namespace.Name+"/"+repair.Version, func(t *testing.T) {
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

				// A second workspace whose admin already holds every one of these
				// objects, deliberately NARROWED. The repair's only-if-absent guard is
				// the single thing standing between it and overwriting an operator's
				// decision on every installation that upgrades, and a guard is only
				// proved by a row it has to skip.
				narrowed := seedWorkspace(t, admin, "repair-narrowed-"+namespace.Name+"-"+repair.Version)
				seedRole(t, admin, narrowed, "admin", true, documentGranting(t, objects, alteredGrant))

				// Executed directly rather than through Up: the version is already
				// recorded, and skipping recorded versions is the behaviour that makes
				// the repair necessary in the first place.
				if _, err := migrator.Exec(ctx, repair.UpSQL); err != nil {
					t.Fatalf("applying the %s/%s repair: %v", namespace.Name, repair.Version, err)
				}

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

					if held, present := readGrant(ctx, t, admin, narrowed, "admin", object); !present ||
						held != alteredGrant {
						t.Errorf("the repair rewrote admin's existing %s grant to %+v; it was %+v, which an "+
							"operator set deliberately. The only-if-absent guard is what keeps a repair "+
							"from overruling every installation it touches", object, held, alteredGrant)
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
