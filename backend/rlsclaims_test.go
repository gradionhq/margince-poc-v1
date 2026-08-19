// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Fitness function over a guarantee this codebase no longer has.
//
// Core migration 0217 (ADR-0091 §8 phase A) dropped all 139 tenant-isolation
// policies and both RLS flags from every schema. What replaced them is a
// predicate each statement writes for itself against the app.workspace_id GUC
// — so a source comment saying RLS scopes, bounds, confines or gates a read
// names a control the database does not apply, and the next reader trusts it
// instead of checking. Nineteen statements whose scope had been the DATABASE's
// were found during phase A, two of them data loss and one a cross-tenant
// disclosure; every one was found by a failing test, because nothing gates the
// class. This is the cheap half of that gate: it cannot tell whether a query
// is scoped, but it can stop the tree from claiming a retired mechanism
// scopes it.
//
// It bans the CLAIM, not the word. Three spellings stay legal because they
// name something real: the `// rls-exempt:` waiver marker that
// scripts/check-rls-store-path.sh reads, the BYPASSRLS/NOBYPASSRLS role
// attributes (a live cluster property AssertRuntimeRole still refuses), and
// prose that names the retirement itself.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rlsClaim matches RLS asserted as a live scoping control. The verb list is
// the set of spellings this tree actually grew before the gate existed, in
// both orders (`RLS scopes it` and `scoped by RLS`) plus the hyphenated
// adjective form, which is how most of them were written.
// The suffix group carries `d` as well as `ed` because the stems are whole
// words: "scoped" is scope+d, not scope+ed. Leaving it out is what the pinning
// test below caught — the gate had swept the tree clean of `RLS-scoped` while
// being unable to see it.
var rlsClaim = regexp.MustCompile(`(?i)\bRLS[ -](?:scope|bound|confine|restrict|isolate|enforce|govern|gate|bind|keep|constrain|protect)(?:s|d|es|ed)?\b` +
	`|\bRLS already\b` +
	`|\b(?:scoped|bounded|confined|restricted|isolated|gated|governed|protected|filtered)[ -]by[ -]RLS\b` +
	`|\bFORCE RLS (?:doesn't|does not|do not|don't)\b` +
	// The prose form a decision record reaches for. A migrated record said
	// "tenant isolation through row-level security" and no pattern above
	// matched it, because every one of them keys on the abbreviation.
	`|(?i)\btenant isolation (?:through|through the|via|by|using) row.level security\b`)

// TestTheRLSClaimPatternCatchesTheSpellingsThisTreeGrew pins the pattern
// against the real phrasings the 2026-08 sweep removed, and against the
// three that must survive it. A gate whose pattern silently stopped matching
// would read exactly like a clean tree.
func TestTheRLSClaimPatternCatchesTheSpellingsThisTreeGrew(t *testing.T) {
	mustMatch := []string{
		"// EmailSuppressed reports whether an address belongs to an erased subject in the current workspace (RLS scopes the read).",
		"// members (row-scoped by RLS), optionally filtered by in.Q.",
		"// comms_outbound is RLS-scoped, so every read the dispatcher makes",
		"// The workspace predicate duplicates what RLS already enforces",
		"// for the RLS-governed catalog insert and audit write.",
		"// workspace-scoped read (RLS confines it to the tenant)",
		"// superuser, so FORCE RLS doesn't bite behaviorally",
	}
	for _, line := range mustMatch {
		if !rlsClaim.MatchString(line) {
			t.Errorf("the pattern no longer catches a claim it was written for:\n\t%s", line)
		}
	}
	mustNotMatch := []string{
		"\t// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped",
		"// the fixture owner role holds neither rolsuper nor rolbypassrls",
		"// margince_owner is NOSUPERUSER NOBYPASSRLS in db-bootstrap.sql",
		"// core 0217 (ADR-0091) retired every tenant-isolation policy",
		"// the query's own workspace predicate scopes the read",
	}
	for _, line := range mustNotMatch {
		if rlsClaim.MatchString(line) {
			t.Errorf("the pattern flags a legitimate mention:\n\t%s", line)
		}
	}
}

// TestNoGoSourceClaimsRLSStillScopesARead walks the same hand-written trees the
// license notice covers — derived from the tree, so a new file is enrolled the
// moment it exists — and the decision records under docs/adr/ alongside them.
//
// The name says Go source because that is where the sweep started. It reads
// prose too: a decision record ratified row-level security as the live tenant
// control months after 0217 retired it, and a Go-only gate had nothing to say
// about it. The name is kept because the shipped record of that sweep cites it.
func TestNoGoSourceClaimsRLSStillScopesARead(t *testing.T) {
	var claims []string
	checked := 0
	for _, tree := range licensedTrees {
		err := filepath.WalkDir(tree.root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return fs.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || !d.Type().IsRegular() {
				return nil
			}
			// This file states the banned spellings in order to pin them, so
			// it is the one file the pattern must not read. The exemption is
			// by name and nothing else: any OTHER file naming itself the same
			// way is still scanned, and the pinning test above is what proves
			// the pattern still bites while this file goes unread.
			if d.Name() == "rlsclaims_test.go" && filepath.Dir(path) == "." {
				return nil
			}
			b, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.go file from walking the trusted source tree
			if err != nil {
				return err
			}
			checked++
			for i, line := range strings.Split(string(b), "\n") {
				if rlsClaim.MatchString(line) {
					claims = append(claims, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", tree.root, err)
		}
	}
	// Decision records make the same claim in prose, and until this was added
	// they were not scanned at all: a migrated record ratified row-level
	// security as the live tenant control months after 0217 retired it, and
	// every Go-only gate passed it.
	//
	// Prose is scanned per PARAGRAPH, not per line. Markdown wraps at 80
	// columns wherever the words fall, so the record that caused this split
	// "tenant" from "isolation through row-level security" across two lines and
	// a line-by-line sweep matched neither half. A gate that a line break
	// defeats is one the next author defeats by accident.
	records, err := filepath.Glob("../docs/adr/ADR-*.md")
	if err != nil {
		t.Fatalf("listing decision records: %v", err)
	}
	for _, path := range records {
		b, err := os.ReadFile(path) // #nosec G304 -- path comes from a glob over the tracked docs tree
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		checked++
		for _, para := range prosePargraphs(string(b)) {
			if rlsClaim.MatchString(para.text) {
				claims = append(claims, filepath.ToSlash(path)+":"+strconv.Itoa(para.line)+": "+para.text)
			}
		}
	}

	if checked == 0 {
		t.Fatal("the sweep read no file — a gate that scans nothing passes exactly like a clean one")
	}
	if len(claims) > 0 {
		t.Errorf("%d line(s) credit RLS with a guarantee core 0217 retired. "+
			"State what actually scopes the statement — its own workspace predicate, the row-scope "+
			"clauses in platform/auth, or A107/ADR-0061's single organization — and if the answer is "+
			"\"nothing does\", that is a defect to fix rather than a comment to reword:\n\t%s",
			len(claims), strings.Join(claims, "\n\t"))
	}
}

// paragraph is one blank-line-delimited block of prose, flattened to a single
// line, with the source line its first line came from so a finding is still
// navigable.
type paragraph struct {
	text string
	line int
}

// prosePargraphs joins wrapped lines so a claim split across a line break is
// still one string to match against. Fenced code blocks are skipped: a sample
// inside one is an illustration, not an assertion about this repository.
func prosePargraphs(body string) []paragraph {
	var out []paragraph
	var cur []string
	start, fenced := 0, false

	flush := func() {
		if len(cur) > 0 {
			out = append(out, paragraph{text: strings.Join(cur, " "), line: start})
			cur = nil
		}
	}

	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			flush()
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if len(cur) == 0 {
			start = i + 1
		}
		cur = append(cur, strings.TrimSpace(line))
	}
	flush()
	return out
}

// TestTheProseSweepSurvivesALineWrap pins the shape that defeated the first
// version of this gate. The record that prompted it wrapped at 80 columns
// between "tenant" and "isolation through row-level security", so every
// line-by-line match failed while the claim sat in plain sight. The wrap is
// reproduced verbatim below: reflow it onto one line and this test stops
// proving anything, which is why it is written out rather than generated.
func TestTheProseSweepSurvivesALineWrap(t *testing.T) {
	wrapped := "test red, so the change cannot merge. The controls covered this way are tenant\n" +
		"isolation through row-level security, the rule that an agent's permissions never\n" +
		"exceed those of the human who granted them.\n"

	for _, line := range strings.Split(wrapped, "\n") {
		if rlsClaim.MatchString(line) {
			t.Fatalf("a single line already matches, so this fixture no longer pins the wrap: %q", line)
		}
	}

	paras := prosePargraphs(wrapped)
	if len(paras) != 1 {
		t.Fatalf("expected the three lines to join into one paragraph, got %d", len(paras))
	}
	if !rlsClaim.MatchString(paras[0].text) {
		t.Error("the joined paragraph does not match the claim pattern — a wrapped claim would ship unnoticed")
	}
}

// TestTheProseSweepIgnoresFencedCode keeps the gate from failing an honest
// record that quotes the retired mechanism inside a code sample.
func TestTheProseSweepIgnoresFencedCode(t *testing.T) {
	body := "Prose above.\n\n```sql\n-- tenant isolation through row-level security, as it was\n```\n\nProse below.\n"
	for _, p := range prosePargraphs(body) {
		if rlsClaim.MatchString(p.text) {
			t.Errorf("a fenced sample was scanned as an assertion: %q", p.text)
		}
	}
}
