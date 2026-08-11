// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dbmigrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoad_pairsAndOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"core/0002_second.up.sql":   {Data: []byte("CREATE TABLE b ();")},
		"core/0002_second.down.sql": {Data: []byte("DROP TABLE b;")},
		"core/0001_first.up.sql":    {Data: []byte("CREATE TABLE a ();")},
		"core/0001_first.down.sql":  {Data: []byte("DROP TABLE a;")},
		"core/README.md":            {Data: []byte("ignored")},
	}

	ms, err := Load(fsys, "core")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d migrations, want 2", len(ms))
	}
	if ms[0].Version != "0001" || ms[1].Version != "0002" {
		t.Errorf("order = %s, %s; want 0001, 0002", ms[0].Version, ms[1].Version)
	}
	if ms[0].Name != "first" || ms[0].UpSQL == "" || ms[0].DownSQL == "" {
		t.Errorf("0001 loaded incompletely: %+v", ms[0])
	}
}

func TestLoad_rejectsIrreversibleMigration(t *testing.T) {
	fsys := fstest.MapFS{
		"core/0001_first.up.sql": {Data: []byte("CREATE TABLE a ();")},
	}
	_, err := Load(fsys, "core")
	if err == nil || !strings.Contains(err.Error(), "both .up.sql and .down.sql") {
		t.Fatalf("err = %v, want missing-down error", err)
	}
}

func TestLoad_rejectsUnversionedName(t *testing.T) {
	fsys := fstest.MapFS{
		"core/nodash.up.sql":   {Data: []byte("SELECT 1;")},
		"core/nodash.down.sql": {Data: []byte("SELECT 1;")},
	}
	_, err := Load(fsys, "core")
	if err == nil {
		t.Fatal("Load accepted a migration without <version>_<name>")
	}
}

func TestNamespaceFor(t *testing.T) {
	cases := []struct {
		unit    string
		want    string
		wantErr string
	}{
		{unit: "foo-1", want: "ext_foo_1"},
		{unit: "notes", want: "ext_notes"},
		{unit: "yogi", want: "ext_yogi"},
		// The refusals all come from the ONE published name rule; this
		// function adds none of its own, so these pin that it validates
		// rather than that it re-implements.
		{unit: "Bad-Name", wantErr: "not a valid unit name"},
		{unit: "trailing-", wantErr: "not a valid unit name"},
		{unit: "", wantErr: "not a valid unit name"},
		{unit: strings.Repeat("a", 33), wantErr: "capped at 32"},
	}
	for _, tc := range cases {
		got, err := NamespaceFor(tc.unit)
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("NamespaceFor(%q) err = %v, want %q", tc.unit, err, tc.wantErr)
			}
		case err != nil:
			t.Errorf("NamespaceFor(%q) = %v", tc.unit, err)
		case got != tc.want:
			t.Errorf("NamespaceFor(%q) = %q, want %q", tc.unit, got, tc.want)
		}
	}
}

// TestNamespaceForFitsTheTrackingTableGrammar closes the loop that made this
// mapping worth writing here rather than at the call site: a derived
// namespace must be spellable as schema_migrations_<ns>, digits and all.
func TestNamespaceForFitsTheTrackingTableGrammar(t *testing.T) {
	ns, err := NamespaceFor("foo-1")
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range ns {
		digit := r >= '0' && r <= '9'
		if (r < 'a' || r > 'z') && r != '_' && !digit {
			t.Fatalf("namespace %q holds %q, which trackingTable refuses", ns, r)
		}
		if digit && i == 0 {
			t.Fatalf("namespace %q starts with a digit", ns)
		}
	}
	// The longest namespace the name grammar can produce is ext_ plus a
	// 32-character name; its tracking table must still fit the same budget.
	longest, err := NamespaceFor(strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if n := len("schema_migrations_") + len(longest); n > 63 {
		t.Fatalf("the longest tracking table is %d bytes, over PostgreSQL's 63", n)
	}
}

func TestTrackingTableRefusesUnspellableNamespaces(t *testing.T) {
	// The namespace is interpolated into DDL and cannot be a parameter, so
	// the grammar check runs before the connection is ever touched — which
	// is why a nil conn is safe here and is itself the assertion that no
	// refused namespace reaches the database.
	for _, tc := range []struct{ ns, wantErr string }{
		{ns: "Core", wantErr: "want lower-case letters"},
		{ns: "ext-foo", wantErr: "want lower-case letters"},
		{ns: "ext foo", wantErr: "want lower-case letters"},
		{ns: "1ext", wantErr: "cannot start with a digit"},
		{ns: "", wantErr: "empty namespace"},
	} {
		if _, err := trackingTable(t.Context(), nil, tc.ns); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("trackingTable(%q) err = %v, want %q", tc.ns, err, tc.wantErr)
		}
	}
}
