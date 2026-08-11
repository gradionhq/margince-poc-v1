// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// migrationsLayer is the unit subdirectory holding its SQL migrations —
// the first capability layer this generator composes beyond Go
// registrations, and therefore the first to leave unbuiltCapabilityLayers.
//
// Taken from the published surface rather than restated: cmd/migrate
// applies extension.Extension.Migrations from exactly this directory, and
// a generator validating a different one would bless files nothing runs.
const migrationsLayer = extension.MigrationsDir

// pgIdentifierBudget is PostgreSQL's NAMEDATALEN-1. An identifier longer
// than this is TRUNCATED, silently: no warning, no error, and two long
// names that agree in their first 63 bytes become one object. That silence
// is the whole reason the budget is checked here rather than being left to
// the database to complain about.
const pgIdentifierBudget = 63

// unitTables is one unit's declared table suffixes: the bare names its
// migrations append to the unit's ext_<name>_ namespace.
type unitTables struct {
	name   string
	tables []string
}

// checkDerivedIdentifiers validates the composed set's table identifiers at
// the point where they are DERIVED — `ext_` + the unit namespace + `_` + the
// table suffix — rather than at the names that feed the derivation.
//
// Both refusals are structurally invisible to a single unit:
//
//   - The collision is at the join. Unit "a-b" table "c" and unit "a" table
//     "b_c" both derive ext_a_b_c. Neither name nor suffix repeats, so
//     nothing a unit can inspect about itself shows the clash, and the two
//     units cannot see each other. Only this generator sees the whole set.
//   - The budget is silent. Postgres truncates past 63 bytes without
//     complaint, so an over-long identifier reaches production working, and
//     the collision it may have caused surfaces as corrupt data rather than
//     as an error.
//
// Every refusal names the offending unit AND table: an author reading it has
// no other way to locate the other side of a collision.
func checkDerivedIdentifiers(units []unitTables) error {
	// origin remembers which unit contributed an identifier, so the second
	// occurrence can name the first.
	type origin struct{ unit, table string }
	seen := make(map[string]origin)
	for _, u := range units {
		namespace, err := extension.Name(u.name).Namespace()
		if err != nil {
			return fmt.Errorf("extensions/%s: %w", u.name, err)
		}
		for _, table := range u.tables {
			derived, err := derivedIdentifier(namespace, table)
			if err != nil {
				return fmt.Errorf("extensions/%s: %w", u.name, err)
			}
			if prev, dup := seen[derived]; dup {
				return fmt.Errorf(
					"derived table identifier %q is claimed twice: extension %q table %q and extension %q table %q both derive it — the collision is at the join, so neither unit can see it alone",
					derived, prev.unit, prev.table, u.name, table)
			}
			seen[derived] = origin{unit: u.name, table: table}
		}
	}
	return nil
}

// derivedIdentifier joins a unit's namespace with a table suffix and checks
// the one thing the join can get wrong without seeing any other unit: the
// byte budget. It is the single spelling of both, so the per-file refusal
// (which can quote a line) and the whole-set refusal cannot drift apart.
func derivedIdentifier(namespace, table string) (string, error) {
	derived := namespace + "_" + table
	if len(derived) > pgIdentifierBudget {
		return "", fmt.Errorf(
			"table %q derives the identifier %q, %d bytes — PostgreSQL truncates silently past %d, so shorten the table name by %d byte(s) (%s_ leaves %d)",
			table, derived, len(derived), pgIdentifierBudget,
			len(derived)-pgIdentifierBudget, namespace, pgIdentifierBudget-len(namespace)-1)
	}
	return derived, nil
}

// collectUnitTables is the migrations layer's own composition rule, the
// thing that replaces the blanket "not built yet" refusal scanUnit used to
// apply to migrations/. It reads the tables a unit declares and returns
// their suffixes for the cross-unit check.
//
// A unit without migrations/ declares no tables; that is the common case and
// not an error. `present` distinguishes that case from a unit that ships the
// layer, which is what the declaration reader needs to know: the SQL being on
// disk and the SQL being applied are two facts, and only the Extension
// literal's Migrations field connects them.
//
// On the schema: declaredTables checks the qualifier only when one is WRITTEN,
// so a bare `CREATE TABLE ext_notes_note (…)` is accepted here even though
// an unqualified name resolves through search_path rather than to ext. That is
// not a silent hole — the unit's migrations are applied as the ext_<name>
// role, which holds CREATE on ext alone, so an unqualified create fails loudly
// at apply time — but the rule this function enforces is "no OTHER schema is
// named", not "every table lands in ext".
func collectUnitTables(name, dir string) (tableNames []string, present bool, err error) {
	layer := filepath.Join(dir, migrationsLayer)
	entries, err := os.ReadDir(layer)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// The layer EXISTS, and that is the whole question `present` answers. It is
	// deliberately not "an .up.sql was found": a layer holding only a .down.sql
	// is an incomplete pair, and keying presence on the up half would let such a
	// unit omit the Migrations field, which in turn is what makes the migration
	// gate skip it — so the broken pair would be validated by nothing, on the
	// strength of being broken. Present here, refused there.
	present = true
	namespace, err := extension.Name(name).Namespace()
	if err != nil {
		return nil, false, fmt.Errorf("extensions/%s: %w", name, err)
	}
	var tables []string
	for _, e := range entries {
		if e.IsDir() {
			// dbmigrate.Load reads ONE directory and does not descend, so a
			// nested migration would be ignored at apply time while looking
			// applied in the tree — silence, not a partial apply.
			return nil, false, fmt.Errorf("extensions/%s: %s/%s/ is a directory — the migrations layer is a flat set of .sql files, and a nested one would never be applied", name, migrationsLayer, e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".sql") {
			return nil, false, fmt.Errorf("extensions/%s: %s/%s is not a .sql file — the migrations layer holds NNNN_name.up.sql / .down.sql pairs and nothing else", name, migrationsLayer, e.Name())
		}
		// Same reason as the api layer's: os.ReadFile on a FIFO blocks until
		// something writes, so a non-regular entry hangs composition rather
		// than failing it.
		if !e.Type().IsRegular() {
			return nil, false, fmt.Errorf("extensions/%s: %s/%s is not a regular file — this composer will not read a migration through a link, a device or a pipe", name, migrationsLayer, e.Name())
		}
		// Only the up half creates tables; the down half drops them, and
		// scanning it would double-count every name.
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join(layer, e.Name()))
		if err != nil {
			return nil, false, err
		}
		found, err := declaredTables(name, namespace, filepath.ToSlash(filepath.Join(migrationsLayer, e.Name())), string(sqlBytes))
		if err != nil {
			return nil, false, err
		}
		tables = append(tables, found...)
	}
	// Sorted so a refusal reads the same on every machine: os.ReadDir is
	// ordered, but the suffixes within one file are not otherwise ranked.
	sort.Strings(tables)
	return tables, present, nil
}

// createTablePattern finds the target of a CREATE TABLE. It is deliberately
// permissive about what it CAPTURES — any identifier-ish run, including a
// schema qualifier and double quotes — because everything it captures is then
// validated: a shape this pattern matches but declaredTables cannot read is a
// refusal, never a silent skip.
//
// That holds for what the pattern SEES; it is not a claim that the pattern
// sees every CREATE TABLE. See declaredTables on why this gate is best-effort.
var createTablePattern = regexp.MustCompile(
	`(?is)\bCREATE\s+(?:(?:GLOBAL|LOCAL)\s+)?(?:(?:TEMPORARY|TEMP|UNLOGGED)\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_."]+)`)

// declaredTables extracts the table suffixes one up-migration declares.
//
// Text, not a SQL parse: this generator has no database and must run in a
// bare checkout. Masking comments and literals first keeps a table name in
// prose or in a function body from reading as a declaration, and every shape
// the pattern matches but cannot read with confidence is refused.
//
// This is a BEST-EFFORT gate, not a complete one, and the trade is deliberate:
// a textual scanner that handled the remaining shapes would be a SQL parser,
// which is the wrong thing to own here. Known gaps, all of them obscure and
// all of them closed at apply time by extmigrategate's catalog gate (Task 7),
// which applies the SQL as the restricted ext_<name> role and then asks
// PostgreSQL what actually exists:
//
//   - A CREATE TABLE inside a DO $$ … $$ block is masked away as a
//     dollar-quoted body yet executes, so the table it creates is invisible
//     here.
//   - A quote inside a double-quoted identifier (ext."it's") or a
//     backslash-escaped quote in an E'…' string desynchronises maskNonCode's
//     literal tracking, which can blank real statements after it.
//   - Only tables are collected. CREATE INDEX, SEQUENCE, VIEW and MATERIALIZED
//     VIEW share PostgreSQL's per-schema relation namespace with tables, so a
//     unit's index named ext_a_b_c would collide with another unit's table of
//     that derived name and checkDerivedIdentifiers would not see it. The
//     brief scopes this rule to tables; the catalog gate enumerates every
//     relkind.
func declaredTables(unit, namespace, rel, sql string) ([]string, error) {
	masked := maskNonCode(sql)
	var tables []string
	for _, m := range createTablePattern.FindAllStringSubmatchIndex(masked, -1) {
		raw := masked[m[2]:m[3]]
		line := 1 + strings.Count(masked[:m[2]], "\n")
		where := fmt.Sprintf("extensions/%s: %s:%d", unit, rel, line)

		ident := strings.ReplaceAll(raw, `"`, "")
		if schema, rest, qualified := strings.Cut(ident, "."); qualified {
			if schema != extSchema {
				return nil, fmt.Errorf("%s: CREATE TABLE %s targets schema %q — an extension's tables live in the %s schema, which is the only one its ext_<name> role can write", where, raw, schema, extSchema)
			}
			ident = rest
		}
		if strings.Contains(ident, ".") {
			return nil, fmt.Errorf("%s: CREATE TABLE %s is not a plain schema-qualified name", where, raw)
		}
		suffix, ok := strings.CutPrefix(ident, namespace+"_")
		if !ok {
			return nil, fmt.Errorf("%s: CREATE TABLE %s is outside the unit's namespace — an extension table is %s_<table>, which is what keeps two units in the %s schema from colliding or addressing each other's rows", where, raw, namespace, extSchema)
		}
		if suffix == "" {
			return nil, fmt.Errorf("%s: CREATE TABLE %s has an empty table name after the %s_ namespace", where, raw, namespace)
		}
		// Checked here as well as over the whole set so the refusal can name
		// the line the author has to shorten.
		if _, err := derivedIdentifier(namespace, suffix); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		tables = append(tables, suffix)
	}
	return tables, nil
}

// extSchema is the one schema an extension's migrations may create in; the
// core owns public (see migrations/core/0211_ext_schema.up.sql).
const extSchema = "ext"

// maskNonCode blanks SQL comments and quoted literals, preserving byte
// offsets and newlines so a refusal can still report the real line. Blanking
// rather than deleting is the point: the position an author has to go fix is
// the only handle they have on a generated identifier.
func maskNonCode(sql string) string {
	out := []byte(sql)
	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = len(sql) - i
			}
			blank(i, i+end)
			i += end
		case strings.HasPrefix(sql[i:], "/*"):
			// Postgres block comments nest, so a naive search for the first
			// */ would leave the tail of an outer comment unmasked.
			depth, j := 1, i+2
			for j < len(sql) && depth > 0 {
				switch {
				case strings.HasPrefix(sql[j:], "/*"):
					depth, j = depth+1, j+2
				case strings.HasPrefix(sql[j:], "*/"):
					depth, j = depth-1, j+2
				default:
					j++
				}
			}
			blank(i, j)
			i = j
		case sql[i] == '\'':
			j := i + 1
			for j < len(sql) {
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' { // '' escapes a quote
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			blank(i, j)
			i = j
		case sql[i] == '$':
			// A dollar-quoted body ($$ … $$ or $tag$ … $tag$) is arbitrary
			// text — a function definition in there can contain the literal
			// words CREATE TABLE without declaring one.
			tag, ok := dollarTag(sql[i:])
			if !ok {
				i++
				continue
			}
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				blank(i, len(sql))
				i = len(sql)
				continue
			}
			stop := i + len(tag) + end + len(tag)
			blank(i, stop)
			i = stop
		default:
			i++
		}
	}
	return string(out)
}

// dollarTag reads the opening $tag$ (or $$) at the start of s, if there is
// one. A lone $ — a positional parameter, say — is not a quote opener.
func dollarTag(s string) (string, bool) {
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1], true
		}
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return "", false
		}
	}
	return "", false
}
