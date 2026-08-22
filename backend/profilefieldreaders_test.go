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
	"regexp"
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
var selectsValues = regexp.MustCompile(
	`(?is)SELECT\s.*?(?:\b(?:value|evidence_snippet|source_ref|confidence)\b|(?:[a-z_]+\.)?\*).*?\sFROM\s`)

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
// here. Measured: with a file-wide match, deleting the overlay call from
// readProfileFields left this test green, because applyFieldVerdicts was still
// declared ten lines below. A marker the defect cannot remove is not a marker.
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
