// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Content addressing for the composition: how a unit's tree, the core's inputs
// and a single file become the hashes the staleness probe and the unit manifest
// compare. Split out of scan.go, which reads the enabled set — one file finds
// the units, this one decides what their bytes amount to.

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

// addFileOrEmpty records a file that may legitimately be absent, with
// an explicit absence MARKER — a zero-byte file and a missing file must
// not share a digest, or presence itself would stop being part of the
// input identity.
func (h *treeHasher) addFileOrEmpty(rel string) error {
	content, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		h.lines = append(h.lines, rel+"\x00absent")
		return nil
	}
	if err != nil {
		return err
	}
	h.lines = append(h.lines, rel+"\x00"+digestBytes(content))
	return nil
}

// addTree hashes every regular file under rel — the whole subtree, not
// just .go — so a non-Go asset the published surface gains (a go:embed
// template or schema, including a dot-prefixed one an `all:` pattern can
// embed, or one that happens to end in _test.go) still invalidates the
// composition when it changes. The digest classifies nothing by name: it
// hashes bytes, conservatively, so the staleness probe never misses a
// change. A non-regular entry is refused, as in digestTree.
func (h *treeHasher) addTree(rel string) error {
	root := filepath.Join(h.root, filepath.FromSlash(rel))
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: only regular files back the composition digest (found %s)", path, d.Type())
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
// The unit's generated manifest is excluded: it derives FROM this tree,
// so including it would make the digest chase the generator's own
// output — it rides in its own manifestExtRow field instead.
func digestTree(dir string) (string, error) {
	h := newTreeHasher(dir)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
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
		if filepath.ToSlash(rel) == unitManifestFile {
			return nil
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
