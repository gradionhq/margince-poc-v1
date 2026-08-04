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
	"testing"
)

const policyFile = "internal/modules/identity/internal/policy/policy.go"

// frozenLegacyObjects is the RBAC vocabulary as it stood in the first shipped
// defaults — the cohort every installation received from its own bootstrap, so
// no backfill was ever needed. Derived from the objects present in
// policy.coreObjects before the backfill practice began at migration 0035
// (the first *_rbac backfill), cross-checked against the migration history:
// no migration under 0035 writes role.permissions at all.
//
// This list is FROZEN. A new object never belongs here — it belongs in
// objectBackfills with a migration. Editing it is a security-owned review,
// because moving an object in here silently excuses it from ever reaching an
// existing installation.
var frozenLegacyObjects = []string{
	"activity",
	"deal",
	"lead",
	"list",
	"organization",
	"person",
	"pipeline",
	"tag",
}

// objectBackfills maps every object added after the first release to the
// migration that grants it to existing installations. The parity test in
// backend/migrations proves each one writes the right grants to the right
// roles; this map proves none is missing.
var objectBackfills = map[string]string{
	"ai_model_rate":        "0117",
	"automation":           "0035",
	"capture_settings":     "0121",
	"channel_connection":   "0154",
	"computed_field":       "0066",
	"custom_field":         "0064",
	"embedding_reindex":    "0115",
	"fx_rate":              "0117",
	"offer":                "0044",
	"offer_template":       "0072",
	"partner":              "0182",
	"product":              "0043",
	"project":              "0132",
	"quota":                "0068",
	"relationship":         "0181",
	"saved_view":           "0179",
	"signal":               "0047",
	"voice_profile":        "0042",
	"webhook_subscription": "0180",
}

// customNamespaceBackfills are objects whose backfill lives in the fork-owned
// custom/ namespace rather than core/ (ADR-0017). Same obligation, different
// directory.
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
			found := false
			for _, f := range files {
				name := f.Name()
				if len(name) >= len(version) && name[:len(version)] == version &&
					filepath.Ext(name) == ".sql" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("object %q names backfill migration %s in %s, but no such migration exists",
					object, version, spec.dir)
			}
		}
	}
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
