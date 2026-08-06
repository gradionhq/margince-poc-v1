// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The settings-catalog fitness gates (ADR-0090/A135 §7). Moving a setting's
// validation out of Postgres and into Go is only sound if the obligations the
// CHECK constraints used to carry are DERIVED from the system rather than
// maintained as a list somebody remembers to update. These three tests are
// that derivation:
//
//  1. every registered key is unique and well-formed, and carries the
//     governance a setting needs to be writable at all;
//  2. a key's module prefix names the module that actually declares it —
//     the table-ownership obligation, in a second place;
//  3. no key exists in both the settings registry and margince.yaml's runtime
//     surface, which is ADR-0061 §2's "no key exists in both surfaces"
//     promise, enforced for the first time.
//
// The registry these read is compose's, reached through the exported test
// seam, so a setting that is declared but never registered fails here rather
// than being silently unreachable.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// keyShape mirrors the CHECK on setting.key. Both exist on purpose: the
// constraint proves a key HAS a module prefix, this test proves the prefix
// names a real module.
var keyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

func TestEverySettingIsUniqueWellFormedAndGoverned(t *testing.T) {
	defs := compose.SettingsCatalogForTest()
	if len(defs) == 0 {
		t.Fatal("the settings catalog is empty; this gate would pass vacuously")
	}

	seen := map[string]int{}
	for _, d := range defs {
		seen[d.Key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("%s is declared %d times: two modules cannot own one setting — "+
				"the registry would silently keep the last", key, n)
		}
	}

	for _, d := range defs {
		if !keyShape.MatchString(d.Key) {
			t.Errorf("%q is not a <module>.<name> key", d.Key)
		}
		// A setting with no RBAC object is ungated: with no RLS on the
		// `setting` table (0189), the object gate at the writer is the ONLY
		// control, so an empty one is an open door rather than an oversight.
		if d.Object == "" {
			t.Errorf("%s declares no RBAC object; the settings table has no RLS beneath it", d.Key)
		}
		// A settings change must stay as legible in the ledger as the
		// per-column writes it replaced.
		if d.AuditVerb == "" {
			t.Errorf("%s declares no audit verb; its changes would be unattributable", d.Key)
		}
		// A default that cannot encode means a read of an unset setting fails
		// — the one path every installation takes before anyone changes it.
		if d.DefaultErr != nil {
			t.Errorf("%s: default does not encode: %v", d.Key, d.DefaultErr)
		}
	}
}

func TestEverySettingKeyIsPrefixedByItsOwningModule(t *testing.T) {
	modules, err := os.ReadDir(filepath.Join("internal", "modules"))
	if err != nil {
		t.Fatalf("listing modules: %v", err)
	}
	known := map[string]bool{}
	for _, m := range modules {
		if m.IsDir() {
			known[m.Name()] = true
		}
	}
	// `installation` is the one prefix with no module directory of its own:
	// it names the installation itself, and identity owns the entries. It is
	// listed rather than inferred so adding a second such prefix is a visible
	// decision, not a silent one.
	known["installation"] = true

	for _, d := range compose.SettingsCatalogForTest() {
		prefix, _, ok := strings.Cut(d.Key, ".")
		if !ok {
			continue // the shape gate above already reported this
		}
		if !known[prefix] {
			t.Errorf("%s is prefixed %q, which is not a module under internal/modules "+
				"(nor the `installation` prefix); a setting's prefix names its owner", d.Key, prefix)
		}
	}
}

// bootstrapSections are the deployment-config sections ADR-0061 §2 consumes
// exactly once at bootstrap. A setting seeded from one of these is the
// sanctioned pattern (ADR-0090 §8) — the row is authoritative afterwards —
// so they are excluded from the collision check. Every OTHER section is a
// live runtime surface, and a key in both places would have two authorities
// with nothing to say which one the operator last changed.
var bootstrapSections = map[string]bool{
	"organization":    true,
	"bootstrap_admin": true,
	"seeds":           true,
}

func TestNoSettingKeyIsAlsoADeploymentConfigKey(t *testing.T) {
	// Derived from the config STRUCT, not from the example file: the template
	// is illustrative and omits whole sections (there is no `capture:` block
	// in it today), so comparing against it would pass vacuously for exactly
	// the settings most likely to collide.
	configPaths := compose.DeploymentConfigKeysForTest()
	if len(configPaths) == 0 {
		t.Fatal("no yaml paths derived from the deployment config; this gate would pass vacuously")
	}

	for _, d := range compose.SettingsCatalogForTest() {
		section, _, _ := strings.Cut(d.Key, ".")
		if bootstrapSections[section] {
			continue
		}
		if configPaths[d.Key] {
			t.Errorf("%s is settable BOTH as a setting row and at runtime in margince.yaml: "+
				"ADR-0061 §2 forbids a key existing in both surfaces — the effective "+
				"value would depend on load order", d.Key)
		}
	}
}
