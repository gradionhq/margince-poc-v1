// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The extension lane's pure half: turning the composed set into migration
// namespaces, and saying out loud which ones were found. Both are the
// difference between a migrate that applies an installation's extension
// schema and one that silently applies none, so neither is left to the
// integration lane alone.

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// unitFS builds the filesystem shape a unit's `//go:embed migrations`
// produces: the layer directory sitting at the root of the FS.
func unitFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[extension.MigrationsDir+"/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestExtensionNamespacesSkipsAUnitThatOwnsNoTables(t *testing.T) {
	got, err := extensionNamespaces([]extension.Extension{{Name: "yogi", Version: "1.0.0"}})
	if err != nil {
		t.Fatalf("extensionNamespaces: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a unit with no Migrations produced %d namespace(s), want none — declaring no schema is the common case, not an error", len(got))
	}
}

func TestExtensionNamespacesMapsAUnitOntoItsExtNamespace(t *testing.T) {
	got, err := extensionNamespaces([]extension.Extension{{
		Name:    "foo-1",
		Version: "1.0.0",
		Migrations: unitFS(map[string]string{
			"0001_note.up.sql":   "CREATE TABLE ext.ext_foo_1_note (id int)",
			"0001_note.down.sql": "DROP TABLE ext.ext_foo_1_note",
		}),
	}})
	if err != nil {
		t.Fatalf("extensionNamespaces: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d namespaces, want 1", len(got))
	}
	// The hyphen→underscore mapping is dbmigrate.NamespaceFor's, and this
	// pins that migrate goes through it rather than deriving its own: the
	// tracking table and the ext_<name> role must name one namespace.
	if got[0].Name != "ext_foo_1" {
		t.Errorf("namespace = %q, want %q", got[0].Name, "ext_foo_1")
	}
	if len(got[0].Migrations) != 1 || got[0].Migrations[0].Version != "0001" {
		t.Errorf("migrations = %+v, want the single 0001 pair", got[0].Migrations)
	}
}

func TestExtensionNamespacesOrdersByUnitNameNotCompositionOrder(t *testing.T) {
	layer := unitFS(map[string]string{
		"0001_t.up.sql":   "SELECT 1",
		"0001_t.down.sql": "SELECT 1",
	})
	got, err := extensionNamespaces([]extension.Extension{
		{Name: "zulu", Version: "1.0.0", Migrations: layer},
		{Name: "alpha", Version: "1.0.0", Migrations: layer},
	})
	if err != nil {
		t.Fatalf("extensionNamespaces: %v", err)
	}
	var names []string
	for _, ns := range got {
		names = append(names, ns.Name)
	}
	want := []string{"ext_alpha", "ext_zulu"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v — two runs of one composition must produce the same migration log", names, want)
	}
}

// TestExtensionNamespacesRefusesADeclaredButEmptyLayer covers the guard in
// extensionNamespaces, which needs a layer that EXISTS and holds no pair —
// a README-only directory is the real shape of that. An FS with no
// migrations/ directory at all does not reach the guard: dbmigrate.Load
// fails first on fs.ErrNotExist, which is the case below this one.
func TestExtensionNamespacesRefusesADeclaredButEmptyLayer(t *testing.T) {
	_, err := extensionNamespaces([]extension.Extension{{
		Name: "hollow", Version: "1.0.0",
		Migrations: unitFS(map[string]string{"README.md": "how this unit's schema works"}),
	}})
	if err == nil {
		t.Fatal("an embedded migrations layer holding no pair was accepted — it reads as a schema that applied")
	}
	if !strings.Contains(err.Error(), "hollow") {
		t.Errorf("error %q does not name the offending unit", err)
	}
	// Pinned on the guard's own words, so a future refactor that lets
	// dbmigrate.Load answer this case instead cannot pass silently: the two
	// messages tell an author different things to do.
	if !strings.Contains(err.Error(), "leave Migrations nil") {
		t.Errorf("error %q is not the declared-but-empty guard — it must say what to do instead", err)
	}
}

// TestExtensionNamespacesRefusesAMissingLayer is the botched-embed case: a
// unit sets Migrations to an FS that carries no migrations/ directory. It
// must fail loudly rather than read as a unit that owns no tables — leaving
// the field nil is how a unit says that.
func TestExtensionNamespacesRefusesAMissingLayer(t *testing.T) {
	_, err := extensionNamespaces([]extension.Extension{{
		Name: "misembedded", Version: "1.0.0", Migrations: fstest.MapFS{},
	}})
	if err == nil {
		t.Fatal("a Migrations FS with no migrations/ directory was accepted")
	}
	if !strings.Contains(err.Error(), "misembedded") {
		t.Errorf("error %q does not name the offending unit", err)
	}
}

func TestExtensionNamespacesRefusesAnUnmappableUnitName(t *testing.T) {
	_, err := extensionNamespaces([]extension.Extension{{
		Name: "Bad Name", Version: "1.0.0", Migrations: unitFS(map[string]string{
			"0001_t.up.sql":   "SELECT 1",
			"0001_t.down.sql": "SELECT 1",
		}),
	}})
	if err == nil {
		t.Fatal("a unit name that cannot be a SQL identifier was accepted — the namespace is interpolated into DDL")
	}
	if !strings.Contains(err.Error(), "Bad Name") {
		t.Errorf("error %q does not name the offending unit", err)
	}
}

func TestExtensionNamespacesRefusesAMigrationThatCannotBeReverted(t *testing.T) {
	_, err := extensionNamespaces([]extension.Extension{{
		Name: "oneway", Version: "1.0.0", Migrations: unitFS(map[string]string{
			"0001_t.up.sql": "SELECT 1",
		}),
	}})
	if err == nil {
		t.Fatal("a migration with no .down.sql was accepted")
	}
	if !strings.Contains(err.Error(), "oneway") {
		t.Errorf("error %q does not name the offending unit", err)
	}
}

func TestReportExtensionNamespacesSaysSoWhenThereAreNone(t *testing.T) {
	var out strings.Builder
	if err := reportExtensionNamespaces(nil, &out); err != nil {
		t.Fatalf("reportExtensionNamespaces: %v", err)
	}
	// Silence here is the failure this wiring exists to prevent: a migrate
	// resolving the vanilla stub applies zero extension migrations and would
	// otherwise look exactly like a correct run.
	if !strings.Contains(out.String(), "none in the composed set") {
		t.Errorf("empty set printed %q, want an explicit line saying none were composed", out.String())
	}
}

func TestReportExtensionNamespacesNamesEachLaneAndItsSize(t *testing.T) {
	var out strings.Builder
	err := reportExtensionNamespaces([]dbmigrate.Namespace{
		{Name: "ext_alpha", Migrations: []dbmigrate.Migration{{Version: "0001"}}},
		{Name: "ext_zulu", Migrations: []dbmigrate.Migration{{Version: "0001"}, {Version: "0002"}}},
	}, &out)
	if err != nil {
		t.Fatalf("reportExtensionNamespaces: %v", err)
	}
	got := out.String()
	for _, want := range []string{"ext_alpha (1 declared)", "ext_zulu (2 declared)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q is missing %q", got, want)
		}
	}
}

// failWriter is a stdout that cannot be written to — a closed pipe, say.
type failWriter struct{}

var errWriteFailed = errors.New("write failed")

func (failWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

func TestReportExtensionNamespacesPropagatesAWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		exts []dbmigrate.Namespace
	}{
		{"empty set", nil},
		{"populated set", []dbmigrate.Namespace{{Name: "ext_alpha"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A migrate whose log went nowhere must not report success: the
			// line IS the operator's evidence the lane ran.
			if err := reportExtensionNamespaces(tc.exts, failWriter{}); !errors.Is(err, errWriteFailed) {
				t.Errorf("err = %v, want it to wrap the write failure", err)
			}
		})
	}
}

// shellMatcherPattern finds migrate_template's comparison in
// scripts/lib-testdb.sh and captures the literal prefix it tests the summary
// against.
var shellMatcherPattern = regexp.MustCompile(`\[\[ "\$summary" != "([^"]*)"\* \]\]`)

// TestUpSummaryMatchesTheShellMatcher closes a silent-drift risk between two
// files that cannot see each other: cmd/migrate prints the summary and
// scripts/lib-testdb.sh string-matches it.
//
// A mismatch is not loud. migrate_template would print "was behind" on every
// run — a staleness check that cries wolf permanently is worse than none, and
// build_template discards its output, so the warning would not even be seen
// where it is most likely to be produced. Both sides are read here rather
// than restated, so this test cannot itself go stale.
func TestUpSummaryMatchesTheShellMatcher(t *testing.T) {
	const script = "../../../scripts/lib-testdb.sh"
	source, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("reading %s: %v", script, err)
	}
	found := shellMatcherPattern.FindSubmatch(source)
	if found == nil {
		t.Fatalf("%s no longer compares $summary against a literal prefix — migrate_template's staleness check was rewritten; re-point this test at whatever replaced it", script)
	}
	prefix := string(found[1])
	// The zero-applied form is the one the shell classifies on: it means
	// "nothing was missing", which is migrate_template's silent path.
	summary := fmt.Sprintf(upSummaryFormat, 0, 0)
	if !strings.HasPrefix(summary, prefix) {
		t.Errorf("migrate prints %q but %s matches on prefix %q — migrate_template would report every template as behind; change both together", summary, script, prefix)
	}
}
