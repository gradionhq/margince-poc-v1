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

func TestExtensionNamespacesRefusesADeclaredButEmptyLayer(t *testing.T) {
	_, err := extensionNamespaces([]extension.Extension{{
		Name: "hollow", Version: "1.0.0", Migrations: unitFS(nil),
	}})
	if err == nil {
		t.Fatal("an embedded migrations layer holding no pair was accepted — it reads as a schema that applied")
	}
	if !strings.Contains(err.Error(), "hollow") {
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
