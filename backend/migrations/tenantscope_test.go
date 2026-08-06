// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// The obligation: a migration statement that writes while touching a tenant
// table must first bind app.workspace_id. Tenant tables carry FORCE ROW LEVEL
// SECURITY with deny-on-unset semantics (0014_rls.up.sql), and FORCE binds the
// table owner — the role migrations run as. An unbound write matches zero rows
// and reports success, so the migration records itself as applied while the data
// change is simply gone.
//
// It is invisible in development, which is why it needs a gate rather than care:
// the dev owner is the Postgres container's POSTGRES_USER, a superuser, and a
// superuser bypasses RLS outright. A deployed installation's owner is an ordinary
// role, so a migration that works on every developer's machine can do nothing at
// all in production — as 0154_channel_connection_rbac did, leaving every role
// 403ing on the channel routes of an installation seeded before that object
// existed. Its loop is the shape this asks for.
//
// This is the static half. The executing half is the RBAC replay in the
// integration lane, which upgrades a legacy installation as a NON-SUPERUSER
// owner and compares the resulting matrix against what the server seeds. Neither
// subsumes the other: the replay reaches only the role table and would pass a
// loop that wrote the wrong grants; this reaches every table and would pass a
// correctly-shaped loop that wrote nonsense.
//
// Only writes are asked about, and that is the whole obligation rather than a
// convenient subset: an unbound read is silently empty, but nothing about it
// persists until some write consumes it, and every write that so much as
// mentions a tenant table is checked — including one that writes an untenanted
// table from a tenant SELECT.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// workspaceGUCBinding is the only thing that makes a tenant write visible to
// itself. The `true` third argument scopes it to the transaction: a session-level
// SET would outlive the migration on a pooled connection.
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

// A gate over a clean tree passes whether or not it works, so the cases that
// must FAIL are stated here against SQL written for the purpose. Each is a shape
// the tree really contains — the trigger events, the GRANT verb list and the
// function bodies are why the check reads statements instead of grepping for
// keywords, and the losses in 0154 and 20260730130000 are the first case.
func TestTheTenantScopeCheckSeesAnUnscopedWriteAndPassesTheScopedShapes(t *testing.T) {
	tenant := map[string]bool{"role": true, "activity": true, "organization": true}
	for _, tc := range []struct {
		name    string
		sql     string
		flagged bool
	}{{
		name:    "the shape that lost 0154: a top-level backfill",
		sql:     `UPDATE role SET permissions = '{}'::jsonb WHERE is_system;`,
		flagged: true,
	}, {
		name: "a DO block that loops but never binds the workspace",
		sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			UPDATE role SET permissions = '{}'::jsonb; END LOOP; END $$;`,
		flagged: true,
	}, {
		name: "the required shape",
		sql: `DO $$ BEGIN FOR ws IN SELECT id FROM workspace LOOP
			PERFORM set_config('app.workspace_id', ws::text, true);
			UPDATE role SET permissions = '{}'::jsonb; END LOOP; END $$;`,
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
		name: "a function body, which runs later on a connection that binds the GUC itself",
		sql: `CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $fn$
			BEGIN UPDATE activity SET updated_at = now(); RETURN NEW; END $fn$;`,
		flagged: false,
	}, {
		name:    "a documented example inside a comment",
		sql:     "-- e.g. UPDATE role SET permissions = '{}'::jsonb;\nSELECT 1;",
		flagged: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			findings := unscopedTenantWrites(tc.sql, tenant)
			if tc.flagged && len(findings) == 0 {
				t.Error("the check reported this clean; it is exactly the shape whose write a deployed " +
					"installation would silently discard, so the gate would pass the bug it exists to stop")
			}
			if !tc.flagged && len(findings) > 0 {
				t.Errorf("the check flagged a correct migration: %v. A false positive here is not a "+
					"harmless nag — the next author works around the gate instead of trusting it", findings)
			}
		})
	}
}

// unscopedTenantWrites reports every write in sql that touches a tenant table
// without a workspace binding in scope.
func unscopedTenantWrites(sql string, tenant map[string]bool) []string {
	topLevel, blocks := executedSQL(stripLineComments(sql))
	var findings []string
	for _, statement := range writeStatements(topLevel) {
		if table, touched := tenantTableIn(statement, tenant); touched {
			findings = append(findings, "writes the tenant table "+table+" at the top level, where no "+
				"app.workspace_id is bound. FORCE row-level security matches ZERO rows for the migration "+
				"role, so this statement will report success and change nothing on a deployed installation. "+
				"Wrap it in the per-workspace DO block 0154_channel_connection_rbac shows.")
		}
	}
	for _, block := range blocks {
		if strings.Contains(block, workspaceGUCBinding) {
			continue
		}
		for _, statement := range writeStatements(block) {
			if table, touched := tenantTableIn(statement, tenant); touched {
				findings = append(findings, "writes the tenant table "+table+" inside a DO block that never "+
					"binds app.workspace_id, so row-level security discards the write silently. Loop over "+
					"workspace and "+workspaceGUCBinding+", ws::text, true) first, as 0154_channel_connection_rbac does.")
			}
		}
	}
	return findings
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

// executedSQL splits SQL into what runs now: the statements outside any
// dollar-quoted body, and the bodies of DO blocks. A dollar-quoted body that is
// NOT a DO block is a function or procedure definition — it runs later, on an
// app connection that binds the GUC itself, so it belongs to neither.
func executedSQL(sql string) (topLevel string, doBlocks []string) {
	var outside strings.Builder
	for rest := sql; rest != ""; {
		open := dollarTagPattern.FindStringIndex(rest)
		if open == nil {
			outside.WriteString(rest)
			break
		}
		tag := rest[open[0]:open[1]]
		outside.WriteString(rest[:open[0]])
		body := rest[open[1]:]
		quoted, after, closed := strings.Cut(body, tag)
		if !closed {
			// Unterminated quoting is a syntax error the database will reject.
			// Treating the remainder as an unscoped body keeps this check from
			// being the thing that lets it through.
			doBlocks = append(doBlocks, body)
			break
		}
		if doIntroducerPattern.MatchString(outside.String()) {
			doBlocks = append(doBlocks, quoted)
		}
		rest = after
	}
	return outside.String(), doBlocks
}

// writeStatements returns each write statement in sql, from its keyword to the
// end of the statement. Statements are found by keyword rather than by splitting
// on `;` because plpgsql bodies open statements after LOOP and BEGIN, which carry
// no separator of their own.
func writeStatements(sql string) []string {
	var statements []string
	for _, match := range writeKeywordPattern.FindAllStringSubmatchIndex(sql, -1) {
		start, target := match[0], strings.ToLower(sql[match[6]:match[7]])
		// Three shapes read as writes and are not. `BEFORE UPDATE ON t` and
		// `ON UPDATE CASCADE` are DDL naming the verb, not performing it; `FOR
		// UPDATE` is a lock on a read. The word before the keyword tells them
		// apart, and `ON` as the apparent target catches the trigger events.
		if target == "on" || ddlVerbContext[precedingWord(sql, start)] {
			continue
		}
		statement, _, _ := strings.Cut(sql[start:], ";")
		statements = append(statements, statement)
	}
	return statements
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
func tenantTableIn(statement string, tenant map[string]bool) (string, bool) {
	for _, word := range identifierPattern.FindAllString(strings.ToLower(statement), -1) {
		if tenant[word] {
			return word, true
		}
	}
	return "", false
}

// stripLineComments removes `--` comments so a documented example inside one is
// never read as a statement. Block comments are left alone: no migration uses
// them, and a half-understood parser is worse than a narrow one.
func stripLineComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if at := strings.Index(line, "--"); at >= 0 {
			lines[i] = line[:at]
		}
	}
	return strings.Join(lines, "\n")
}

var (
	rlsArrayPattern     = regexp.MustCompile(`(?is)tenant_tables\s+text\[\]\s*:=\s*ARRAY\s*\[(.*?)\]`)
	quotedNamePattern   = regexp.MustCompile(`'([a-z_][a-z0-9_]*)'`)
	forceRLSPattern     = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+([a-z_][a-z0-9_]*)\s+FORCE\s+ROW\s+LEVEL\s+SECURITY`)
	dollarTagPattern    = regexp.MustCompile(`\$[a-zA-Z_]*\$`)
	doIntroducerPattern = regexp.MustCompile(`(?is)(\A|[\s;])DO\s*\z`)
	writeKeywordPattern = regexp.MustCompile(`(?is)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(ONLY\s+)?([a-z_][a-z0-9_]*)`)
	identifierPattern   = regexp.MustCompile(`[a-z_][a-z0-9_]*`)
)
