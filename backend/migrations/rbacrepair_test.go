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

// Every `UPDATE role SET permissions = jsonb_set(` in the tree, guarded or not.
// The count has to equal the guarded-pattern count or the derivation is blind to
// a shape it should be reading.
var anyRolePermissionWrite = regexp.MustCompile(`UPDATE role SET permissions = jsonb_set\(`)

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
		})
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
// fails if the migration writes role permissions in a shape the pattern misses.
func collectGrants(t *testing.T, migration dbmigrate.Migration, grants map[rbacGrant]string) {
	t.Helper()
	matches := guardedBackfillPattern.FindAllStringSubmatch(migration.UpSQL, -1)
	if written := len(anyRolePermissionWrite.FindAllString(migration.UpSQL, -1)); written != len(matches) {
		t.Fatalf("%s_%s writes role permissions %d times but only %d are in the guarded shape this test "+
			"reads. The unread ones are invisible to the coverage check above, which would then report "+
			"a repair complete while a backfill it cannot see goes unrepaired.",
			migration.Version, migration.Name, written, len(matches))
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
			grants[rbacGrant{object: object, role: role[1]}] = normalizePayload(payload)
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

// normalizePayload strips the formatting a JSON literal may vary in, so two
// blocks granting the same permission compare equal however they were spaced.
func normalizePayload(payload string) string {
	return strings.Join(strings.Fields(payload), "")
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
