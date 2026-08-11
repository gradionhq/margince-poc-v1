// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDerivedIdentifierCollisionAcrossUnits(t *testing.T) {
	// The collision is at the JOIN, not the name: unit "a-b" table "c" and
	// unit "a" table "b_c" both derive ext_a_b_c. Only gen-composition sees
	// every unit, so only it can catch this.
	units := []unitTables{
		{name: "a-b", tables: []string{"c"}},
		{name: "a", tables: []string{"b_c"}},
	}
	err := checkDerivedIdentifiers(units)
	if err == nil {
		t.Fatal("accepted two units deriving the same table identifier ext_a_b_c")
	}
	// An author reading this error has no other way to find the offending
	// pair: neither unit can see the other's tables.
	for _, want := range []string{"ext_a_b_c", `"a-b"`, `"c"`, `"a"`, `"b_c"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error does not name %s: %v", want, err)
		}
	}
}

func TestDerivedIdentifierExceedsBudget(t *testing.T) {
	// 32-char name + a 30-char suffix exceeds 63 bytes; Postgres truncates
	// silently, so generation must refuse with the offending position.
	name := strings.Repeat("a", 32)
	table := strings.Repeat("b", 30)
	err := checkDerivedIdentifiers([]unitTables{{name: name, tables: []string{table}}})
	if err == nil {
		t.Fatalf("accepted a %d-byte derived identifier over the 63-byte limit", len("ext_")+len(name)+len("_")+len(table))
	}
	// The unit and the table must each be named in their OWN right. Asserting
	// the bare strings would not prove that: both are substrings of the
	// derived identifier the message already quotes, so a message naming only
	// the identifier would pass. The "extensions/<name>:" prefix and the
	// separately quoted table are what an author actually navigates by.
	for _, want := range []string{
		"extensions/" + name + ":",
		fmt.Sprintf("%q", table),
		fmt.Sprintf("%q", "ext_"+name+"_"+table),
		"63",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("budget error does not carry %s: %v", want, err)
		}
	}
}

// TestDerivedIdentifierPinsBothSidesOfTheBudget pins the arithmetic itself,
// not just that some over-long name is refused. 63 must be accepted and 64
// must be refused: with only a far-over case and an at-limit case, a
// comparison loosened to `> 64` would leave both passing while a 64-byte
// identifier reached PostgreSQL and truncated silently — the exact failure
// this rule exists to prevent.
func TestDerivedIdentifierPinsBothSidesOfTheBudget(t *testing.T) {
	// ext_ + 32 + _ = 37, leaving exactly 26 for the suffix.
	name := strings.Repeat("a", 32)
	for _, tc := range []struct {
		suffix  int
		refused bool
	}{
		{suffix: 26, refused: false}, // 63 — the documented budget
		{suffix: 27, refused: true},  // 64 — one byte over
	} {
		table := strings.Repeat("b", tc.suffix)
		derived := len("ext_") + len(name) + len("_") + len(table)
		err := checkDerivedIdentifiers([]unitTables{{name: name, tables: []string{table}}})
		if tc.refused && err == nil {
			t.Errorf("accepted a %d-byte derived identifier — PostgreSQL would truncate it", derived)
		}
		if !tc.refused && err != nil {
			t.Errorf("refused a %d-byte derived identifier, which is exactly the budget: %v", derived, err)
		}
	}
}

func TestDerivedIdentifierAcceptsDistinctUnits(t *testing.T) {
	units := []unitTables{
		{name: "notes", tables: []string{"note", "note_tag"}},
		{name: "yogi", tables: []string{"pose"}},
	}
	if err := checkDerivedIdentifiers(units); err != nil {
		t.Fatalf("refused a non-colliding set: %v", err)
	}
}

// unitWithMigrations writes a minimal well-formed unit whose migrations/
// layer holds the given files, and scans it.
func unitWithMigrations(t *testing.T, unit string, migrations map[string]string) (extensionUnit, error) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module example.test/ext/x\n\ngo 1.26.5\n",
		"x.go":   "package x\n",
	}
	for rel, content := range migrations {
		files["migrations/"+rel] = content
	}
	root := t.TempDir()
	writeUnit(t, root, unit, files)
	return scanUnit(unit, filepath.Join(root, "extensions", unit))
}

// TestMigrationsLayerIsGovernedByItsOwnRule pins what replaced migrations/'s
// blanket "not built yet" refusal. The layer left unbuiltCapabilityLayers, so
// it also left that list's subpackage-walk exemption — deliberately: a Go
// package under migrations/ would run its init() as unchecked as one
// anywhere else in the unit, and the layer's own rule (SQL files only) says
// it has no business being there either way.
func TestMigrationsLayerIsGovernedByItsOwnRule(t *testing.T) {
	if slices.Contains(unbuiltCapabilityLayers, migrationsLayer) {
		t.Fatal("migrations/ is still refused on sight — the layer has a composition now")
	}

	t.Run("a well-formed layer yields the declared suffixes", func(t *testing.T) {
		unit, err := unitWithMigrations(t, "notes", map[string]string{
			"0001_note.up.sql":   "CREATE TABLE ext.ext_notes_note (id uuid PRIMARY KEY);\n",
			"0001_note.down.sql": "DROP TABLE ext.ext_notes_note;\n",
			"0002_tag.up.sql":    "CREATE TABLE IF NOT EXISTS ext_notes_note_tag (id uuid);\n",
			"0002_tag.down.sql":  "DROP TABLE ext.ext_notes_note_tag;\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"note", "note_tag"}; !slices.Equal(unit.Tables, want) {
			t.Fatalf("tables = %v, want %v", unit.Tables, want)
		}
	})

	t.Run("a unit without migrations declares no tables", func(t *testing.T) {
		unit, err := unitWithMigrations(t, "plain", nil)
		if err != nil || unit.Tables != nil {
			t.Fatalf("tables, err = %v, %v — want the empty set", unit.Tables, err)
		}
	})

	refusals := []struct {
		name       string
		migrations map[string]string
		wantErr    string
	}{
		{
			name:       "a Go package under the layer",
			migrations: map[string]string{"placeholder.go": "package placeholder\n"},
			wantErr:    "holds a Go package outside the unit root",
		},
		{
			name:       "a non-SQL file",
			migrations: map[string]string{"README.md": "notes\n"},
			wantErr:    "is not a .sql file",
		},
		{
			name:       "a nested directory",
			migrations: map[string]string{"v2/0001_note.up.sql": "CREATE TABLE ext.ext_x_note (id uuid);\n"},
			wantErr:    "is a directory",
		},
		{
			name:       "a table outside the unit namespace",
			migrations: map[string]string{"0001_x.up.sql": "CREATE TABLE ext.person (id uuid);\n"},
			wantErr:    "outside the unit's namespace",
		},
		{
			name:       "a table in another schema",
			migrations: map[string]string{"0001_x.up.sql": "CREATE TABLE public.ext_x_note (id uuid);\n"},
			wantErr:    `targets schema "public"`,
		},
		{
			name:       "an over-budget table",
			migrations: map[string]string{"0001_x.up.sql": "CREATE TABLE ext.ext_x_" + strings.Repeat("n", 60) + " (id uuid);\n"},
			wantErr:    "PostgreSQL truncates silently",
		},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unitWithMigrations(t, "x", tc.migrations)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	t.Run("a refusal carries the file and line", func(t *testing.T) {
		_, err := unitWithMigrations(t, "x", map[string]string{
			"0001_x.up.sql": "-- a comment\n\nCREATE TABLE ext.person (id uuid);\n",
		})
		if err == nil || !strings.Contains(err.Error(), "migrations/0001_x.up.sql:3") {
			t.Fatalf("err = %v, want the position of the offending statement", err)
		}
	})
}

func TestMigrationScanIgnoresCommentsAndLiterals(t *testing.T) {
	// A table name inside a comment, a string, or a dollar-quoted function
	// body is prose, not a declaration — mistaking one for a declaration
	// would refuse a legitimate migration for a table it never creates.
	sql := `-- CREATE TABLE person (id uuid);
/* nested /* CREATE TABLE public.other (id uuid); */ still a comment */
CREATE TABLE ext.ext_x_note (id uuid);
INSERT INTO ext.ext_x_note (id) VALUES ('CREATE TABLE public.injected (x int)');
CREATE FUNCTION ext.ext_x_f() RETURNS void AS $body$
  CREATE TABLE public.inside_body (id uuid);
$body$ LANGUAGE sql;
`
	unit, err := unitWithMigrations(t, "x", map[string]string{"0001_x.up.sql": sql})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"note"}; !slices.Equal(unit.Tables, want) {
		t.Fatalf("tables = %v, want %v", unit.Tables, want)
	}
}

func TestScanExtensionsCatchesACrossUnitCollision(t *testing.T) {
	// The end-to-end shape of the rule the whole check exists for: each unit
	// is individually well-formed, and only the composed set is wrong.
	root := t.TempDir()
	writeUnit(t, root, "a-b", map[string]string{
		"go.mod":                     "module example.test/ext/ab\n\ngo 1.26.5\n",
		"ab.go":                      "package ab\n",
		"migrations/0001_x.up.sql":   "CREATE TABLE ext.ext_a_b_c (id uuid);\n",
		"migrations/0001_x.down.sql": "DROP TABLE ext.ext_a_b_c;\n",
	})
	writeUnit(t, root, "a", map[string]string{
		"go.mod":                     "module example.test/ext/a\n\ngo 1.26.5\n",
		"a.go":                       "package a\n",
		"migrations/0001_x.up.sql":   "CREATE TABLE ext.ext_a_b_c (id uuid);\n",
		"migrations/0001_x.down.sql": "DROP TABLE ext.ext_a_b_c;\n",
	})
	_, err := scanExtensions(root)
	if err == nil || !strings.Contains(err.Error(), "ext_a_b_c") {
		t.Fatalf("err = %v, want the cross-unit collision refusal", err)
	}
}

func TestDerivedIdentifierRefusesAnInvalidUnitName(t *testing.T) {
	// checkDerivedIdentifiers derives a SQL identifier from the name, so it
	// re-validates rather than trusting its caller to have scanned first.
	if err := checkDerivedIdentifiers([]unitTables{{name: "Bad-Name", tables: []string{"t"}}}); err == nil {
		t.Fatal("accepted a unit name the published grammar refuses")
	}
}

func TestMaskNonCodePreservesOffsetsAndClosesUnterminatedSpans(t *testing.T) {
	// Offsets must survive masking or a refusal would quote the wrong line;
	// an unterminated span must mask to the end of the file rather than
	// leaving its tail readable as code, which is the fail-closed reading.
	cases := []struct{ name, in, want string }{
		{name: "line comment", in: "a -- x\nb", want: "a     \nb"},
		{name: "block comment", in: "a/* x\ny */b", want: "a    \n    b"},
		{name: "string literal", in: "a 'x''y' b", want: "a        b"},
		{name: "dollar quote", in: "a $t$x\ny$t$ b", want: "a     \n     b"},
		{name: "bare dollar quote", in: "a $$x$$ b", want: "a       b"},
		{name: "a lone dollar is a parameter, not a quote", in: "v = $1 AND w = $2", want: "v = $1 AND w = $2"},
		{name: "unterminated block comment", in: "a /* x\ny", want: "a     \n "},
		{name: "unterminated string", in: "a 'x\ny", want: "a   \n "},
		{name: "unterminated dollar quote", in: "a $t$x\ny", want: "a     \n "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskNonCode(tc.in)
			if got != tc.want {
				t.Fatalf("maskNonCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) != len(tc.in) {
				t.Fatalf("masking changed the length %d -> %d, so positions would drift", len(tc.in), len(got))
			}
		})
	}
}

// digestRoot lays out the smallest tree coreDigest can read: the workspace and
// its one member, the two committed stubs stubMatchesVanilla holds, every base
// contract, and a backend/pkg subtree for addTree to walk.
func digestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		goWorkFile:                         "go 1.26.5\n\nuse (\n\t./backend\n)\n",
		"backend/go.mod":                   "module example.test/backend\n\ngo 1.26.5\n",
		"backend/pkg/extension/surface.go": "package extension\n",
		"composition/go.mod":               "module example.test/composition\n\ngo 1.26.5\n",
		"composition/extensions_gen.go":    "package composition\n",
		frontendVanillaStub:                "export const extensions = [];\n",
	}
	for _, base := range composedContractBases {
		files["backend/"+apiLayer+"/"+base] = "# " + base + "\n"
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestCoreDigestCoversEveryComposedContractBase is the fast staleness probe's
// own falsification.
//
// Each base is read by composedContracts and emitted as an output, so a base the
// digest does not cover is an input whose change -verify-inputs reports as
// clean. Nothing goes red in that state — the full -verify still fails, but only
// once somebody runs it — which is why this is asserted over the WHOLE list
// rather than over the one contract that was covered first.
func TestCoreDigestCoversEveryComposedContractBase(t *testing.T) {
	root := digestRoot(t)
	before, err := coreDigest(root)
	if err != nil {
		t.Fatalf("coreDigest: %v", err)
	}
	for _, base := range composedContractBases {
		t.Run(base, func(t *testing.T) {
			path := filepath.Join(root, "backend", apiLayer, base)
			original, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0o644); err != nil {
					t.Fatal(err)
				}
			})
			if err := os.WriteFile(path, append(original, "# edited\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
			after, err := coreDigest(root)
			if err != nil {
				t.Fatalf("coreDigest: %v", err)
			}
			if after == before {
				t.Errorf("editing backend/%s/%s left the core digest at %s — this contract composes into build/composition/api/%s, so -verify-inputs would call a stale composition current",
					apiLayer, base, before, base)
			}
		})
	}
}

// TestCoreDigestCoversTheCommittedStubsAndTheWorkspace keeps the arms above from
// being read as the whole list: the same silent-staleness argument applies to
// every other input, and a refactor that dropped one would leave the contract
// arms green.
func TestCoreDigestCoversTheCommittedStubsAndTheWorkspace(t *testing.T) {
	for _, rel := range []string{
		goWorkFile,
		"backend/go.mod",
		"backend/pkg/extension/surface.go",
		"composition/go.mod",
		"composition/extensions_gen.go",
		frontendVanillaStub,
	} {
		t.Run(rel, func(t *testing.T) {
			root := digestRoot(t)
			before, err := coreDigest(root)
			if err != nil {
				t.Fatalf("coreDigest: %v", err)
			}
			path := filepath.Join(root, filepath.FromSlash(rel))
			original, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(original, "\n// edited\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
			after, err := coreDigest(root)
			if err != nil {
				t.Fatalf("coreDigest: %v", err)
			}
			if after == before {
				t.Errorf("editing %s left the core digest unchanged — a composed output derives from it, so -verify-inputs would call a stale composition current", rel)
			}
		})
	}
}
