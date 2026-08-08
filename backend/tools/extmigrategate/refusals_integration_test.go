// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The seven named tenancy breaks have a test each next door, because each is
// worth reading on its own. The allowlist has more edges than those seven, and
// every one of them is a way to keep a tenant table's rows reachable from
// outside its workspace — so they are all provoked here, table-driven, one row
// per edge.
//
// A refusal with no test behind it is a refusal nobody has ever seen fire.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// refusal is one violation: the SQL that provokes it and the substrings the
// message must carry for an author to be able to act on it.
type refusal struct {
	tag         string // feeds the unit name, so each case gets its own role
	up          func(ns string) string
	down        func(ns string) string
	mustMention []string
}

func dropNote(ns string) string { return scaffoldDown(ns) }

func TestGateRefusesEveryShapeOutsideTheAllowlist(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.tag, func(t *testing.T) {
			unit := unitName(t, c.tag)
			ns := namespaceOf(t, unit)
			err := runGate(t, unit, migrationDir(t, c.up(ns), c.down(ns)))
			requireRefusal(t, err, c.mustMention...)
		})
	}
}

// valid is the shape the gate accepts; the grant and object rows below add one
// thing to it, and the policy and column rows replace one part of it.
func valid(ns string) string { return scaffoldUp(ns) }

// policyBody is the correct policy body, so a row that varies the command or
// the roles is not also varying the predicate.
var policyBody = "USING " + predicateSQL + " WITH CHECK " + predicateSQL

// refusalCases is split into three groups only because the craft gate caps a
// function's length; the grouping follows what each row is about.
func refusalCases() []refusal {
	cases := grantRefusals()
	cases = append(cases, policyRefusals()...)
	return append(cases, relationRefusals()...)
}

// grantRefusals: who may hold a privilege on an extension table, and which.
func grantRefusals() []refusal {
	return []refusal{{
		tag:  "grantpub",
		up:   func(ns string) string { return valid(ns) + fmt.Sprintf("GRANT SELECT ON ext.%s_note TO PUBLIC;\n", ns) },
		down: dropNote,
		// PUBLIC includes roles created after the unit was installed, which no
		// review of the unit's own SQL can enumerate.
		mustMention: []string{"_note", "to PUBLIC"},
	}, {
		tag: "granttrunc",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("GRANT TRUNCATE ON ext.%s_note TO margince_app;\n", ns)
		},
		down:        dropNote,
		mustMention: []string{"margince_app", "arwdD"},
	}, {
		tag: "grantother",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("GRANT SELECT ON ext.%s_note TO margince_owner;\n", ns)
		},
		down:        dropNote,
		mustMention: []string{"margince_owner", "only the owner and margince_app"},
	}, {
		tag: "grantcol",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("GRANT UPDATE (body) ON ext.%s_note TO margince_app;\n", ns)
		},
		down:        dropNote,
		mustMention: []string{"column-level grant", "body"},
	}}
}

// policyRefusals: the single policy's dimensions, and the tenant column the
// policy compares.
func policyRefusals() []refusal {
	return []refusal{{
		tag: "twopolicies",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("CREATE POLICY %[1]s_note_extra ON ext.%[1]s_note FOR SELECT USING (true);\n", ns)
		},
		down:        dropNote,
		mustMention: []string{"carries 2 policies", "_note_extra"},
	}, {
		tag: "restrictive",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "AS RESTRICTIVE "+policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"RESTRICTIVE", "admits no row at all"},
	}, {
		tag: "selectonly",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "FOR SELECT USING "+predicateSQL) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{`applies to "SELECT" only`},
	}, {
		// The other three single-command spellings, so the refusal names the
		// command an author actually wrote rather than pg_policy's letter.
		tag: "insertonly",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "FOR INSERT WITH CHECK "+predicateSQL) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{`applies to "INSERT" only`},
	}, {
		tag: "updateonly",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "FOR UPDATE USING "+predicateSQL) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{`applies to "UPDATE" only`},
	}, {
		tag: "deleteonly",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "FOR DELETE USING "+predicateSQL) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{`applies to "DELETE" only`},
	}, {
		tag: "rolescoped",
		up: func(ns string) string {
			return noteTable(ns, "", tenantColumnSQL) + noteRLS(ns, true) +
				notePolicy(ns, "TO margince_app "+policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"scoped TO specific roles"},
	}, {
		// No policy here, deliberately: with a text tenant column the canonical
		// predicate does not even parse (text = uuid), so the fixture would be
		// refused by the CREATE POLICY rather than by the column rule this row
		// exists to exercise.
		tag: "wrongtype",
		up: func(ns string) string {
			return noteTable(ns, "", "workspace_id text NOT NULL")
		},
		down:        dropNote,
		mustMention: []string{"workspace_id is text NOT NULL", "uuid NOT NULL"},
	}, {
		tag: "nullable",
		up: func(ns string) string {
			return noteTable(ns, "", "workspace_id uuid NULL REFERENCES workspace(id) ON DELETE CASCADE") +
				noteRLS(ns, true) + notePolicy(ns, policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"uuid NULL", "uuid NOT NULL"},
	}, {
		tag: "nofk",
		up: func(ns string) string {
			return noteTable(ns, "", "workspace_id uuid NOT NULL") +
				noteRLS(ns, true) + notePolicy(ns, policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"no foreign key onto public.workspace(id)"},
	}, {
		tag: "nocascade",
		up: func(ns string) string {
			return noteTable(ns, "", "workspace_id uuid NOT NULL REFERENCES workspace(id)") +
				noteRLS(ns, true) + notePolicy(ns, policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"without ON DELETE CASCADE", "erasing a tenant"},
	}}
}

// relationRefusals: what may exist in the ext schema at all, and what must be
// gone after the revert.
func relationRefusals() []refusal {
	return []refusal{{
		tag: "unlogged",
		up: func(ns string) string {
			return noteTable(ns, "UNLOGGED ", tenantColumnSQL) +
				noteRLS(ns, true) + notePolicy(ns, policyBody) + noteGrant(ns)
		},
		down:        dropNote,
		mustMention: []string{"relpersistence", "loses its rows"},
	}, {
		tag: "view",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("CREATE VIEW ext.%[1]s_all AS SELECT * FROM ext.%[1]s_note;\n", ns)
		},
		down: func(ns string) string {
			return fmt.Sprintf("DROP VIEW IF EXISTS ext.%s_all;\n", ns) + scaffoldDown(ns)
		},
		mustMention: []string{"is a VIEW", "owner's rights"},
	}, {
		tag: "routine",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("CREATE FUNCTION ext.%s_count() RETURNS bigint LANGUAGE sql AS 'SELECT 1::bigint';\n", ns)
		},
		down: func(ns string) string {
			return fmt.Sprintf("DROP FUNCTION IF EXISTS ext.%s_count();\n", ns) + scaffoldDown(ns)
		},
		mustMention: []string{"routine ext.", "_count", "outside the ext schema's table allowlist"},
	}, {
		tag: "enumtype",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf("CREATE TYPE ext.%s_kind AS ENUM ('a', 'b');\n", ns)
		},
		down: func(ns string) string {
			return fmt.Sprintf("DROP TYPE IF EXISTS ext.%s_kind;\n", ns) + scaffoldDown(ns)
		},
		mustMention: []string{"type ext.", "_kind"},
	}, {
		tag: "defaultacl",
		up: func(ns string) string {
			return valid(ns) + "ALTER DEFAULT PRIVILEGES IN SCHEMA ext GRANT SELECT ON TABLES TO PUBLIC;\n"
		},
		down: func(ns string) string {
			return "ALTER DEFAULT PRIVILEGES IN SCHEMA ext REVOKE SELECT ON TABLES FROM PUBLIC;\n" + scaffoldDown(ns)
		},
		mustMention: []string{"default privileges", "objects that do not exist yet"},
	}, {
		// suppress_redundant_updates_trigger is a built-in owned by the
		// bootstrap superuser, so this provokes the trigger rule WITHOUT also
		// tripping the routine rule, which runs first.
		tag: "trigger",
		up: func(ns string) string {
			return valid(ns) + fmt.Sprintf(
				"CREATE TRIGGER %[1]s_note_squash BEFORE UPDATE ON ext.%[1]s_note FOR EACH ROW EXECUTE FUNCTION suppress_redundant_updates_trigger();\n", ns)
		},
		down:        dropNote,
		mustMention: []string{"_note_squash", "arbitrary code"},
	}, {
		// The down half is syntactically fine and reverts nothing.
		tag:         "partialdown",
		up:          valid,
		down:        func(string) string { return "-- deliberately reverts nothing\n" },
		mustMention: []string{"the down-migrations left these behind", "relation ext."},
	}, {
		// gen-composition's textual rule accepts an UNQUALIFIED create and
		// documents that it fails at apply time because the role holds CREATE
		// on ext alone. This is that claim, exercised.
		tag: "unqualified",
		up: func(ns string) string {
			return fmt.Sprintf("CREATE TABLE %s_note (id uuid NOT NULL PRIMARY KEY);\n", ns)
		},
		down:        func(ns string) string { return fmt.Sprintf("DROP TABLE IF EXISTS %s_note;\n", ns) },
		mustMention: []string{"permission denied for schema public", "0001_gate.up.sql"},
	}}
}

// The gate is also a command, and a command's arguments are part of its
// contract: a missing one must say which, not fail somewhere inside pgx.
func TestGateRefusesIncompleteInvocations(t *testing.T) {
	ctx := context.Background()
	dir := migrationDir(t, scaffoldUp("ext_x"), scaffoldDown("ext_x"))
	dsn := ownerDSN(t)

	for _, c := range []struct {
		name           string
		unit, dir, dsn string
		mustMention    string
	}{
		{"no unit", "", dir, dsn, "-unit is required"},
		{"no dir", "u", "", dsn, "-dir is required"},
		{"no dsn", "u", dir, "", "-dsn is required"},
		{"invalid unit name", "Not A Unit", dir, dsn, "not a valid unit name"},
		{"missing directory", "u", filepath.Join(dir, "nope"), dsn, "reading nope"},
		{"unreachable database", "u", dir, "postgres://nobody@127.0.0.1:1/none", "connecting to the throwaway database"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := run(ctx, c.unit, c.dir, c.dsn)
			requireRefusal(t, err, c.mustMention)
		})
	}
}

// An empty migrations directory is a unit that declared the layer and then
// shipped nothing in it — which reads, everywhere else in the build, as a unit
// with migrations.
func TestGateRefusesAnEmptyMigrationsDirectory(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "migrations")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatalf("creating the empty migrations directory: %v", err)
	}
	err := run(context.Background(), unitName(t, "empty"), empty, ownerDSN(t))
	requireRefusal(t, err, "holds no NNNN_name.up.sql/.down.sql pair")
}

// The gate refuses to share its role with a concurrent run rather than
// adopting or dropping it: the role is cluster-scoped and named from the unit,
// so dropping one in use would corrupt the OTHER run.
func TestGateRefusesWhileAnotherRunHoldsTheRole(t *testing.T) {
	unit := unitName(t, "busy")
	ns := namespaceOf(t, unit)
	owner := admin(t)
	ctx := context.Background()

	password, err := randomPassword()
	if err != nil {
		t.Fatalf("minting the stand-in role's password: %v", err)
	}
	if _, err := owner.Exec(ctx, `CREATE ROLE `+ns+` LOGIN PASSWORD '`+password+`' NOSUPERUSER NOBYPASSRLS`); err != nil {
		t.Fatalf("creating the stand-in role: %v", err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{`DROP OWNED BY ` + ns + ` CASCADE`, `DROP ROLE IF EXISTS ` + ns} {
			if _, err := owner.Exec(context.Background(), statement); err != nil {
				t.Errorf("removing the stand-in role (%s): %v", statement, err)
			}
		}
	})

	held := connectAs(t, ns, password)
	defer func() {
		if err := held.Close(context.Background()); err != nil {
			t.Errorf("closing the held session: %v", err)
		}
	}()

	requireRefusal(t, run(ctx, unit, migrationDir(t, scaffoldUp(ns), scaffoldDown(ns)), ownerDSN(t)),
		"live session", ns)
}

// The gate's whole strength is the privileges its role does NOT hold, so it
// checks them after connecting rather than assuming the CREATE ROLE it just
// issued produced what it asked for. These two provoke that check by changing
// what the cluster grants to PUBLIC underneath it — which is exactly how a
// real cluster would drift into making this gate vacuous.
func TestGateRefusesARoleTheClusterHasWidened(t *testing.T) {
	owner := admin(t)
	ctx := context.Background()

	for _, c := range []struct {
		tag, name, setup, restore, mustMention string
	}{{
		tag:         "widenc",
		name:        "create on public",
		setup:       `GRANT CREATE ON SCHEMA public TO PUBLIC`,
		restore:     `REVOKE CREATE ON SCHEMA public FROM PUBLIC`,
		mustMention: "holds CREATE on schema public",
	}, {
		tag:         "widenu",
		name:        "no usage on public",
		setup:       `REVOKE USAGE ON SCHEMA public FROM PUBLIC`,
		restore:     `GRANT USAGE ON SCHEMA public TO PUBLIC`,
		mustMention: "cannot USE schema public",
	}} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := owner.Exec(ctx, c.setup); err != nil {
				t.Fatalf("%s: %v", c.setup, err)
			}
			// Restored before the assertion runs, so a failing assertion cannot
			// leave the rest of the package running on a widened cluster.
			defer func() {
				if _, err := owner.Exec(context.Background(), c.restore); err != nil {
					t.Fatalf("%s: %v", c.restore, err)
				}
			}()

			unit := unitName(t, c.tag)
			ns := namespaceOf(t, unit)
			requireRefusal(t, runGate(t, unit, migrationDir(t, scaffoldUp(ns), scaffoldDown(ns))), c.mustMention)
		})
	}
}
