// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// Decision records replaced a system where the index and the records lived in
// two places and disagreed about which decisions were live. The index here is
// generated from the records, so it cannot make a claim the tree does not
// contain — but a generated file only stays true if something fails when it is
// stale, which is what this asserts.
//
// It also holds two properties of the records themselves. Every record states a
// status, because a reader who cannot tell live from superseded has to read all
// of them. And every "Superseded by" names a record that exists, because a
// dangling pointer sends that reader nowhere.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const adrDir = "../docs/adr"

// supersededBy captures the record a superseded decision points at.
var supersededBy = regexp.MustCompile(`Superseded by \[?(ADR-\d{4})`)

// statusHeading matches the metadata line itself, anchored, so the marker
// appearing in ordinary prose is not mistaken for a declaration.
var statusHeading = regexp.MustCompile(`(?m)^\*\*Status:\*\*[ \t]*(.*)$`)

// statusValue is the taxonomy. A qualifier after the word is allowed and is how
// a record says which half of it is built; the word itself is not open.
var statusValue = regexp.MustCompile(`^(Active|Retired|Superseded by)\b`)

// statusLine returns the declared status value and whether the record declares
// one at all. The two are separate answers: a missing line and a line with an
// empty value both yield "", and only the second is a record that tried.
func statusLine(body string) (string, bool) {
	m := statusHeading.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func TestEveryDecisionRecordStatesItsStatus(t *testing.T) {
	for _, rec := range decisionRecords(t) {
		line, declared := statusLine(readRecord(t, rec))
		switch {
		case !declared:
			t.Errorf("%s has no `**Status:**` line at the start of a line — a reader "+
				"cannot tell whether this decision is live", rec)
		case !statusValue.MatchString(line):
			// The value may carry a qualifier ("Active — the contract half is not
			// built"), which is the shape a record with a known gap uses. What it
			// may not be is empty, or a word outside the taxonomy.
			t.Errorf("%s states a status this repository does not use: %q\n"+
				"\tuse Active, Superseded by [ADR-NNNN](...), or Retired", rec, line)
		}
	}
}

func TestASupersededRecordNamesOneThatExists(t *testing.T) {
	present := map[string]bool{}
	records := decisionRecords(t)
	for _, rec := range records {
		present[strings.SplitN(rec, "-", 3)[0]+"-"+strings.SplitN(rec, "-", 3)[1]] = true
	}

	for _, rec := range records {
		for _, m := range supersededBy.FindAllStringSubmatch(readRecord(t, rec), -1) {
			if !present[m[1]] {
				t.Errorf("%s says it is superseded by %s, which is not in docs/adr/ — "+
					"the reader lands nowhere", rec, m[1])
			}
		}
	}
}

func TestNoTwoRecordsClaimTheSameNumber(t *testing.T) {
	// A number is how every citation in the tree reaches a record, so two files
	// sharing one makes every one of those citations ambiguous. This caught a
	// real duplicate the first time it ran: the same decision written twice
	// under two slugs, which the generated index rendered as two rows with no
	// sign anything was wrong.
	seen := map[string]string{}
	for _, rec := range decisionRecords(t) {
		num := strings.SplitN(strings.TrimPrefix(rec, "ADR-"), "-", 2)[0]
		if first, dup := seen[num]; dup {
			t.Errorf("ADR-%s is claimed by two files, so a citation to it is ambiguous:\n\t%s\n\t%s",
				num, first, rec)
			continue
		}
		seen[num] = rec
	}
}

func TestTheDecisionIndexIsNotStale(t *testing.T) {
	if len(decisionRecords(t)) == 0 {
		t.Skip("no decision records yet")
	}

	committed, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	if err != nil {
		t.Fatalf("reading the committed index: %v (run `make adr-index`)", err)
	}

	// The generator writes wherever it is told, so this compares against a
	// scratch file and never touches the tree it is checking. A gate that
	// rewrites the file it checks corrupts the worktree the moment it fails
	// partway through, and races with any other test doing the same.
	scratch := filepath.Join(t.TempDir(), "README.md")
	if out, err := exec.Command("../scripts/gen-adr-index.sh", scratch).CombinedOutput(); err != nil {
		t.Fatalf("regenerating the index: %v\n%s", err, out)
	}
	regenerated, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("reading the regenerated index: %v", err)
	}

	if string(regenerated) != string(committed) {
		t.Error("docs/adr/README.md is stale — run `make adr-index` and commit the result")
	}
}

// decisionRecords lists the record filenames, derived from the tree so a new
// record is covered without anyone adding it to a list here.
func decisionRecords(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", adrDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "ADR-") && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	return out
}

func readRecord(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(adrDir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(body)
}
