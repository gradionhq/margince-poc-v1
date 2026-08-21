// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// visibilityCheck matches a CHECK that constrains a `visibility` column,
// capturing the vocabulary it admits. Both DDL shapes the migrations use are
// covered: the inline column inside a CREATE TABLE (project, 0131) and the
// later ALTER TABLE (person and organization, 0095; signal, 0208; the
// narrowing in 1787320003).
var visibilityCheck = regexp.MustCompile(`(?is)CHECK\s*\(\s*visibility\s*(=|IN)\s*([^)]*)\)`)

// TestEveryTableThatCanHoldAnOwnerRowIsOwnerPrivate derives capture privacy
// from what the SCHEMA CAN HOLD, not from which tables happen to have a
// column and not from what today's writers happen to produce.
//
// The failure it exists to catch already happened once: project got a
// visibility column in 0131 whose CHECK admitted 'owner', and
// ownerPrivateTables was never told — so a perfectly valid 'owner' row would
// have read as workspace-visible to every seat. Nothing wrote one, which is
// exactly why nobody noticed.
//
// The invariant is a biconditional, and both directions are load-bearing:
//
//   - A table whose CHECK admits 'owner' MUST be in ownerPrivateTables, or the
//     read path ignores a state the database accepts — a silent disclosure
//     waiting for its first writer.
//   - A table in ownerPrivateTables MUST be able to hold 'owner', or the
//     predicate filters on a state that cannot occur — dead scope logic that
//     reads as a real guarantee.
//
// So widening project's CHECK back to admit 'owner' fails this test until
// ownerPrivateTables learns about it, and the reverse holds too. That is the
// whole point: after 1787320003 the two halves cannot drift apart in either
// direction.
func TestEveryTableThatCanHoldAnOwnerRowIsOwnerPrivate(t *testing.T) {
	canHoldOwner := tablesWhoseCheckAdmitsOwner(t)
	if len(canHoldOwner) == 0 {
		t.Fatal("no table admits an 'owner' visibility; the scan is broken, not the schema")
	}

	for _, table := range canHoldOwner {
		// Only the row-scoped record tables compose VisiblePredicate at all;
		// a table outside ownerScopedTables (signal) has no read predicate
		// here to add the arm to, and is governed by its own module.
		if !ownerScopedTables[table] {
			continue
		}
		if !ownerPrivateTables[table] {
			t.Errorf("%s admits visibility='owner' in the migrations but is not in "+
				"ownerPrivateTables, so an owner-private row on it reads as "+
				"workspace-visible to every seat", table)
		}
	}
	for table := range ownerPrivateTables {
		if !slicesContains(canHoldOwner, table) {
			t.Errorf("ownerPrivateTables names %s, but no migration lets it hold "+
				"visibility='owner' — the predicate carries an arm for a state the "+
				"database refuses", table)
		}
	}
}

// TestAProjectCannotHoldAnOwnerRow pins the decision 1787320003 made, so a
// later migration cannot widen the CHECK back without the read path noticing.
// The test above would also catch it; this one names the record and the reason
// so the failure explains itself.
func TestAProjectCannotHoldAnOwnerRow(t *testing.T) {
	if slicesContains(tablesWhoseCheckAdmitsOwner(t), tableProject) {
		t.Errorf("project admits visibility='owner' again. Capture privacy protects a record "+
			"a connector invented from somebody's mailbox; nothing auto-creates a project. "+
			"If a project should now be private before it is real, add %s to "+
			"ownerPrivateTables in the SAME change that widens the CHECK.", tableProject)
	}
}

// tablesWhoseCheckAdmitsOwner reads the core migrations in version order and
// answers every table whose LAST word on the matter lets visibility be
// 'owner'. Last-wins is what makes a later narrowing (or widening) count: the
// question is what the head schema accepts, not what some historical migration
// once accepted.
func tablesWhoseCheckAdmitsOwner(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(coreMigrationsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", coreMigrationsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	// Migration filenames are version-prefixed and zero-padded within each
	// scheme, so lexical order is apply order.
	sort.Strings(names)

	admits := map[string]bool{}
	for _, name := range names {
		path := filepath.Join(coreMigrationsDir, name)
		raw, err := os.ReadFile(path) // #nosec G304 -- a *.up.sql name from the trusted migrations tree, test-only
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for table, vocabulary := range visibilityChecksIn(string(raw)) {
			admits[table] = strings.Contains(vocabulary, "'owner'")
		}
	}
	out := make([]string, 0, len(admits))
	for table, ok := range admits {
		if ok {
			out = append(out, table)
		}
	}
	sort.Strings(out)
	return out
}

// createTableHeader opens a CREATE TABLE body; the table name is the capture.
var createTableHeader = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s*\(`)

// alterTableHeader opens an ALTER TABLE statement; the table name is the
// capture. A CHECK in the same statement belongs to this table.
var alterTableHeader = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?(\w+)\b`)

// visibilityChecksIn maps each table this migration constrains to the
// vocabulary its visibility CHECK admits. A statement's text is taken as
// everything up to the next CREATE/ALTER TABLE header, which attributes a
// CHECK to its own table without balancing parentheses.
func visibilityChecksIn(text string) map[string]string {
	type span struct {
		table      string
		start, end int
	}
	var spans []span
	for _, m := range createTableHeader.FindAllStringSubmatchIndex(text, -1) {
		spans = append(spans, span{text[m[2]:m[3]], m[1], len(text)})
	}
	for _, m := range alterTableHeader.FindAllStringSubmatchIndex(text, -1) {
		spans = append(spans, span{text[m[2]:m[3]], m[1], len(text)})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := range spans {
		if i+1 < len(spans) {
			spans[i].end = spans[i+1].start
		}
	}
	out := map[string]string{}
	for _, s := range spans {
		if m := visibilityCheck.FindStringSubmatch(text[s.start:s.end]); m != nil {
			out[s.table] = m[2]
		}
	}
	return out
}

func slicesContains(tables []string, want string) bool {
	for _, t := range tables {
		if t == want {
			return true
		}
	}
	return false
}
