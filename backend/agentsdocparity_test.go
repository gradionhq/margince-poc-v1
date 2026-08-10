// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// CLAUDE.md and AGENTS.md are deliberately different documents: CLAUDE.md is
// the long form, AGENTS.md the digest other agent harnesses read. Most sections
// differ on purpose and must not be synced.
//
// Three sections are the exception. They state binding invariants — the write
// shape, the license header, and the rules the review loop produced — and both
// files carry them in full because both are read in isolation: CLAUDE.md by
// Claude Code, AGENTS.md by `cli/craft`, which feeds the whole nearest
// AGENTS.md into the gate prompt. A pointer in one of them would leave whoever
// reads the other without the rule.
//
// Full copies in two files drift. This asserts they have not: the three
// sections must be byte-identical, so changing an invariant in one file fails
// here until it is changed in both.

import (
	"os"
	"strings"
	"testing"
)

// sharedSections are the H2 headings both rulebooks must carry identically.
// Each entry is matched as a prefix, so a heading may carry a parenthetical.
var sharedSections = []string{
	"## The write shape",
	"## License headers",
	"## Rules learned from the review loop",
}

func TestSharedRulebookSectionsAreIdenticalInBothDocs(t *testing.T) {
	claude := readRulebook(t, "../CLAUDE.md")
	agents := readRulebook(t, "../AGENTS.md")

	for _, heading := range sharedSections {
		inClaude, okClaude := findSection(claude, heading)
		if !okClaude {
			t.Errorf("CLAUDE.md is missing the %q section; both rulebooks must carry it in full", heading)
			continue
		}
		inAgents, okAgents := findSection(agents, heading)
		if !okAgents {
			t.Errorf("AGENTS.md is missing the %q section; both rulebooks must carry it in full", heading)
			continue
		}
		if inClaude != inAgents {
			t.Errorf("the %q section has drifted between CLAUDE.md and AGENTS.md.\n"+
				"Both files are read in isolation, so this rule is duplicated on purpose — "+
				"apply the same edit to both.\n--- CLAUDE.md ---\n%s\n--- AGENTS.md ---\n%s",
				heading, inClaude, inAgents)
		}
	}
}

func readRulebook(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(raw), "\n")
}

// findSection returns the body of the H2 section whose heading starts with
// prefix, from the line after the heading up to the next H2 (or end of file).
func findSection(lines []string, prefix string) (string, bool) {
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n")), true
}
