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
