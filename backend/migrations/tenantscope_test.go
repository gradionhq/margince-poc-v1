// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// The obligation: a migration statement that writes a tenant table must run with
// app.workspace_id bound to the workspace it names, and must name that workspace
// itself. Tenant tables carry FORCE row-level security with deny-on-unset
// semantics (0014_rls.up.sql), and FORCE binds the table owner — the role
// migrations run as. Two consequences, and each needs its own half of the rule:
//
//   - Unbound, the policy hides every row. An UPDATE, a DELETE, or an
//     INSERT … SELECT reading a tenant table therefore matches nothing and
//     reports SUCCESS: the migration records itself as applied with its data
//     change gone. An INSERT of literal rows is the exception — WITH CHECK
//     rejects it and the deploy halts — so flagging it still matters, but the
//     failure it prevents is a stopped migration, not a silent loss.
//   - Bound, the binding makes rows VISIBLE but does not SCOPE the statement. An
//     executor row-level security does not filter — a superuser or a BYPASSRLS
//     role, which is what developer machines and the unit lane run as — sees
//     every workspace on every iteration, so a loop without an explicit
//     predicate repeats the write N times.
//
// CLAUDE.md carries the rule and the shape it takes. This is the static half of
// enforcing it; the executing half is the RBAC upgrade replay, which migrates as
// a non-superuser owner. Neither subsumes the other: the replay reaches only the
// role table, and a correctly shaped loop can still write the wrong grants.
//
// What this check does NOT see, stated once rather than per-helper: SQL assembled
// at runtime (`EXECUTE format(...)`), a write inside a function body that some
// later statement invokes, and `CREATE TABLE … AS SELECT`. None exists in the
// tree. They are the executing gate's to catch, because no reader of the text can.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// workspaceGUCBinding makes tenant rows visible to an owner RLS binds. The `true`
// third argument scopes it to the transaction: a session-level SET would outlive
// the migration on a pooled connection.
const workspaceGUCBinding = `set_config('app.workspace_id'`

func TestTenantWritesInMigrationsAreWorkspaceScoped(t *testing.T) {
	tenant := tenantTables(t)
	core, custom := namespaces(t)

	for _, ns := range []struct {
		name       string
		migrations []dbmigrate.Migration
	}{
		{"core", core.Migrations},
		{"custom", custom.Migrations},
	} {
		for _, migration := range ns.migrations {
			t.Run(ns.name+"/"+migration.Version+"_"+migration.Name, func(t *testing.T) {
				// Both halves: a down migration that un-backfills is lost to RLS
				// exactly as an up migration is, and a rollback that silently
				// reverts nothing is the harder failure to notice.
				for _, half := range []struct{ name, sql string }{
					{"up", migration.UpSQL},
					{"down", migration.DownSQL},
				} {
					for _, finding := range unscopedTenantWrites(half.sql, tenant) {
						t.Errorf("%s: %s", half.name, finding)
					}
				}
			})
		}
	}
}

// A gate over a clean tree passes whether or not it works, so every shape that
// must fail — and every legitimate shape that must not — is stated here against
// SQL written for the purpose.
//
// tenantScopeCases is a package-level table rather than a literal inside the
// test because it is the specification, not the mechanics: it is read far more
// often than the six lines that run it, and every shape here earns its place by
// having been a real way to get this wrong.
var tenantScopeCases = []struct {
	name    string
	sql     string
	flagged bool
}{{
	name:    "a top-level backfill",
	sql:     `UPDATE role SET permissions = '{}'::jsonb WHERE is_system;`,
	flagged: true,
}, {
	name: "the required shape",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			UPDATE role SET permissions = '{}'::jsonb
			WHERE role.workspace_id = ws; END LOOP; END $$;`,
	flagged: false,
}, {
	name: "a loop that never binds",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			UPDATE role SET permissions = '{}'::jsonb
			WHERE role.workspace_id = ws; END LOOP; END $$;`,
	flagged: true,
}, {
	// The binding is per-iteration state. Hoisted out, it names one
	// workspace for the whole loop, and the write reaches that one alone.
	name: "a binding hoisted outside the loop it should scope",
	sql: `DO $$ BEGIN PERFORM set_config('app.workspace_id', ws::text, true);
			FOR ws IN SELECT id FROM workspace LOOP
			UPDATE role SET permissions = '{}'::jsonb
			WHERE role.workspace_id = ws; END LOOP; END $$;`,
	flagged: true,
}, {
	// A second loop in one block is where a contains-anywhere check fails:
	// the first loop's binding sits earlier in the text than this write.
	name: "a second loop in the same block that does not bind",
	sql: `DO $$ BEGIN
			FOR ws IN SELECT id FROM workspace LOOP
				PERFORM set_config('app.workspace_id', ws::text, true);
				UPDATE role SET permissions = '{}'::jsonb WHERE role.workspace_id = ws;
			END LOOP;
			FOR ws IN SELECT id FROM workspace LOOP
				UPDATE activity SET archived_at = now() WHERE activity.workspace_id = ws;
			END LOOP; END $$;`,
	flagged: true,
}, {
	// The binding search must read a view with comments blanked. Reading raw
	// text lets an author who EXPLAINS the binding satisfy the requirement to
	// perform it.
	name: "a comment that mentions the binding instead of a statement that does it",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			-- already bound above via set_config('app.workspace_id', ws::text, true)
			UPDATE role SET permissions = '{}'::jsonb WHERE role.workspace_id = ws;
			END LOOP; END $$;`,
	flagged: true,
}, {
	// `ws` outlives the loop that declared it, holding the LAST workspace. So
	// this write reads as bound and names a workspace, and reaches one of N.
	name: "a write after END LOOP, riding the loop variable the loop left behind",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			END LOOP;
			UPDATE role SET permissions = '{}'::jsonb WHERE role.workspace_id = ws;
			END $$;`,
	flagged: true,
}, {
	// Same evasion as the comment above, one quoting style further out: a
	// dollar-quoted string is not blanked by the single-quote pass, so its
	// contents would otherwise stand in for the call it only spells.
	name: "a dollar-quoted string that spells the binding instead of a statement that does it",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM log_note($note$ bound via set_config('app.workspace_id', ws::text, true) $note$);
			UPDATE role SET permissions = '{}'::jsonb WHERE role.workspace_id = ws;
			END LOOP; END $$;`,
	flagged: true,
}, {
	name: "a bound loop whose write does not name the workspace it is for",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			UPDATE role SET permissions = '{}'::jsonb; END LOOP; END $$;`,
	flagged: true,
}, {
	// The predicate has to constrain the table being WRITTEN. Constraining
	// only a source relation leaves the target reaching every workspace.
	name: "a predicate that scopes a source relation while the target stays open",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			UPDATE activity SET archived_at = now()
			WHERE organization_id IN (SELECT o.id FROM organization o WHERE o.workspace_id = ws);
			END LOOP; END $$;`,
	flagged: true,
}, {
	name: "an INSERT whose predicate scopes the source it selects from",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			INSERT INTO activity (workspace_id, subject)
			SELECT o.workspace_id, o.name FROM organization o WHERE o.workspace_id = ws;
			END LOOP; END $$;`,
	flagged: false,
}, {
	// DO NOTHING writes only what the source produced, so the source predicate
	// is still the whole scope.
	name: "an upsert whose conflict branch writes nothing",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			INSERT INTO activity (workspace_id, subject)
			SELECT o.workspace_id, o.name FROM organization o WHERE o.workspace_id = ws
			ON CONFLICT DO NOTHING;
			END LOOP; END $$;`,
	flagged: false,
}, {
	// DO UPDATE writes rows already in the target, which the source predicate
	// says nothing about. Under an executor that bypasses row-level security a
	// conflict can land on another workspace's row, so the statement has to
	// name its own target.
	name: "an upsert whose conflict branch updates rows the source never named",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			INSERT INTO activity (workspace_id, subject)
			SELECT o.workspace_id, o.name FROM organization o WHERE o.workspace_id = ws
			ON CONFLICT (workspace_id, subject) DO UPDATE SET subject = EXCLUDED.subject;
			END LOOP; END $$;`,
	flagged: true,
}, {
	// A self-insert carries the TARGET's name on its source predicate, so a
	// whole-statement search would find `activity.workspace_id = ws` and credit
	// the conflict branch with a scope it never declared.
	name: "an upsert whose only predicate belongs to the source it selects from",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			INSERT INTO activity (workspace_id, subject)
			SELECT activity.workspace_id, activity.subject FROM activity WHERE activity.workspace_id = ws
			ON CONFLICT (workspace_id, subject) DO UPDATE SET subject = EXCLUDED.subject;
			END LOOP; END $$;`,
	flagged: true,
}, {
	name: "an upsert whose conflict branch names the workspace it updates",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			INSERT INTO activity (workspace_id, subject)
			SELECT o.workspace_id, o.name FROM organization o WHERE o.workspace_id = ws
			ON CONFLICT (workspace_id, subject) DO UPDATE SET subject = EXCLUDED.subject
			WHERE activity.workspace_id = ws;
			END LOOP; END $$;`,
	flagged: false,
}, {
	name: "a predicate that is only a string literal",
	sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			UPDATE role SET name = 'workspace_id = ws'; END LOOP; END $$;`,
	flagged: true,
}, {
	name:    "a quoted target, which is the same table",
	sql:     `UPDATE "role" SET permissions = '{}'::jsonb WHERE is_system;`,
	flagged: true,
}, {
	name:    "a tenant table's name appearing only as a value",
	sql:     `UPDATE settings SET kind = 'role' WHERE id = 1;`,
	flagged: false,
}, {
	name:    "a write to an untenanted table fed by a tenant read",
	sql:     `INSERT INTO event_outbox (payload) SELECT to_jsonb(a) FROM activity a;`,
	flagged: true,
}, {
	name:    "a trigger naming UPDATE as its event",
	sql:     `CREATE TRIGGER trg BEFORE UPDATE ON organization FOR EACH ROW EXECUTE FUNCTION bump();`,
	flagged: false,
}, {
	name:    "a GRANT listing the write verbs",
	sql:     `GRANT SELECT, INSERT, UPDATE, DELETE ON activity TO margince_app;`,
	flagged: false,
}, {
	name:    "a foreign key's referential action",
	sql:     `ALTER TABLE x ADD CONSTRAINT fk FOREIGN KEY (rid) REFERENCES role(id) ON UPDATE CASCADE;`,
	flagged: false,
}, {
	// RLS does not filter TRUNCATE (the TRUNCATE privilege does, and fails
	// loudly), and COPY FROM against an RLS table is refused outright.
	// Neither can write nothing while reporting success.
	name:    "a TRUNCATE, which row-level security does not filter",
	sql:     `TRUNCATE activity;`,
	flagged: false,
}, {
	name: "a function body, which runs on a later connection that binds for itself",
	sql: `CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $fn$
			BEGIN UPDATE activity SET updated_at = now(); RETURN NEW; END $fn$;`,
	flagged: false,
}, {
	name:    "a documented example inside a comment",
	sql:     "-- e.g. UPDATE role SET permissions = '{}'::jsonb;\nSELECT 1;",
	flagged: false,
}}

func TestTheTenantScopeCheckSeesEveryUnscopedShapeAndPassesTheLegitimateOnes(t *testing.T) {
	tenant := map[string]bool{"role": true, "activity": true, "organization": true}
	for _, tc := range tenantScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			findings := unscopedTenantWrites(tc.sql, tenant)
			if tc.flagged && len(findings) == 0 {
				t.Error("the check reported this clean; it is a shape whose write a deployed installation " +
					"would silently discard or misapply, so the gate would pass the bug it exists to stop")
			}
			if !tc.flagged && len(findings) > 0 {
				t.Errorf("the check flagged a correct migration: %v. A false positive here is not a "+
					"harmless nag — the next author works around the gate instead of trusting it", findings)
			}
		})
	}
}

// unscopedTenantWrites reports every write in sql that touches a tenant table
// without a workspace binding in scope, or without naming its workspace.
//
// Two views of the same text, because the two checks need different things and
// stripComments preserves every offset so the views stay aligned. Statements are
// read from the blanked view, where a table name or a predicate inside a string
// literal cannot raise or satisfy a finding. The binding is looked for in a
// SECOND view that keeps single-quoted literals — `set_config('app.workspace_id'`
// is itself one — and blanks comments, so explaining the binding cannot pass for
// performing it.
func unscopedTenantWrites(sql string, tenant map[string]bool) []string {
	blanked := stripComments(sql)
	topLevel, blocks := executedRegions(blanked)
	var findings []string
	for _, region := range topLevel {
		for _, write := range writeStatements(blanked[region.start:region.end]) {
			if table, touched := tenantTableIn(write, tenant); touched {
				findings = append(findings, "writes the tenant table "+table+" at the top level, where no "+
					"app.workspace_id is bound. Under FORCE row-level security the migration role sees no "+
					"row, so on a deployed installation this either changes nothing while reporting "+
					"success or, for an INSERT of literal rows, fails WITH CHECK and halts the deploy. "+
					"Use the per-workspace loop CLAUDE.md documents.")
			}
		}
	}
	// Two views, aligned because blanking preserves length. The write scan reads
	// `blanked` (comments AND single-quoted literals gone) so a table named in a
	// literal cannot raise a finding; the binding search reads `bindings`
	// (comments gone, single-quoted literals kept) because the binding is itself
	// a literal — and reading the raw text there would let a comment MENTIONING
	// the binding satisfy the requirement to perform one.
	//
	// Both views then lose any NESTED dollar-quoted body. Inside a block region
	// the outer $$ is already stripped, so a $tag$…$tag$ within it is a literal
	// or a function body — either way text this connection does not execute, and
	// either way it must neither raise a finding nor satisfy one. Keeping it in
	// the binding view would let a dollar-quoted string containing the
	// set_config call stand in for making it.
	bindings := blankComments(sql)
	for _, region := range blocks {
		findings = append(findings, unscopedWritesInBlock(
			blankNestedDollarQuoted(blanked[region.start:region.end]),
			blankNestedDollarQuoted(bindings[region.start:region.end]), tenant)...)
	}
	return findings
}

// blankNestedDollarQuoted blanks the body of every dollar-quoted region in a
// block, preserving length so the loop-scope arithmetic still lines up.
func blankNestedDollarQuoted(block string) string {
	out := []byte(block)
	for at := 0; at < len(block); {
		open := dollarTagPattern.FindStringIndex(block[at:])
		if open == nil {
			break
		}
		bodyStart := at + open[1]
		tag := block[at+open[0] : bodyStart]
		end := strings.Index(block[bodyStart:], tag)
		if end < 0 {
			// Unterminated: the database rejects it. Blanking to the end keeps a
			// stray tag from hiding the rest of the block from the write scan.
			end = len(block) - bodyStart
		}
		for i := bodyStart; i < bodyStart+end; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
		at = bodyStart + end + len(tag)
	}
	return string(out)
}

// unscopedWritesInBlock checks one executed-now plpgsql body. Scope is judged
// per enclosing loop, never per block: the binding is per-iteration state, so a
// binding that sits earlier in the text than a write says nothing about whether
// that write ran under one.
func unscopedWritesInBlock(block, bindings string, tenant map[string]bool) []string {
	var findings []string
	for _, write := range writeStatements(block) {
		table, touched := tenantTableIn(write, tenant)
		if !touched {
			continue
		}
		scope := enclosingLoop(block, write.at)
		if !strings.Contains(bindings[scope:write.at], workspaceGUCBinding) {
			findings = append(findings, "writes the tenant table "+table+" without binding "+
				"app.workspace_id inside the loop that write belongs to, so row-level security discards "+
				"it silently. Bind per iteration — "+workspaceGUCBinding+", ws::text, true) — as the "+
				"shape in CLAUDE.md does; a binding hoisted out of the loop names one workspace for all "+
				"of them.")
			continue
		}
		if !write.namesItsWorkspace() {
			findings = append(findings, "writes the tenant table "+table+" inside the workspace loop "+
				"without naming the workspace it is for. Add `AND "+write.target+".workspace_id = ws`: "+
				"the binding only makes rows visible, and an executor row-level security does not filter "+
				"(any superuser, so dev and CI) applies this statement to EVERY workspace on EVERY "+
				"iteration.")
		}
	}
	return findings
}

// enclosingLoop returns the offset just after the LOOP keyword of the innermost
// loop containing at.
//
// A write outside every loop scopes from the last END LOOP before it, not from
// the start of the block. `ws` outlives the loop that declared it, holding the
// LAST workspace, so a write placed after END LOOP still reads as bound and
// still names a workspace — while reaching exactly one of them. Starting the
// search after the loop closed means the binding inside it cannot vouch for a
// write outside it, and the write is reported on the binding rule.
func enclosingLoop(block string, at int) int {
	var open []int
	afterLastClose := 0
	for _, token := range loopTokenPattern.FindAllStringSubmatchIndex(block[:at], -1) {
		if strings.EqualFold(strings.Fields(block[token[0]:token[1]])[0], "END") {
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
			if len(open) == 0 {
				afterLastClose = token[1]
			}
			continue
		}
		open = append(open, token[1])
	}
	if len(open) == 0 {
		return afterLastClose
	}
	return open[len(open)-1]
}

// tenantWrite is one write statement: its text, where it starts, and the target
// the workspace predicate has to constrain.
type tenantWrite struct {
	text   string
	at     int
	verb   string
	target string // the target table, or its alias when the statement gave one
}

// namesItsWorkspace reports whether the statement constrains its own target to
// the loop's workspace. An INSERT is the one exception: it names the workspace on
// the SOURCE it selects from, and the inserted workspace_id comes from that same
// row, so any qualifier is the correct one there.
//
// The exception stops at ON CONFLICT ... DO UPDATE. That branch updates rows
// ALREADY in the target, which the source predicate says nothing about, so the
// statement has to name its own target like any other update. No migration
// upserts a tenant table that way today; requiring it now is what keeps the
// exception from quietly widening to cover one that does.
func (w tenantWrite) namesItsWorkspace() bool {
	searched := w.text
	switch conflict := conflictUpdatePattern.FindStringIndex(w.text); {
	case conflict != nil:
		// Only a predicate inside the conflict branch can scope rows the source
		// never selected. Searching the whole statement would let the source's own
		// predicate stand in for it — and in a self-insert that predicate carries
		// the target's name, so the branch would read as scoped while constraining
		// nothing.
		searched = w.text[conflict[1]:]
	case strings.EqualFold(w.verb, "INSERT INTO"):
		// The exception: a plain insert takes its workspace_id from the row the
		// source produced, so any qualifier is the correct one.
		return qualifiedPredicatePattern.MatchString(w.text)
	}
	for _, predicate := range qualifiedPredicatePattern.FindAllStringSubmatch(searched, -1) {
		if strings.EqualFold(predicate[1], w.target) {
			return true
		}
	}
	return false
}

// writeStatements returns each write statement in sql, from its keyword to the
// end of the statement. Statements are found by keyword rather than by splitting
// on `;` because plpgsql bodies open statements after LOOP and BEGIN, which carry
// no separator of their own.
func writeStatements(sql string) []tenantWrite {
	var writes []tenantWrite
	for _, match := range writeKeywordPattern.FindAllStringSubmatchIndex(sql, -1) {
		verb := strings.Join(strings.Fields(sql[match[2]:match[3]]), " ")
		target := strings.ToLower(strings.Trim(sql[match[6]:match[7]], `"`))
		// Three shapes read as writes and are not. `BEFORE UPDATE ON t` and
		// `ON UPDATE CASCADE` are DDL naming the verb, not performing it; `FOR
		// UPDATE` is a lock on a read. The word before the keyword tells them
		// apart, and `ON` as the apparent target catches the trigger events.
		if target == "on" || ddlVerbContext[precedingWord(sql, match[0])] {
			continue
		}
		// `ON CONFLICT ... DO UPDATE SET` is not a statement of its own — reading it
		// as one yields a write against a table called `set`. It is a branch of the
		// INSERT that opened it, and namesItsWorkspace judges it there.
		if precedingWord(sql, match[0]) == "do" {
			continue
		}
		text, _, _ := strings.Cut(sql[match[0]:], ";")
		writes = append(writes, tenantWrite{
			text: text, at: match[0], verb: verb, target: aliasOr(text, target),
		})
	}
	return writes
}

// aliasOr returns the alias an UPDATE gave its target, or the table name.
func aliasOr(statement, table string) string {
	if alias := updateAliasPattern.FindStringSubmatch(statement); alias != nil {
		return strings.ToLower(alias[1])
	}
	return table
}

// ddlVerbContext holds the words that turn a following write keyword into DDL or
// a lock clause.
var ddlVerbContext = map[string]bool{
	"on": true, "before": true, "after": true, "instead": true,
	"of": true, "or": true, "for": true, "grant": true, "revoke": true,
}

// precedingWord returns the lower-cased word ending immediately before idx.
func precedingWord(sql string, idx int) string {
	head := strings.TrimRight(sql[:idx], " \t\r\n")
	start := strings.LastIndexAny(head, " \t\r\n(,;")
	return strings.ToLower(head[start+1:])
}

// tenantTableIn reports the first tenant table the statement names anywhere —
// target or source alike, since a write fed by an unbound tenant SELECT is lost
// the same way.
func tenantTableIn(write tenantWrite, tenant map[string]bool) (string, bool) {
	for _, word := range identifierPattern.FindAllString(strings.ToLower(write.text), -1) {
		if tenant[word] {
			return word, true
		}
	}
	return "", false
}

// tenantTables derives the RLS-protected set from the migrations themselves,
// two ways because the tree declares it two ways: 0014 loops over a text[] of
// the original tables, and every table added since carries its own ALTER.
func tenantTables(t *testing.T) map[string]bool {
	t.Helper()
	core, custom := namespaces(t)
	tables := map[string]bool{}
	for _, migration := range append(core.Migrations, custom.Migrations...) {
		for _, array := range rlsArrayPattern.FindAllStringSubmatch(migration.UpSQL, -1) {
			for _, quoted := range quotedNamePattern.FindAllStringSubmatch(array[1], -1) {
				tables[quoted[1]] = true
			}
		}
		for _, forced := range forceRLSPattern.FindAllStringSubmatch(migration.UpSQL, -1) {
			tables[strings.ToLower(forced[1])] = true
		}
	}
	// A derivation that silently yields nothing passes every migration, which is
	// the one way a fitness function fails without failing. These four are load-
	// bearing members of both declaration styles.
	for _, expected := range []string{"role", "activity", "organization", "channel_connection"} {
		if !tables[expected] {
			t.Fatalf("the tenant-table derivation found %d tables but not %q, so the RLS declarations "+
				"have moved and the patterns here no longer match them. Fix the derivation: as written "+
				"it reports migrations clean without having looked at the tables that matter.",
				len(tables), expected)
		}
	}
	return tables
}

func namespaces(t *testing.T) (core, custom dbmigrate.Namespace) {
	t.Helper()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading the core namespace: %v", err)
	}
	custom, err = Custom()
	if err != nil {
		t.Fatalf("loading the custom namespace: %v", err)
	}
	return core, custom
}

// region is a half-open [start, end) slice of the migration's text. Regions
// rather than substrings, so both the blanked and the raw view can be indexed by
// the same offsets.
type region struct{ start, end int }

// executedRegions splits SQL into what runs now: the stretches outside any
// dollar-quoted body, and the bodies of DO blocks. A dollar-quoted body that is
// not a DO block is a routine definition, whose statements run when something
// calls it — see the header for what that leaves uncovered.
func executedRegions(sql string) (topLevel, doBlocks []region) {
	for at := 0; at < len(sql); {
		open := dollarTagPattern.FindStringIndex(sql[at:])
		if open == nil {
			topLevel = append(topLevel, region{at, len(sql)})
			break
		}
		tagStart, bodyStart := at+open[0], at+open[1]
		topLevel = append(topLevel, region{at, tagStart})
		tag := sql[tagStart:bodyStart]
		end := strings.Index(sql[bodyStart:], tag)
		if end < 0 {
			// Unterminated quoting is a syntax error the database will reject.
			// Treating the remainder as an unscoped body keeps this check from
			// being the thing that lets it through.
			doBlocks = append(doBlocks, region{bodyStart, len(sql)})
			break
		}
		if doIntroducerPattern.MatchString(sql[:tagStart]) {
			doBlocks = append(doBlocks, region{bodyStart, bodyStart + end})
		}
		at = bodyStart + end + len(tag)
	}
	return topLevel, doBlocks
}

// stripComments blanks comments AND single-quoted literals — the view the write
// scan reads. A table name or a `workspace_id = ws` inside quotes is text, not
// SQL, and must neither raise a finding nor satisfy one. Offsets are preserved
// so the loop-scope arithmetic still lines up.
func stripComments(sql string) string { return blank(sql, true) }

// blankComments blanks comments and leaves single-quoted literals standing — the
// view the BINDING search reads, because the binding it looks for
// (`set_config('app.workspace_id'`) contains a literal of its own and would be
// erased by the pass above. Reading the raw text instead would let a comment that
// merely mentions the binding satisfy the requirement to perform it.
//
// Neither pass recognizes dollar quoting; blankNestedDollarQuoted handles that
// per block, where the outer $$ has already been stripped and a remaining
// $tag$…$tag$ can only be a literal or a body this connection does not run.
func blankComments(sql string) string { return blank(sql, false) }

func blank(sql string, literals bool) string {
	out := []byte(sql)
	blankRange := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(sql); i++ {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.Index(sql[i:], "\n")
			if end < 0 {
				end = len(sql) - i
			}
			blankRange(i, i+end)
			i += end
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				end = len(sql) - i - 2
			}
			blankRange(i, i+end+4)
			i += end + 3
		case sql[i] == '\'':
			end := strings.Index(sql[i+1:], "'")
			if end < 0 {
				end = len(sql) - i - 1
			}
			if literals {
				blankRange(i+1, i+1+end)
			}
			i += end + 1
		}
	}
	return string(out)
}

var (
	rlsArrayPattern     = regexp.MustCompile(`(?is)tenant_tables\s+text\[\]\s*:=\s*ARRAY\s*\[(.*?)\]`)
	quotedNamePattern   = regexp.MustCompile(`'([a-z_][a-z0-9_]*)'`)
	forceRLSPattern     = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+([a-z_][a-z0-9_]*)\s+FORCE\s+ROW\s+LEVEL\s+SECURITY`)
	dollarTagPattern    = regexp.MustCompile(`\$[a-zA-Z_]*\$`)
	doIntroducerPattern = regexp.MustCompile(`(?is)(\A|[\s;])DO\s*\z`)
	// The write verbs row-level security filters. TRUNCATE and COPY are absent
	// deliberately: RLS does not apply to TRUNCATE, and COPY FROM against an RLS
	// table is refused outright, so neither writes nothing while reporting
	// success. A quoted target is the same table as a bare one.
	writeKeywordPattern = regexp.MustCompile(
		`(?is)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM|MERGE\s+INTO)\s+(ONLY\s+)?"?([a-z_][a-z0-9_]*)"?`)
	// An alias only exists when a second name sits between the table and SET;
	// `UPDATE role SET` cannot match, because nothing follows the `SET` it would
	// have to consume as the alias.
	updateAliasPattern = regexp.MustCompile(`(?is)^UPDATE\s+"?[a-z_][a-z0-9_]*"?\s+([a-z][a-z0-9_]*)\s+SET\b`)
	loopTokenPattern   = regexp.MustCompile(`(?is)\bEND\s+LOOP\b|\bLOOP\b`)
	identifierPattern  = regexp.MustCompile(`[a-z_][a-z0-9_]*`)
	// The workspace predicate and the relation it constrains.
	qualifiedPredicatePattern = regexp.MustCompile(`(?is)\b([a-z_][a-z0-9_]*)\.workspace_id\s*=\s*ws\b`)
	// The upsert branch that writes rows the INSERT's source never selected.
	conflictUpdatePattern = regexp.MustCompile(`(?is)\bON\s+CONFLICT\b.*?\bDO\s+UPDATE\b`)
)
