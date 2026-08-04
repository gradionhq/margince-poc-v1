// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every RBAC object must reach an EXISTING installation, not just a fresh one.
//
// The code-side seed (identity.seedSystemRoles) writes the role documents once
// at workspace creation and never re-syncs, so an object added to
// policy.coreObjects without a backfill migration is granted to nobody who
// bootstrapped earlier — it "works on a fresh database and 403s everywhere
// else", permanently. That is exactly how saved_view, webhook_subscription,
// relationship and partner reached production ungranted.
//
// This gate makes the omission impossible to repeat: the object vocabulary is
// DERIVED from policy.go, and every object must be accounted for either as
// part of the first-released cohort or by naming a migration that exists.
// Adding a 30th object therefore fails this test until its backfill is written.
//
// It deliberately does NOT read identity/internal/policy as a package (it is
// import-fenced) and does not restate the vocabulary — it parses the
// declaration, in the manner of enumsync_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const policyFile = "internal/modules/identity/internal/policy/policy.go"

// frozenLegacyObjects is the RBAC vocabulary of the INITIAL COMMIT — the only
// objects every installation necessarily received from its own bootstrap, so no
// backfill can ever have been needed. Derived from git, not from reasoning
// about the migration history: commit 2cb50021 ("Initial commit: WP0 foundation
// + WP1 core spine") declares
//
//	var coreObjects = []string{"person", "organization", "deal", "lead", "activity", "pipeline"}
//
// An earlier draft of this gate derived the cohort as "everything present
// before the backfill practice began at 0035" and so listed `list` and `tag`
// here too. That rule is self-refuting: by it, `relationship` and `partner`
// would also have been frozen — and those are two of the four objects this
// change exists to backfill. Any object added after the initial commit reached
// existing installations only if a migration put it there.
//
// This list is FROZEN. A new object never belongs here — it belongs in
// objectBackfills with a migration. Editing it is a security-owned review,
// because moving an object in here silently excuses it from ever reaching an
// existing installation, which is precisely the defect this gate exists to
// catch.
var frozenLegacyObjects = []string{
	"activity",
	"deal",
	"lead",
	"organization",
	"person",
	"pipeline",
}

// objectBackfills maps every object added after the initial commit to the
// migration that grants it to existing installations. TestEveryNamedBackfill
// below proves each named migration exists AND writes that object's JSON path;
// the parity test in backend/migrations executes the five newest end to end.
// gatekit:fixture the migration each object's grant is looked up in — the value
// is the identifier the assertion resolves, not the cost of an exception
var objectBackfills = map[string]string{
	"ai_model_rate":        "0117",
	"automation":           "0035",
	"capture_settings":     "0121",
	"channel_connection":   "0154",
	"computed_field":       "0066",
	"custom_field":         "0064",
	"embedding_reindex":    "0115",
	"fx_rate":              "0117",
	"list":                 "0183",
	"offer":                "0044",
	"offer_template":       "0072",
	"partner":              "0182",
	"product":              "0043",
	"project":              "0132",
	"quota":                "0068",
	"relationship":         "0181",
	"saved_view":           "0179",
	"signal":               "0047",
	"tag":                  "0183",
	"voice_profile":        "0042",
	"webhook_subscription": "0180",
}

// customNamespaceBackfills are objects whose backfill lives in the fork-owned
// custom/ namespace rather than core/ (ADR-0017). Same obligation, different
// directory.
// gatekit:fixture the custom-namespace migration each object's grant is looked
// up in — the value is the identifier the assertion resolves, not a cost
var customNamespaceBackfills = map[string]string{
	"import_run":         "20260730130000",
	"overlay_connection": "20260716130000",
}

func TestEveryRBACObjectReachesExistingInstallations(t *testing.T) {
	objects := coreObjectsFromSource(t)
	if len(objects) == 0 {
		t.Fatal("parsed no objects from policy.go; the declaration this gate derives from has moved")
	}

	accounted := map[string]string{}
	for _, object := range frozenLegacyObjects {
		accounted[object] = "first release"
	}
	for _, maps := range []map[string]string{objectBackfills, customNamespaceBackfills} {
		for object, version := range maps {
			if where, clash := accounted[object]; clash {
				t.Errorf("object %q is accounted for twice (%s and migration %s); the frozen cohort "+
					"and the backfill map must be disjoint", object, where, version)
				continue
			}
			accounted[object] = "migration " + version
		}
	}

	for _, object := range objects {
		if _, ok := accounted[object]; !ok {
			t.Errorf("RBAC object %q has no backfill migration and is not in the first-released cohort. "+
				"Every installation that bootstrapped before it was added holds no grant on it and will "+
				"403 forever. Write the backfill and add it to objectBackfills.", object)
		}
	}
	for object := range accounted {
		if !slices.Contains(objects, object) {
			t.Errorf("object %q is accounted for here but is no longer in policy.coreObjects; "+
				"drop the stale entry", object)
		}
	}
}

func TestEveryNamedBackfillMigrationExists(t *testing.T) {
	for _, spec := range []struct {
		dir     string
		entries map[string]string
	}{
		{filepath.Join("migrations", "core"), objectBackfills},
		{filepath.Join("migrations", "custom"), customNamespaceBackfills},
	} {
		files, err := os.ReadDir(spec.dir)
		if err != nil {
			t.Fatalf("reading %s: %v", spec.dir, err)
		}
		for object, version := range spec.entries {
			upSQL, found := readUpMigration(t, spec.dir, files, version)
			if !found {
				t.Errorf("object %q names backfill migration %s in %s, but no such up migration exists",
					object, version, spec.dir)
				continue
			}
			// Existence is not enough: a mapping may name a real migration that
			// grants something else entirely, which would leave the object
			// unbackfilled while this gate stayed green.
			if !strings.Contains(upSQL, "{objects,"+object+"}") {
				t.Errorf("object %q names migration %s, but that migration never writes the "+
					"'{objects,%s}' path — the mapping points at the wrong migration",
					object, version, object)
			}
		}
	}
}

// readUpMigration returns the body of the .up.sql for exactly this version.
//
// The match is anchored on the `<version>_` separator, not a bare prefix: these
// are numeric versions, so a bare prefix would let "0117" also match a
// hypothetical "01170_…" and silently attribute the wrong migration to an
// object. Matching the up specifically matters too — a down-only match would
// let a mapping name a migration that never grants anything.
func readUpMigration(t *testing.T, dir string, files []os.DirEntry, version string) (string, bool) {
	t.Helper()
	for _, f := range files {
		name := f.Name()
		if !strings.HasPrefix(name, version+"_") || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(body), true
	}
	return "", false
}

// coreObjectsFromSource extracts the string values of policy.go's coreObjects
// declaration. Derived, never restated — a renamed or moved declaration fails
// loudly above rather than silently shrinking this gate's coverage.
func coreObjectsFromSource(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), policyFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", policyFile, err)
	}
	var objects []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "coreObjects" {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, element := range lit.Elts {
					basic, ok := element.(*ast.BasicLit)
					if !ok || basic.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(basic.Value)
					if err != nil {
						t.Fatalf("unquoting %s: %v", basic.Value, err)
					}
					objects = append(objects, unquoted)
				}
			}
		}
	}
	return objects
}
