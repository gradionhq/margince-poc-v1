// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// The obligation: a repair migration re-applies EVERY guarded RBAC backfill that
// shipped before it, granting exactly what that backfill granted.
//
// The backfills all write into role documents that already exist, so under the
// unbound-write bug each one matched zero rows on a deployed database and the
// grant is still missing there. Correcting the originals in place cannot reach
// those installations — an applied version never runs again — so the repairs are
// the only path, and a repair that covers a subset leaves a permanent 403 on the
// objects it skipped. Which subset an installation actually lost depends on when
// it first booted, which nothing in this tree can know; covering all of them is
// what removes the question, and every block is guarded on key absence so the
// coverage costs a no-op wherever the original landed.
//
// Both sides are derived from the migration tree. A backfill added AFTER the
// newest repair needs none — it has not shipped unbound — so the comparison is
// bounded by version, and a new backfill does not fail this test until someone
// adds a repair above it.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// One guarded backfill block: the object, the role it grants to, and the exact
// permission payload. Role is one entry per role even where the source names
// several in one IN list, so a repair may regroup the roles freely as long as
// every role ends up with the same payload.
type rbacGrant struct {
	object string
	role   string
}

// A `UPDATE role SET permissions = jsonb_set(...)` guarded on key absence — the
// one shape every RBAC backfill in this tree uses. Anything else is not matched,
// and unmatchedRoleWrites below is what refuses to let that pass unnoticed.
var guardedBackfillPattern = regexp.MustCompile(
	`UPDATE role SET permissions = jsonb_set\(\s*` +
		`permissions,\s*'\{objects,(\w+)\}',\s*` +
		`'([^']*)'::jsonb\)\s*` +
		`WHERE \(is_system AND key (?:IN \(([^)]*)\)|= ('\w+'))\s*` +
		`AND NOT permissions->'objects' \? '(\w+)'\)`)

// The second legitimate shape: grant ONE verb on an object entry the role
// ALREADY has, guarded on the object's presence. The pattern above cannot
// express it and must not be stretched to — it seeds `{objects,<object>}` whole
// and guards on that object's ABSENCE, so forcing this write into it would
// either overwrite the verbs already there or, with the absence guard, match
// nothing at all. Two shapes, because there are two operations.
var guardedVerbGrantPattern = regexp.MustCompile(
	`UPDATE role SET permissions = jsonb_set\(\s*` +
		`permissions,\s*'\{objects,(\w+),(\w+)\}',\s*` +
		`'([^']*)'::jsonb\)\s*` +
		`WHERE is_system AND key (?:IN \(([^)]*)\)|= ('\w+'))\s*` +
		`AND permissions->'objects' \? '(\w+)'`)

// Every write to role.permissions, however it is spelled. This has to be
// STRICTLY BROADER than guardedBackfillPattern, not a prefix of it: a canary
// sharing the pattern's literal head would miss exactly the deviations that
// blind the pattern — reflowing `UPDATE role\n SET permissions = jsonb_set(`
// across two lines escapes both at once, the counts stay equal, and the write
// becomes invisible with nothing raised. Matching only as far as `permissions`,
// with whitespace free, leaves the two able to disagree.
var anyRolePermissionWrite = regexp.MustCompile(`(?is)UPDATE\s+"?role"?\s+SET\s+permissions`)

var quotedRolePattern = regexp.MustCompile(`'(\w+)'`)

func TestTheRepairsCoverEveryGuardedRBACBackfill(t *testing.T) {
	core, custom := namespaces(t)
	for _, namespace := range []dbmigrate.Namespace{core, custom} {
		t.Run(namespace.Name, func(t *testing.T) {
			repairs, newestRepair := repairGrants(t, namespace)
			backfills := backfillGrants(t, namespace, newestRepair)

			for _, grant := range sortedGrants(backfills) {
				want := backfills[grant]
				got, covered := repairs[grant]
				switch {
				case !covered:
					t.Errorf("%s: no repair re-applies %s for role %q, but its backfill shipped before "+
						"%s and would have matched zero rows on any database already deployed. Every "+
						"installation that upgraded past it 403s on every %s route, permanently. Add "+
						"the block to the repair.",
						namespace.Name, grant.object, grant.role, newestRepair, grant.object)
				case got != want:
					t.Errorf("%s: the repair grants %q %s on %s, its backfill grants %s. A repair must "+
						"re-apply what was lost, not a different permission.",
						namespace.Name, grant.role, got, grant.object, want)
				}
			}

			// The reverse direction: a repair that grants what no backfill did would
			// hand every upgrading installation a privilege the server never seeded.
			for _, grant := range sortedGrants(repairs) {
				if _, declared := backfills[grant]; !declared {
					t.Errorf("%s: a repair grants %q %s on %s, but no backfill below %s declares it. A "+
						"repair re-applies a lost write; it does not introduce one.",
						namespace.Name, grant.role, repairs[grant], grant.object, newestRepair)
				}
			}

			assertNoUnnamedRepairAboveTheCeiling(t, namespace, newestRepair, backfills)
			assertNoVerbGrantBelowTheCeiling(t, namespace, newestRepair)
		})
	}
}

// assertNoUnnamedRepairAboveTheCeiling catches a repair that did not follow the
// naming convention, from the SQL rather than the filename.
//
// isRepair reads a name, and a repair named something else is invisible twice
// over: it does not raise the ceiling, and sitting above the ceiling it is not
// read as a backfill either, so nothing it grants is compared to anything. What
// gives it away is what a repair IS — a migration that re-declares an (object,
// role) an earlier migration already granted. A genuine new backfill introduces
// an object; only a repair repeats one.
func assertNoUnnamedRepairAboveTheCeiling(
	t *testing.T, namespace dbmigrate.Namespace, ceiling string, below map[rbacGrant]string,
) {
	t.Helper()
	for _, migration := range namespace.Migrations {
		if isRepair(migration) || migration.Version <= ceiling {
			continue
		}
		above := map[rbacGrant]string{}
		collectGrants(t, migration, above)
		for _, grant := range sortedGrants(above) {
			if _, already := below[grant]; already {
				t.Errorf("%s: %s_%s re-grants %q %s on %s, which a migration below %s already granted. "+
					"Re-declaring an existing grant is what a repair does, and this migration is not "+
					"named as one — so it raises no ceiling and nothing checks what it writes. Rename it "+
					"to end in rbac_backfill_repair, or grant a new object.",
					namespace.Name, migration.Version, migration.Name, grant.role, above[grant],
					grant.object, ceiling)
			}
		}
	}
}

// assertNoVerbGrantBelowTheCeiling refuses to be silently blind about the one
// write shape the coverage derivation does not model.
//
// A verb grant reaches into an object entry the role already has, so the
// (object, role) key the comparison speaks in cannot hold it: two migrations
// granting different verbs on one object would collide on a single key and one
// would replace the other, unnoticed. Above the ceiling that costs nothing — a
// migration up there never shipped unbound and needs no repair, which is why the
// verb grant that exists today is fine where it sits. At or below the ceiling the
// comparison would quietly skip a backfill, the exact outcome this file exists to
// prevent, so it fails instead and whoever writes that migration extends the
// derivation on purpose rather than by accident.
func assertNoVerbGrantBelowTheCeiling(t *testing.T, namespace dbmigrate.Namespace, ceiling string) {
	t.Helper()
	for _, migration := range namespace.Migrations {
		if migration.Version > ceiling {
			continue
		}
		for _, match := range guardedVerbGrantPattern.FindAllStringSubmatch(migration.UpSQL, -1) {
			t.Errorf("%s: %s_%s grants %q the %s verb on %s, and it sits at or below %s where a repair "+
				"must re-apply it. The coverage comparison keys on (object, role) and cannot carry a "+
				"verb, so it would pass over this write instead of demanding the repair. Teach the "+
				"derivation the verb dimension before landing a verb grant this low.",
				namespace.Name, migration.Version, migration.Name, match[4]+match[5], match[2],
				match[1], ceiling)
		}
	}
}

// repairGrants reads the repair migrations, returning what they grant and the
// version of the newest one — the ceiling below which a backfill needs covering.
func repairGrants(t *testing.T, namespace dbmigrate.Namespace) (map[rbacGrant]string, string) {
	t.Helper()
	grants, newest := map[rbacGrant]string{}, ""
	for _, migration := range repairMigrations(namespace) {
		if migration.Version > newest {
			newest = migration.Version
		}
		collectGrants(t, migration, grants)
	}
	if newest == "" {
		t.Fatalf("the %s namespace holds no RBAC repair migration. The repairs are the only thing that "+
			"reaches a database which already recorded a backfill row-level security discarded, so "+
			"without one this test compares nothing and every deployed installation keeps its missing "+
			"grants.", namespace.Name)
	}
	return grants, newest
}

// backfillGrants reads every non-repair migration below ceiling.
func backfillGrants(t *testing.T, namespace dbmigrate.Namespace, ceiling string) map[rbacGrant]string {
	t.Helper()
	grants := map[rbacGrant]string{}
	for _, migration := range namespace.Migrations {
		if isRepair(migration) || migration.Version > ceiling {
			continue
		}
		collectGrants(t, migration, grants)
	}
	if len(grants) == 0 {
		t.Fatalf("the %s namespace derived no RBAC backfill grants below %s, so this test passes without "+
			"comparing anything. Either the backfills moved to a shape %s no longer matches, or the "+
			"namespace loaded empty — both make a green run meaningless.",
			namespace.Name, ceiling, guardedBackfillPattern)
	}
	return grants
}

// collectGrants adds one migration's guarded backfill blocks to grants, and
// fails if the migration writes role permissions in a shape neither pattern
// reads. Verb grants are reconciled here but NOT collected: they carry a verb the
// (object, role) key cannot hold, and assertNoVerbGrantBelowTheCeiling is what
// keeps that omission from ever reaching the coverage comparison.
func collectGrants(t *testing.T, migration dbmigrate.Migration, grants map[rbacGrant]string) {
	t.Helper()
	matches := guardedBackfillPattern.FindAllStringSubmatch(migration.UpSQL, -1)
	verbGrants := guardedVerbGrantPattern.FindAllStringSubmatch(migration.UpSQL, -1)
	read := len(matches) + len(verbGrants)
	if written := len(anyRolePermissionWrite.FindAllString(migration.UpSQL, -1)); written != read {
		t.Fatalf("%s_%s writes role permissions %d times but only %d are in a guarded shape this test "+
			"reads. The unread ones are invisible to the coverage check above, which would then report "+
			"a repair complete while a backfill it cannot see goes unrepaired.",
			migration.Version, migration.Name, written, read)
	}
	for _, match := range verbGrants {
		if object, guardedObject := match[1], match[6]; object != guardedObject {
			t.Fatalf("%s_%s sets '{objects,%s,%s}' but guards on presence of %q. One of the two is a "+
				"typo, and whichever it is, the block writes into or skips the wrong object.",
				migration.Version, migration.Name, object, match[2], guardedObject)
		}
	}
	for _, match := range matches {
		object, payload, roleList, singleRole, guardedObject := match[1], match[2], match[3], match[4], match[5]
		if object != guardedObject {
			t.Fatalf("%s_%s sets '{objects,%s}' but guards on absence of %q. One of the two is a typo, and "+
				"whichever it is, the block writes or skips the wrong object.",
				migration.Version, migration.Name, object, guardedObject)
		}
		if roleList == "" {
			roleList = singleRole
		}
		for _, role := range quotedRolePattern.FindAllStringSubmatch(roleList, -1) {
			grants[rbacGrant{object: object, role: role[1]}] = normalizePayload(t, payload)
		}
	}
}

// repairMigrations returns a namespace's repair migrations, identified by name.
// The naming is the declaration: a migration that re-applies other migrations'
// writes says so in its filename, because nothing in the SQL distinguishes a
// repair from the backfill it mirrors.
func repairMigrations(namespace dbmigrate.Namespace) []dbmigrate.Migration {
	var repairs []dbmigrate.Migration
	for _, migration := range namespace.Migrations {
		if isRepair(migration) {
			repairs = append(repairs, migration)
		}
	}
	return repairs
}

func isRepair(migration dbmigrate.Migration) bool {
	return strings.HasSuffix(migration.Name, "rbac_backfill_repair")
}

// normalizePayload canonicalizes a permission literal so two blocks granting the
// same permission compare equal however they were written. Folding whitespace
// alone would leave key ORDER significant, and the mismatch it then reported
// would show two payloads a reader cannot tell apart.
//
// Decoded as `any`, not `bool`: a bool target would read JSON null as false and
// call `{"create":null}` equal to `{"create":false}`. Postgres stores those as
// different JSONB, and `permissions->'objects'->'x'->>'create'` returns NULL for
// one and "false" for the other — a difference this comparison must not erase.
func normalizePayload(t *testing.T, payload string) string {
	t.Helper()
	var verbs map[string]any
	if err := json.Unmarshal([]byte(payload), &verbs); err != nil {
		t.Fatalf("a grant payload is not a permission document: %s (%v). The derivation reads these as "+
			"the thing being compared, so one it cannot parse silently compares as a string instead.",
			payload, err)
	}
	canonical, err := json.Marshal(verbs)
	if err != nil {
		t.Fatalf("re-encoding the grant payload %s: %v", payload, err)
	}
	return string(canonical)
}

func sortedGrants(grants map[rbacGrant]string) []rbacGrant {
	ordered := make([]rbacGrant, 0, len(grants))
	for grant := range grants {
		ordered = append(ordered, grant)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].object != ordered[j].object {
			return ordered[i].object < ordered[j].object
		}
		return ordered[i].role < ordered[j].role
	})
	return ordered
}
