// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// person_profile_field holds what a machine ASSERTED about a person, and
// ai_feedback holds what a human then decided about that assertion. A surface
// that shows the value without consulting the ledger shows the reader the exact
// claim they already overrode — so consulting it cannot be one caller's job.
//
// person360's readProfileFields overlays the verdict and its comment says it is
// every read that renders the table. That was a claim with nothing holding it,
// which is the shape this repo's own rulebook forbids: "a comment may not claim
// to be the only implementation unless a test holds it", and every such claim
// audited in this tree had turned out false. This is the test.
//
// It judges READS that serve values, so an existence probe and a merge relink
// are not subjects: neither hands a stored value to anybody, and asking them to
// consult a verdict ledger would be asking the wrong question.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

var profileFieldRead = gatekit.TableReadPattern("person_profile_field")

// selectsValues distinguishes a read that serves the assertion from one that
// only asks whether a row exists or moves it. `SELECT 1 … EXISTS` and a
// `DELETE`/`UPDATE … SET person_id` touch the table without ever putting a
// machine's claim in front of a reader.
//
// The value column may sit ANYWHERE in the projection, so the match spans from
// SELECT to FROM rather than anchoring on the first column. Anchoring on the
// first is how it was written and it was wrong in the direction that matters:
// `SELECT ppf.person_id, ppf.value FROM person_profile_field` serves the value
// and would have escaped the census entirely, leaving this gate green over the
// defect it exists to name. It only passed at all because sar.go happens to
// list `field` first.
//
// `field` is deliberately NOT one of the value words. It is the column that
// says WHICH assertion a row is about, so it appears in the WHERE of every
// existence probe on this table — including one nested inside an unrelated
// query, which is how the candidate sweep in enrichsignature.go came to look
// like a value reader. The words below are the assertion ITSELF and its
// evidence, which is what a verdict can overturn.
//
// A STAR projection is a value serve too, and it names none of those words.
// `SELECT *` and `SELECT ppf.*` are matched separately for that reason: a
// census that misses them misses them SILENTLY, which is the one direction
// this gate must not fail in.
//
// The residue, and it fails the safe way: the words are matched between a
// SELECT and a later FROM within a statement that names this table, so a
// `value` column belonging to a DIFFERENT table in the same statement matches
// too. That is a false POSITIVE — it surfaces as a reader that must be
// overlaid or ratified, never as silence — and a wrong waiver is visible in
// review where a missed reader is not.
// valueColumns are the columns that carry the assertion itself or the evidence
// for it — the things a human verdict can overturn.
//
// A list rather than words spelled inside the pattern, so the defect test can
// COMPARE it against a set of words that test writes out for itself. The test
// deliberately does not iterate this list to build its cases: a case derived
// from the same list the pattern is built from disappears when a word does, and
// proves nothing about that word. The comparison is what holds both directions
// — a word dropped here fails, and a word added here fails until the test names
// it too.
var valueColumns = []string{"value", "evidence_snippet", "source_ref", "confidence"}

var selectsValues = regexp.MustCompile(
	`(?is)SELECT\s.*?(?:\b(?:` + strings.Join(valueColumns, "|") + `)\b|(?:[a-z_]+\.)?\*).*?\sFROM\s`)

// profileFieldValueReaders ratifies each statement that serves values from the
// table WITHOUT the verdict overlay, and says what each one costs.
var profileFieldValueReaders = gatekit.Waive(map[string]string{
	"internal/modules/people/mergerelink.go":      "the merge copies each surviving evidence row from the merged-away person to the survivor, value and provenance together, and deletes the source rows. It serves nobody: the values move between two records and are read back through person360 afterwards like any other, verdict and all. Overlaying here would write the OVERLAID value into the survivor's row and destroy the distinction between what the machine asserted and what a human decided about it — the copy must be faithful precisely because the ledger is consulted later. The cost is that a verdict keyed to the merged-away person's id does not follow its value across, which is a gap in the merge rather than in this reader",
	"internal/modules/people/orgnamepromotion.go": "RATIFIED PENDING A DECISION, see issue #2319. The corroborated org-name promotion reads the org_name signature values to count how many people at an organization state the same employer name. It does not show them as fact — it ranks a claim and, when signatures alone are the only agreement, stages a 🟡 proposal for a human. But it does not consult the ledger either, so an org_name a human already REJECTED still corroborates. Whether a verdict about one person's signature should veto a rename of their employer is a product question with an argument on each side, which is why it is filed rather than decided here. The cost, stated plainly: until that is answered, a rejected claim carries weight it may not deserve",
	"internal/modules/privacy/sar.go":             "the Article 15 export owes the subject what this installation HOLDS, and it holds two facts: the value the machine asserted and the verdict the human recorded against it. It exports the stored columns here and ai_feedback as its own section beside them, so the subject sees both. Overlaying the verdict instead would hand them one merged value and conceal that an override exists — the opposite of what an export is for. It also cannot share person360's reader: privacy is a module and may not import compose (ADR-0054 §3). The cost is that the two statements must be corrected together when the column set changes, which is why they name each other at both sites",
})

var profileFieldReaderScope = gatekit.Scope{
	Roots:   []string{"internal"},
	Subject: readsProfileFieldValues,
	Exempt:  gatekit.Waive(map[string]string{}),
}

func readsProfileFieldValues(path string, file *ast.File) bool {
	if !gatekit.FileReadsTable(path, file, profileFieldRead) {
		return false
	}
	for _, read := range gatekit.TableReads(file, profileFieldRead) {
		if selectsValues.MatchString(read.SQL) {
			return true
		}
	}
	return false
}

// The overlay itself, matched in the FUNCTION that does the reading — not
// file-wide, which is how the sibling activity census matches and is wrong
// here. A file-wide match is satisfied by applyFieldVerdicts merely being
// DECLARED in the file, so deleting the call from readProfileFields leaves it
// green: a marker the defect cannot remove is not a marker.
//
// Per-function is right for this obligation because the overlay is a direct
// call from the reader (readProfileFields → applyFieldVerdicts → VerdictsForTx)
// rather than a WHERE fragment assembled elsewhere. A future reader that wraps
// the pair in a third function still passes: it reads through the wrapper, so
// the read and the call are in the same declaration again.
var verdictOverlayMarkers = []string{"applyFieldVerdicts", "VerdictsForTx"}

// unoverlaidValueReaders names each declaration in the file that serves values
// from the table and consults no verdict of its own.
func unoverlaidValueReaders(file *ast.File) []string {
	var offenders []string
	for _, decl := range file.Decls {
		reads := gatekit.DeclReads(decl, profileFieldRead)
		serves := false
		for _, read := range reads {
			if selectsValues.MatchString(read.SQL) {
				serves = true
			}
		}
		if !serves || gatekit.CallsAny(decl, verdictOverlayMarkers) {
			continue
		}
		name := "a package-level SQL fragment"
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			name = fn.Name.Name
		}
		offenders = append(offenders, name)
	}
	return offenders
}

func TestEveryReaderServingProfileFieldValuesConsultsTheVerdictLedger(t *testing.T) {
	defer profileFieldValueReaders.AssertAllMatched(t)
	files := profileFieldReaderScope.Files(t)
	if len(files) == 0 {
		t.Fatal("no reader of person_profile_field found — the matcher has stopped seeing this tree's SQL, and a gate that judges nothing reads exactly like a clean one")
	}
	for _, src := range files {
		if profileFieldValueReaders.Waived(t, src.Path) {
			continue
		}
		for _, offender := range unoverlaidValueReaders(src.File) {
			t.Errorf("%s: %s serves person_profile_field values without consulting ai_feedback — it would show the machine's claim as fact to somebody who already overrode it. Route it through person360's readProfileFields, or ratify it in profileFieldValueReaders with the reason and the cost", src.Path, offender)
		}
	}
}

// The census's own defect test.
//
// Every shape it must catch, and every lookalike it must not, parsed from
// source here rather than planted in the tree — the two shell gates on this
// branch point at a throwaway directory for the same reason, and a Go parser
// needs no directory at all.
//
// It exists because this census has been wrong twice, in the direction that
// does not announce itself. It first matched the overlay marker file-wide, so
// deleting the overlay call from readProfileFields left it GREEN because
// applyFieldVerdicts was still declared below. Then it anchored on the first
// column of a projection, so `SELECT ppf.person_id, ppf.value` escaped it —
// and a star projection escaped it too, naming none of the value words at all.
// Each was found by a person or a bot, not by the gate, which is the argument
// for this test.
func TestTheProfileFieldCensusSeesWhatItClaimsTo(t *testing.T) {
	for name, tc := range map[string]struct {
		source string
		want   []string
	}{
		"a value column first in the projection": {
			source: `func read() string { return "SELECT value, field FROM person_profile_field" }`,
			want:   []string{"read"},
		},
		// The shape that escaped the first widening.
		"a value column later in the projection": {
			source: `func read() string { return "SELECT ppf.person_id, ppf.value FROM person_profile_field ppf" }`,
			want:   []string{"read"},
		},
		// And the shape that named none of the words.
		"a qualified star": {
			source: `func read() string { return "SELECT ppf.* FROM person_profile_field ppf" }`,
			want:   []string{"read"},
		},
		"a bare star": {
			source: `func read() string { return "SELECT * FROM person_profile_field" }`,
			want:   []string{"read"},
		},
		// An existence probe serves nobody: `field` says WHICH assertion a row
		// is about and appears in the WHERE of every one of them, which is why
		// it is not a value word.
		"an existence probe is not a serve": {
			source: `func probe() string { return "SELECT 1 FROM person_profile_field f WHERE f.field = 'org_name'" }`,
			want:   nil,
		},
		"a delete is not a serve": {
			source: `func retire() string { return "DELETE FROM person_profile_field WHERE person_id = $1" }`,
			want:   nil,
		},
		// The overlay must be reached by the DECLARATION that reads, not merely
		// declared somewhere in the file. This is the file-wide hole, pinned.
		"a reader that consults the ledger passes": {
			source: `func read() string {
				applyFieldVerdicts()
				return "SELECT value FROM person_profile_field"
			}`,
			want: nil,
		},
		"an overlay declared elsewhere does not answer for the reader": {
			source: `func read() string { return "SELECT value FROM person_profile_field" }
func applyFieldVerdicts() {}`,
			want: []string{"read"},
		},
		"another table's value column is not this table's": {
			source: `func read() string { return "SELECT value FROM ai_feedback" }`,
			want:   nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := unoverlaidValueReaders(parseProbe(t, tc.source))
			if len(got) != len(tc.want) {
				t.Fatalf("offenders = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("offender %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}

	// EVERY value word gets a must-catch case, and the list is written out HERE
	// rather than taken from valueColumns. A test and the thing it tests cannot
	// share a source: cases derived from the pattern's own list vanish when a
	// word does, so they say nothing about that word.
	//
	// The equality check below is what makes this hold in both directions: a
	// word removed from the pattern fails it, and a word ADDED to the pattern
	// fails it until somebody writes the case that proves the pattern sees it.
	expectedValueColumns := []string{"value", "evidence_snippet", "source_ref", "confidence"}
	if strings.Join(valueColumns, ",") != strings.Join(expectedValueColumns, ",") {
		t.Fatalf("valueColumns = %v, and this test names %v — every value word owes a must-catch case below, so add or remove one here to match",
			valueColumns, expectedValueColumns)
	}
	for _, column := range expectedValueColumns {
		t.Run("serving "+column+" is a serve", func(t *testing.T) {
			source := `func read() string { return "SELECT ` + column + ` FROM person_profile_field" }`
			if got := unoverlaidValueReaders(parseProbe(t, source)); len(got) != 1 {
				t.Errorf("a read of %s was not seen as serving a value: offenders = %v", column, got)
			}
		})
	}
}

// parseProbe compiles one probe declaration into the AST the census reads, so a
// case states the SQL it means and nothing else.
func parseProbe(t *testing.T, source string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", "package probe\n\n"+source, 0)
	if err != nil {
		t.Fatalf("parsing the probe: %v", err)
	}
	return file
}
