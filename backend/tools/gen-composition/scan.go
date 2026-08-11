// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extensionUnit is one enabled extension: a directory under extensions/.
type extensionUnit struct {
	Name       string
	Dir        string
	ModulePath string
	// Tables are the bare table names the unit's migrations declare, each
	// already stripped of its ext_<namespace>_ prefix — the suffixes whose
	// join with the namespace checkDerivedIdentifiers validates.
	Tables []string
	// HasMigrations reports that the unit ships a migrations/ DIRECTORY —
	// nothing more. It is not proof of an .up.sql, and not `len(Tables) > 0`: a
	// migration that alters an existing table declares no new one, and a layer
	// holding only a .down.sql is an incomplete pair that must still be seen
	// (see collectUnitTables on why presence is keyed on the directory). What
	// the flag exists to catch is the unit whose schema is on disk and applied
	// by nothing, so it must stay true for every broken shape of the layer.
	HasMigrations bool
	// Fragments are the unit's contract overlays, keyed by the core contract
	// each targets (composedContractBases). Nil for a Go-only unit.
	Fragments map[string]contractFragment
	// Frontend is the unit's screen package, or nil when it ships none — the
	// common case, and not an error. The composed screen registry imports it
	// by package name.
	Frontend *unitFrontend
}

// scanExtensions reads the enabled set. A capability layer this composer
// cannot compose yet is a hard error, not a silent drop — an extension
// shipping one must not build until its composition slice exists. There are
// none left today (unbuiltCapabilityLayers is empty); the rule outlives the
// list.
func scanExtensions(root string) ([]extensionUnit, error) {
	entries, err := os.ReadDir(filepath.Join(root, "extensions"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var units []extensionUnit
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			// IsDir() is false for a symlink, so without this check a
			// symlinked unit would silently drop out of the composed
			// binary while sitting visibly under extensions/.
			return nil, fmt.Errorf("extensions/%s: a symlinked entry is not composable — an enabled unit is a plain directory tree", entry.Name())
		}
		if !entry.IsDir() {
			continue // approvals.lock, .gitkeep
		}
		name := entry.Name()
		// The ONE unit-name rule, published on the seam: scan-time
		// acceptance must never drift from boot-time validation.
		if err := extension.Name(name).Validate(); err != nil {
			return nil, fmt.Errorf("extensions/%s: %w", name, err)
		}
		dir := filepath.Join(root, "extensions", name)
		unit, err := scanUnit(name, dir)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	// The cross-unit check runs HERE, over the sorted whole set, because
	// this is the only place in the build that sees every unit at once: a
	// derived-identifier collision belongs to no single unit's tree, and no
	// unit can see the other's tables to find it.
	tables := make([]unitTables, 0, len(units))
	for _, u := range units {
		tables = append(tables, unitTables{name: u.Name, tables: u.Tables})
	}
	if err := checkDerivedIdentifiers(tables); err != nil {
		return nil, err
	}
	return units, nil
}

// unbuiltCapabilityLayers are the top-level subdirectory names a unit may
// hold that this composer does not compose yet. scanUnit refuses their mere
// presence outright (below).
//
// It is EMPTY, and that is the tier's current state rather than a mistake: all
// three layers now have a composition. It stays as the seam a fourth capability
// would arrive through — refused on sight until its own rule exists — because
// the alternative to an empty list is deleting the mechanism and rebuilding it
// under pressure the day someone proposes one.
//
// refuseNonRootGoPackages exempts the same names from its walk, and that is
// not a second, independent policy that happens to agree: the exemption
// exists ONLY so an already-refused layer reports its own refusal instead of
// a confusing "holds a Go package outside the unit root". A name on this list
// never reaches the walk at all. The two uses are therefore one role — "not
// composed yet, refused on sight" — and stay a single list.
//
// A layer that HAS a composition is governed by that composition's own rule
// and leaves this list entirely. migrations/ was the first: collectUnitTables
// says what its subtree may hold, and it deliberately does NOT re-grant the
// walk exemption, so a Go package under migrations/ is refused exactly like
// one anywhere else in the unit — an init() there would run just as
// unchecked. api/ is the second, on identical terms: collectUnitFragments
// (contracts.go) says what it may hold, and it stays subject to the walk.
// frontend/ was the third and last: collectUnitFrontend (extfrontend.go) says
// what its package must be. Lifting a future layer means the same two edits:
// drop the string here, add the layer's own rule.
var unbuiltCapabilityLayers = []string{}

func scanUnit(name, dir string) (extensionUnit, error) {
	for _, sub := range unbuiltCapabilityLayers {
		if _, err := os.Stat(filepath.Join(dir, sub)); err == nil {
			return extensionUnit{}, fmt.Errorf("extensions/%s: %s/ composition is not built yet — the walking skeleton composes Go registrations only", name, sub)
		}
	}
	if err := refuseNonRootGoPackages(name, dir); err != nil {
		return extensionUnit{}, err
	}
	hasGo, err := hasRootGoFiles(dir)
	if err != nil {
		return extensionUnit{}, err
	}
	modBytes, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	switch {
	case os.IsNotExist(err):
		if hasGo {
			return extensionUnit{}, fmt.Errorf("extensions/%s: *.go present but no go.mod — a Go-bearing extension is its own module", name)
		}
		// The removal case, named rather than left to the reader. `git rm -r`
		// takes a unit's tracked files and leaves its INSTALLED ones, because
		// node_modules is ignored — so the directory survives holding nothing
		// but a dependency tree, and "no Go module" sends whoever removed the
		// unit looking for a go.mod they deliberately deleted.
		if empty, err := holdsOnlyInstalledOutput(dir); err == nil && empty {
			return extensionUnit{}, fmt.Errorf("extensions/%s holds nothing but installed dependencies — its source is gone but the directory is not, and presence under extensions/ IS enablement; remove the directory (`rm -rf extensions/%s`) and re-run `pnpm install` to prune the workspace member", name, name)
		}
		return extensionUnit{}, fmt.Errorf("extensions/%s: nothing to compose (no Go module)", name)
	case err != nil:
		return extensionUnit{}, err
	}
	if !hasGo {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod present but no root package — the unit's root package must export New()", name)
	}
	mod, err := modfile.Parse(filepath.Join(dir, "go.mod"), modBytes, nil)
	if err != nil {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod: %w", name, err)
	}
	if mod.Module == nil || mod.Module.Mod.Path == "" {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod declares no module path", name)
	}
	tables, hasMigrations, err := collectUnitTables(name, dir)
	if err != nil {
		return extensionUnit{}, err
	}
	fragments, err := collectUnitFragments(name, dir)
	if err != nil {
		return extensionUnit{}, err
	}
	frontend, err := collectUnitFrontend(name, dir)
	if err != nil {
		return extensionUnit{}, err
	}
	return extensionUnit{
		Name:          name,
		Dir:           dir,
		ModulePath:    mod.Module.Mod.Path,
		Tables:        tables,
		HasMigrations: hasMigrations,
		Fragments:     fragments,
		Frontend:      frontend,
	}, nil
}

// holdsOnlyInstalledOutput reports whether a unit directory contains nothing a
// human wrote AND does contain an installed dependency tree. It is how the scan
// tells a half-removed unit from a directory that was always empty — two
// situations that look identical by content and want opposite advice.
//
// The node_modules requirement is what makes it the half-removed case rather
// than merely an empty one: `git rm -r` takes the tracked files and leaves the
// ignored install behind, so that tree IS the evidence a unit used to be here.
func holdsOnlyInstalledOutput(dir string) (bool, error) {
	installed, wrote := false, false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == nodeModulesDir {
				installed = true
				return fs.SkipDir
			}
			return nil
		}
		wrote = true
		return filepath.SkipAll
	})
	return installed && !wrote, err
}

// refuseNonRootGoPackages refuses any Go package inside a unit's tree other
// than the root package itself.
//
// This closes a gap the AST liveness walk cannot reach: parser.ParseDir in
// deriveUnitManifest only ever reads the unit's ROOT directory, never
// descending, so a package sitting in a subdirectory — reached only through
// a blank import from the root package's own source, e.g.
// `import _ ".../internal/live"` next to a `func init() { go dialOut() }` in
// internal/live/live.go — is parsed by nothing here. rejectLiveInitializers
// would never see that file to refuse its init(), and digestTree only
// hashes its bytes for staleness, which does not stop it running at import.
//
// Refusing the subpackage outright, rather than walking into it and
// re-running the same liveness checks there, is the only rule that holds:
// even a recursive walk would still leave the GENERAL form of this hole
// open, because a blank import of code OUTSIDE the unit tree — some other
// module entirely — cannot be gated by a generator that only ever reads
// this one unit's own files. This function says something about, and only
// about, Go packages inside the unit's own directory tree; it is not a
// guarantee about what a unit's import graph can reach.
//
// unbuiltCapabilityLayers is exempted at the top level, and the exemption
// buys message quality, not permission: scanUnit already refused those names
// outright above, so by the time this runs none of them exist under dir. A
// layer with a composition (migrations/) is NOT exempt — it is ordinary unit
// tree as far as Go packages are concerned, and an init() under it would run
// exactly as unchecked as one anywhere else.
func refuseNonRootGoPackages(name, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || slices.Contains(unbuiltCapabilityLayers, e.Name()) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		err := filepath.WalkDir(sub, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			hasGo, err := hasRootGoFiles(path)
			if err != nil {
				return err
			}
			if hasGo {
				rel, relErr := filepath.Rel(dir, path)
				if relErr != nil {
					rel = path
				}
				return fmt.Errorf("extensions/%s: %s/ holds a Go package outside the unit root — the declaration reader only parses the root package, so a subpackage's init() or an import-time call would run unchecked", name, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func hasRootGoFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true, nil
		}
	}
	return false, nil
}

// computeInputs digests everything generation reads:
// the core files feeding the composed outputs, each extension's source
// tree, and the installation approval lock. Content digests, not git
// revisions — identical in a work tree and a release tarball, and only
// a real input change invalidates the composition.
func computeInputs(root string) (manifestInputs, error) {
	core, err := coreDigest(root)
	if err != nil {
		return manifestInputs{}, err
	}
	lock, err := digestFileOrEmpty(filepath.Join(root, "extensions", "approvals.lock"))
	if err != nil {
		return manifestInputs{}, err
	}
	units, err := scanExtensions(root)
	if err != nil {
		return manifestInputs{}, err
	}
	rows := make(map[string]manifestExtRow, len(units))
	for _, u := range units {
		tree, err := digestTree(u.Dir)
		if err != nil {
			return manifestInputs{}, fmt.Errorf("extensions/%s: %w", u.Name, err)
		}
		// The manifest digests as it sits on disk: the fast staleness
		// probe (-verify-inputs) catches a hand edit or a missing file by
		// digest alone; only the full verify re-derives from the AST.
		unitManifest, err := digestFileOrEmpty(filepath.Join(u.Dir, unitManifestFile))
		if err != nil {
			return manifestInputs{}, fmt.Errorf("extensions/%s: %w", u.Name, err)
		}
		rows[u.Name] = manifestExtRow{Tree: tree, Manifest: unitManifest}
	}
	return manifestInputs{Core: core, ApprovalsLock: lock, Extensions: rows}, nil
}

// coreDigest covers the committed inputs the composed outputs derive from.
//
// EVERY base contract is hashed, not just crm.yaml, and the list is
// composedContractBases itself rather than a second copy of it. That is the
// whole point: each base is read by composedContracts and emitted as
// build/composition/api/<base>, so a base the digest missed would be an input
// of an output that the fast staleness probe (-verify-inputs) cannot see
// changing. The full -verify would still catch it — it regenerates and finds
// the output hash no longer reproduces the recorded one — but nothing goes RED
// in the meantime, which is the worst failure mode this generator has. Adding a
// fifth base to composedContractBases therefore extends the digest by
// construction; there is no second list to forget.
//
// What it covers besides: the workspace definition plus EVERY member's go.mod
// and go.sum (any member's dependency change can change the composed
// go.work.sum `go list -m all` resolves — tracking only backend's would
// let a tools/ or cli/ bump slip past `-verify`), the composition module
// contract (stub), and the published surface the extensions compile against.
//
// What it deliberately does NOT cover is the generator's own source — the merge
// rules in contractmerge.go among it. A digest of the tool that computes the
// digest could only ever chase itself, and the recorded toolchain plus -verify's
// full regeneration are what hold that end: a merge-rule change that alters any
// output makes the regenerated hash stop reproducing the recorded one.
func coreDigest(root string) (string, error) {
	h := newTreeHasher(root)
	files := []string{
		goWorkFile,
		"composition/go.mod",
		"composition/extensions_gen.go",
		// The SPA's committed vanilla registry, for the same reason as the Go
		// stub beside it: stubMatchesVanilla compares the generator's empty-tree
		// output against it, so a hand edit changes what the composition means
		// and must restale the fast probe rather than wait for a full -verify.
		frontendVanillaStub,
	}
	for _, base := range composedContractBases {
		files = append(files, "backend/"+apiLayer+"/"+base)
	}
	for _, rel := range files {
		if err := h.addFile(rel); err != nil {
			return "", err
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, goWorkFile))
	if err != nil {
		return "", err
	}
	rootWork, err := modfile.ParseWork(goWorkFile, raw, nil)
	if err != nil {
		return "", fmt.Errorf("root go.work: %w", err)
	}
	for _, use := range rootWork.Use {
		member := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(use.Path)), "./")
		if err := h.addFile(member + "/go.mod"); err != nil {
			return "", err
		}
		// go.sum may legitimately be absent (a dependency-free member);
		// absence digests as empty, so appearing registers as a change.
		if err := h.addFileOrEmpty(member + "/go.sum"); err != nil {
			return "", err
		}
	}
	if err := h.addTree("backend/pkg"); err != nil {
		return "", err
	}
	return h.sum(), nil
}
