// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
}

// scanExtensions reads the enabled set. Every capability layer the
// skeleton cannot compose yet (api/, frontend/, migrations/) is a hard
// error, not a silent drop — an extension shipping one of those must not
// build until its composition slice exists.
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
	return units, nil
}

func scanUnit(name, dir string) (extensionUnit, error) {
	for _, sub := range []string{"api", "frontend", "migrations"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err == nil {
			return extensionUnit{}, fmt.Errorf("extensions/%s: %s/ composition is not built yet — the walking skeleton composes Go registrations only", name, sub)
		}
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
		return extensionUnit{}, fmt.Errorf("extensions/%s: nothing to compose (no Go module)", name)
	case err != nil:
		return extensionUnit{}, err
	}
	if !hasGo {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod present but no root package — the unit's root package must export New() (ADR-0069 §4)", name)
	}
	mod, err := modfile.Parse(filepath.Join(dir, "go.mod"), modBytes, nil)
	if err != nil {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod: %w", name, err)
	}
	if mod.Module == nil || mod.Module.Mod.Path == "" {
		return extensionUnit{}, fmt.Errorf("extensions/%s: go.mod declares no module path", name)
	}
	return extensionUnit{Name: name, Dir: dir, ModulePath: mod.Module.Mod.Path}, nil
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

// computeInputs digests everything generation reads (CODEORG-RULE-5):
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
		rows[u.Name] = manifestExtRow{Tree: tree}
	}
	return manifestInputs{Core: core, ApprovalsLock: lock, Extensions: rows}, nil
}

// coreDigest covers exactly the committed inputs the composed outputs
// derive from: the workspace definition plus EVERY member's go.mod and
// go.sum (any member's dependency change can change the composed
// go.work.sum `go list -m all` resolves — tracking only backend's would
// let a tools/ or cli/ bump slip past `-verify`), the composition module
// contract (stub), the base API contract, and the published surface the
// extensions compile against.
func coreDigest(root string) (string, error) {
	h := newTreeHasher(root)
	for _, rel := range []string{
		"go.work",
		"backend/api/crm.yaml",
		"composition/go.mod",
		"composition/extensions_gen.go",
	} {
		if err := h.addFile(rel); err != nil {
			return "", err
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return "", err
	}
	rootWork, err := modfile.ParseWork("go.work", raw, nil)
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
	if err := h.addGoTree("backend/pkg"); err != nil {
		return "", err
	}
	return h.sum(), nil
}

// treeHasher accumulates (relpath, content-hash) pairs and digests the
// sorted list — file identity and bytes, never timestamps.
type treeHasher struct {
	root  string
	lines []string
}

func newTreeHasher(root string) *treeHasher { return &treeHasher{root: root} }

func (h *treeHasher) addFile(rel string) error {
	content, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	h.lines = append(h.lines, rel+"\x00"+digestBytes(content))
	return nil
}

// addFileOrEmpty records a file that may legitimately be absent —
// absence digests as empty input, so the file appearing or vanishing
// both register as a change.
func (h *treeHasher) addFileOrEmpty(rel string) error {
	digest, err := digestFileOrEmpty(filepath.Join(h.root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	h.lines = append(h.lines, rel+"\x00"+digest)
	return nil
}

func (h *treeHasher) addGoTree(rel string) error {
	return filepath.WalkDir(filepath.Join(h.root, filepath.FromSlash(rel)), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		sub, err := filepath.Rel(h.root, path)
		if err != nil {
			return err
		}
		return h.addFile(filepath.ToSlash(sub))
	})
}

func (h *treeHasher) sum() string {
	sort.Strings(h.lines)
	return digestBytes([]byte(strings.Join(h.lines, "\n")))
}

// digestTree hashes every regular file under dir. A symlink is refused:
// it would digest as its target's bytes while provenance points
// elsewhere, and a real installation lands extensions as plain trees.
func digestTree(dir string) (string, error) {
	h := newTreeHasher(dir)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			// A symlink would digest as its target's bytes while
			// provenance points elsewhere; a FIFO would block the read
			// forever. An extension unit is a plain file tree.
			return fmt.Errorf("%s: only regular files are part of an extension unit (found %s)", path, d.Type())
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		return h.addFile(filepath.ToSlash(rel))
	})
	if err != nil {
		return "", err
	}
	return h.sum(), nil
}

func digestBytes(b []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b))
}

// digestFileOrEmpty digests a file that may legitimately be absent (the
// approval lock before any approval, go.work.sum for a dependency-free
// workspace); absence digests as empty input, recorded, so appearing and
// vanishing both register as a change.
func digestFileOrEmpty(path string) (string, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return digestBytes(nil), nil
	}
	if err != nil {
		return "", err
	}
	return digestBytes(content), nil
}
