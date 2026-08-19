// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// This repository is public. Everything in it is readable by anyone, which
// makes a reference to a private repository, document or pull request two
// distinct failures at once: it leaks internal structure, and it sends a
// contributor somewhere they cannot go to satisfy a rule that then blocks
// their push.
//
// The rule is therefore absolute rather than a matter of taste: if a rule
// matters, it is written out here. A citation is by decision number, and the
// number is a label, not a pointer: the record it names is not in this tree.
//
// This derives the file list from git rather than walking the filesystem, so
// an untracked scratch file is out of scope and a newly tracked one is in it
// automatically. There is no exemption list: the one file that could not be
// cleaned was moved out of this repository instead, and an exemption is a hole
// somebody eventually widens.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbidden is what a private reference looks like in this tree's history.
// Each pattern carries the plain-language reason it fails, because a gate that
// only says "no match allowed" leaves the reader guessing what to write.
var forbidden = []struct {
	name    string
	pattern *regexp.Regexp
	why     string
}{
	{
		name:    "private repository name",
		pattern: regexp.MustCompile(`margince-(foundation|principles|business)`),
		why:     "name the thing, not the repository — a reader here cannot open it",
	},
	{
		name:    "private specification path",
		pattern: regexp.MustCompile(`(^|[^\w/.-])specs/[a-z]`),
		why:     "write the rule out here — a public contributor cannot open anything else",
	},
	{
		name:    "private pull-request reference",
		pattern: regexp.MustCompile(`foundation\s?#\d+`),
		why:     "link an issue or PR in this repository, or state the fact without a link",
	},
}

// scanned are the file types whose prose a human or agent actually reads.
// Binary and generated artifacts are excluded: a match inside a lockfile or an
// image is noise, and generated files are regenerated rather than edited.
var scanned = map[string]bool{
	".go": true, ".md": true, ".yml": true, ".yaml": true,
	".json": true, ".ts": true, ".tsx": true, ".sh": true, ".sql": true,
}

func TestPublicTreeCitesNothingPrivate(t *testing.T) {
	out, err := exec.Command("git", "-C", "..", "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("listing tracked files: %v (this test must run inside the git worktree)", err)
	}

	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" || !scanned[filepath.Ext(rel)] {
			continue
		}
		// This file names the patterns it bans, so scanning it would fail on
		// its own source. The exemption is the exact path, not the basename: a
		// second file called publicreferences_test.go anywhere else in the tree
		// is scanned like everything else.
		if rel == "backend/publicreferences_test.go" {
			continue
		}
		assertFileCitesNothingPrivate(t, rel)
	}
}

// assertFileCitesNothingPrivate reports every offending line in one file, so a
// sweep is one fix-and-rerun cycle rather than one per line.
func assertFileCitesNothingPrivate(t *testing.T, rel string) {
	t.Helper()

	// The working copy, not the committed blob: a violation must fail before
	// it is committed, not after.
	body, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		// A tracked file can be absent from the working tree mid-rebase or
		// after `git rm`. That is not this gate's business.
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading %s: %v", rel, err)
	}

	for i, line := range strings.Split(string(body), "\n") {
		for _, f := range forbidden {
			if f.pattern.MatchString(line) {
				t.Errorf("%s:%d carries a %s — %s\n\t%s",
					rel, i+1, f.name, f.why, strings.TrimSpace(line))
			}
		}
	}
}
