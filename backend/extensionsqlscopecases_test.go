// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The SQL-scope gate's own test, driven with SYNTHETIC unit sources rather than
// the tree — the real units are supposed to pass, so a gate proven only by
// "extensions/ is currently clean" is one that keeps passing after it stops
// working. A fixture unit could not carry these either: a fixture naming a core
// table would have to fail the gate to prove anything.
//
// The gate has a second way to read green that a clean tree hides completely:
// seeing no SQL at all. Every case therefore pins how many table references were
// READ alongside the verdict, and each rule below was checked by breaking it —
// the case is here because the gate without that rule fails it.

import (
	"strings"
	"testing"
)

// probeUnit is the synthetic unit the gate's own test is written against. Its
// namespace is ext_probe, so ext.ext_probe_note is its table and everything else
// in the cases below is somebody's else's.
const probeUnit = "probe"

// extSQLGateCase is one source the gate must judge a particular way. want is the
// text the refusal has to carry, or "" when the source must be accepted.
type extSQLGateCase struct {
	name string
	body string
	// decls is package-level source this case needs beside the shared ones. It
	// is per case rather than shared because a declaration holding SQL is read
	// by every case that can see it.
	decls  string
	want   string
	tables int // table references the gate is expected to have read
}

// extSQLGateCases exercise the gate against a unit it can refuse. The real tree
// is supposed to pass, so a gate proven only by "extensions/ is currently clean"
// is one that keeps passing after it stops working — and this one has a second
// way to read green that a clean tree hides completely: seeing no SQL at all.
// Every accepted case therefore also pins how many table references were READ.
var extSQLGateCases = []extSQLGateCase{
	{
		name:   "a core table named inline",
		body:   `tx.Exec(ctx, "SELECT id FROM person WHERE id = $1")`,
		want:   `"person": the unit probe addresses ext.ext_probe_…`,
		tables: 1,
	},
	{
		name:   "a core table named through the public schema",
		body:   `tx.Exec(ctx, "DELETE FROM public.person WHERE id = $1")`,
		want:   `"public.person"`,
		tables: 1,
	},
	{
		name: "a core table reached through a constant",
		body: `tx.Exec(ctx, "SELECT id FROM "+subject+" WHERE id = $1")`,
		// The spelling the one unit shipping SQL uses for its OWN table. A gate
		// blind to the concatenation reads the whole tier green.
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "another unit's table",
		body:   `tx.Exec(ctx, "SELECT body FROM ext.ext_notes_note LIMIT 1")`,
		want:   `"ext.ext_notes_note": the unit probe addresses`,
		tables: 1,
	},
	{
		name:   "a core table joined onto the unit's own",
		body:   `tx.Exec(ctx, "SELECT n.id FROM ext.ext_probe_note n JOIN person p ON p.id = n.subject_id")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table reached around a DELETE … USING",
		body:   `tx.Exec(ctx, "DELETE FROM ext.ext_probe_note USING person WHERE person.id = ext_probe_note.subject_id")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table written by an INSERT",
		body:   `tx.Exec(ctx, "INSERT INTO activity (kind, subject_id) VALUES ($1, $2)")`,
		want:   `"activity"`,
		tables: 1,
	},
	{
		name:   "a core table rewritten by an UPDATE",
		body:   `tx.Exec(ctx, "UPDATE person SET full_name = $1 WHERE id = $2")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "the unit's own table left unqualified",
		body:   `tx.Exec(ctx, "SELECT id FROM ext_probe_note LIMIT 1")`,
		want:   "the ext schema is on no search_path",
		tables: 1,
	},
	{
		name:   "a scratch table created over a core name",
		body:   `tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS person (id uuid)")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "a table name assembled at runtime",
		body:   `tx.Exec(ctx, fmt.Sprintf("SELECT id FROM %s LIMIT 1", subject))`,
		want:   "this gate cannot read",
		tables: 1,
	},
	{
		name:   "the unit's own table, schema-qualified through a constant",
		body:   `tx.Exec(ctx, "SELECT id FROM "+noteTable+" ORDER BY id LIMIT 1")`,
		tables: 1,
	},
	{
		name:   "a CTE the same statement declares",
		body:   `tx.Exec(ctx, "WITH stale AS (SELECT id FROM "+noteTable+" ORDER BY id DESC OFFSET 50) DELETE FROM "+noteTable+" WHERE id IN (SELECT id FROM stale)")`,
		tables: 2,
	},
	{
		name:   "a catalog read",
		body:   `tx.Exec(ctx, "SELECT column_name FROM information_schema.columns WHERE table_name = $1")`,
		tables: 1,
	},
	{
		name:   "EXTRACT's argument separator",
		body:   `tx.Exec(ctx, "SELECT extract(epoch FROM created_at) FROM "+noteTable)`,
		tables: 1,
	},
	{
		name:   "a row lock and an upsert clause",
		body:   `tx.Exec(ctx, "INSERT INTO "+noteTable+" (id) VALUES ($1) ON CONFLICT (id) DO UPDATE SET body = $2 RETURNING (SELECT body FROM "+noteTable+" WHERE id = $1 FOR UPDATE)")`,
		tables: 2,
	},
	{
		name:   "a set-returning function and a USING column list",
		body:   `tx.Exec(ctx, "SELECT n.id FROM "+noteTable+" n JOIN unnest($1::uuid[]) AS wanted(id) USING (id)")`,
		tables: 1,
	},
	{
		name:   "a core table updated from a WITH clause",
		body:   `tx.Exec(ctx, "WITH chosen AS (SELECT id FROM "+noteTable+") UPDATE person SET full_name = $1 WHERE id IN (SELECT id FROM chosen)")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table in an old-style comma join",
		body:   `tx.Exec(ctx, "SELECT n.id FROM "+noteTable+" n, person p WHERE p.id = n.subject_id")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name: "a core table REWRITTEN behind a CTE of its own name",
		// PostgreSQL has no such thing as writing a CTE: the target of an
		// UPDATE, DELETE, INSERT or TRUNCATE always resolves to the real table,
		// however the WITH list names itself. Exempting the name in a write
		// position is a two-token way past the whole gate — and the unit's own
		// table, read inside the CTE body, keeps the reference count looking
		// honest while it happens.
		body:   `tx.Exec(ctx, "WITH person AS (SELECT id FROM "+noteTable+") UPDATE person SET full_name = $1")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table DELETED behind a CTE of its own name",
		body:   `tx.Exec(ctx, "WITH person AS (SELECT id FROM "+noteTable+") DELETE FROM person WHERE id = $1")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name: "a core table inside a DO block",
		// The one statement whose quoted body IS the statement. Everywhere else
		// a dollar-quoted body is a value and gets stripped; here stripping it
		// would delete the only place the DML is written.
		body:   "tx.Exec(ctx, `DO $$ BEGIN DELETE FROM person; END $$`)",
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "a core table inside a quoted DO body",
		body:   `tx.Exec(ctx, "DO 'DELETE FROM person'")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name: "a core table read out by COPY",
		// COPY … TO STDOUT needs only SELECT, which the shared runtime role
		// holds on every core table, so this is a read of the whole table.
		body:   `tx.Exec(ctx, "COPY person TO STDOUT")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name: "a core table behind a set-returning function in a FROM list",
		// One non-table entry must not shield the rest of the list.
		body:   `tx.Exec(ctx, "SELECT * FROM generate_series(1,1) g, person p, "+noteTable+" n WHERE true")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table second in a DELETE … USING list",
		body:   `tx.Exec(ctx, "DELETE FROM "+noteTable+" USING "+noteTable+" x, person WHERE true")`,
		want:   `"person"`,
		tables: 3,
	},
	{
		name: "the bulk-delete idiom against the unit's own table",
		// `DELETE FROM own USING unnest($1)` is how a unit deletes a batch, and
		// a gate that reads `unnest` as a table refuses it in the merge gate
		// with a message about a table nobody named.
		body:   `tx.Exec(ctx, "DELETE FROM "+noteTable+" USING unnest($1::uuid[]) AS wanted(id) WHERE "+noteTable+".id = wanted.id")`,
		tables: 1,
	},
	{
		name:   "COPY naming its endpoint rather than a second table",
		body:   `tx.Exec(ctx, "COPY "+noteTable+" FROM STDIN")`,
		tables: 1,
	},
	{
		name: "a bare TRUNCATE, which is read as prose",
		// The stated half of the trade the shape list makes: reading this
		// spelling means reading every sentence that opens with the word. The
		// prose case below is its other half, and both move together.
		body:   `tx.Exec(ctx, "TRUNCATE person")`,
		tables: 0,
	},
	{
		name: "a core table truncated beside the unit's own",
		// TRUNCATE reaches the first name past the qualifier, and TABLE reaches
		// it as a keyword of its own: one mistake, and it must be reported once.
		// The list is what carries the second name.
		body:   `tx.Exec(ctx, "TRUNCATE TABLE "+noteTable+", person")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table behind the statement's own comment",
		body:   "tx.Exec(ctx, `-- the stale rows\nDELETE FROM person WHERE id = $1`)",
		want:   `"person"`,
		tables: 1,
	},
	{
		name: "a core table shadowed by a CTE of the same name",
		// The CTE's own body still reads the core table; only what follows the
		// body is the CTE. Exempting the name everywhere is a one-line evasion.
		body:   `tx.Exec(ctx, "WITH person AS (SELECT id FROM person) SELECT id FROM person")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name: "a core table under a name bound two ways",
		// Two functions, one unit-wide fold: the second binding must not answer
		// for the first.
		body:   `tx.Exec(ctx, "SELECT id FROM "+where+" LIMIT 1"); _ = func() { where := noteTable; _ = where }`,
		want:   "this gate cannot read",
		tables: 1,
	},
	{
		name: "a core table under a name a call also binds",
		// The readable binding comes SECOND here, which is the order the
		// disagreement rule above does not cover on its own: the fold would
		// otherwise take the one value it could read and answer with it, for a
		// name whose value at the statement is whatever pick() returned.
		body:   `chosen := pick(); chosen = noteTable; tx.Exec(ctx, "SELECT id FROM "+chosen)`,
		want:   "this gate cannot read",
		tables: 1,
	},
	{
		name:   "the unit's own table, quoted the way PostgreSQL qualifies one",
		body:   "tx.Exec(ctx, `SELECT id FROM \"ext\".\"ext_probe_note\" LIMIT 1`)",
		tables: 1,
	},
	{
		name:   "a CTE that declares its column list",
		body:   `tx.Exec(ctx, "WITH stale(id) AS (SELECT id FROM "+noteTable+") DELETE FROM "+noteTable+" WHERE id IN (SELECT id FROM stale)")`,
		tables: 2,
	},
	{
		name:   "a recursive CTE naming itself",
		body:   `tx.Exec(ctx, "WITH RECURSIVE walk AS (SELECT id, parent_id FROM "+noteTable+" UNION ALL SELECT n.id, n.parent_id FROM "+noteTable+" n JOIN walk w ON w.parent_id = n.id) SELECT id FROM walk")`,
		tables: 2,
	},
	{
		name:   "a dollar-quoted body that reads like a statement",
		body:   `tx.Exec(ctx, "SELECT $$FROM person$$ AS example FROM "+noteTable)`,
		tables: 1,
	},
	{
		name: "a statement assembled through a chain of constants",
		body: `tx.Exec(ctx, statement)`,
		// Four links deep, declared out of order: the fold resolves one link a
		// pass and must run to a fixed point, not a fixed number of passes.
		decls: `const statement = opener + body
const opener = verb + " "
const verb = keyword
const keyword = upper + lower
const upper = "SE"
const lower = "LECT"
const body = columns + source
const columns = "id "
const source = "FROM person LIMIT 1"`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "prose that merely reads like SQL",
		body:   `_ = "hello from the demo extension"; _ = "update the note, then select the row"; _ = "truncate the note body before sending"; _ = "do the work, then copy the note"`,
		tables: 0,
	},
}

// TestExtensionSQLScopeRefusesWhatItMust drives the gate with sources the real
// tree does not contain.
func TestExtensionSQLScopeRefusesWhatItMust(t *testing.T) {
	for _, probe := range extSQLGateCases {
		t.Run(probe.name, func(t *testing.T) {
			scan := scanUnitSQL(t, probeUnit, map[string]string{"probe.go": probeSource(probe.body, probe.decls)})
			if probe.tables != scan.tables {
				t.Errorf("the gate read %d table reference(s), want %d — a case whose SQL the reader never saw proves nothing about the verdict below", scan.tables, probe.tables)
			}
			switch {
			case probe.want == "" && len(scan.findings) > 0:
				t.Errorf("the gate refused what it must accept: %s", strings.Join(scan.findings, "; "))
			case probe.want == "":
			case len(scan.findings) != 1:
				t.Errorf("the gate returned %d finding(s), want the one refusing %q: %s", len(scan.findings), probe.want, strings.Join(scan.findings, "; "))
			case !strings.Contains(scan.findings[0], probe.want):
				t.Errorf("the gate refused the source but the reason does not mention %q: %s", probe.want, scan.findings[0])
			}
		})
	}
}

// probeSource wraps a case body in a compilable unit whose constants are the
// two a real unit declares: its own table, and — for the cases that reach past
// it — a core one.
func probeSource(body, decls string) string {
	return `package probe

const noteTable = "ext.ext_probe_note"
const subject = "person"
const where = "person"

func pick() string { return subject }

func run(ctx context.Context, tx Tx) { ` + body + ` }
` + decls + "\n"
}
