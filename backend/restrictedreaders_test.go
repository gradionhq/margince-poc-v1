// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A record held under a statutory retention obligation is unavailable in
// EVERY ordinary read path (A165/ADR-0114 §2): lists, timelines, search,
// exports, embeddings, agent grounding. This gate derives the readers of the
// activity table from the tree and asks each how it excludes a held row,
// because a reader that forgets is indistinguishable from one that never
// existed — until a supervisory authority asks why an erased subject's
// correspondence is on a sales rep's screen.
//
// A file satisfies the gate by ONE of three means, each a real exclusion:
//
//   - it carries the shared row scope (auth.ActivityScopeClause or one of the
//     probes built on it), which always composes ActivityAvailableClause;
//   - it names restricted_at itself, or the privacy floor fragments that do;
//   - it filters `archived_at IS NULL`, which excludes held rows because the
//     schema makes restricted imply archived (activity_restricted_is_archived).
//
// A file that reads activity by none of those is waived here with the reason
// it may — and each waiver names the cost.

import (
	"go/ast"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// activityReadLiteral matches a SQL string literal that reads the activity
// table by name. `activity_link`, `activity_participant` and the other
// activity_* tables are deliberately not matched: they carry no content.
var activityReadLiteral = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+activity(\s|$|\n)`)

// restrictedExclusionMarkers are the spellings that exclude a held row. A
// file carrying any of them anywhere is taken as excluding — file-level on
// purpose: the SQL in this tree is assembled from fragments, so a per-statement
// judgement would need to evaluate Go string concatenation, and the gate
// would be wrong about the fragments more often than the files.
var restrictedExclusionMarkers = []string{
	"ActivityScopeClause", "ActivityAvailableClause",
	"EnsureActivityVisible", "EnsureActivityVisibleLive",
	"restricted_at",
	"correspondenceFloorPredicate", "handelsbriefShielded",
	"archived_at IS NULL",
}

// restrictedReadersAdmitted ratifies the readers that carry none of the
// markers and still exclude a held row — through a fragment declared in
// another file — or that reach held rows on purpose. Each names the cost.
var restrictedReadersAdmitted = gatekit.Waive(map[string]string{
	"internal/modules/activities/capturelabel.go": "the classify backlog reads subject and body through ClassifyBacklogPredicate (pipelinefacts.go), which filters archived_at IS NULL and so excludes held rows by the restricted-implies-archived CHECK — the cost is that the predicate's archived filter now carries this obligation as well as its own",
})

// activityReaderScope is every non-test, non-generated file under internal/
// that reads the activity table by name.
var activityReaderScope = gatekit.Scope{
	Roots:   []string{"internal"},
	Subject: readsActivityTable,
	Exempt:  gatekit.Waive(map[string]string{}),
}

func readsActivityTable(path string, file *ast.File) bool {
	if strings.HasSuffix(path, "_gen.go") {
		return false
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && activityReadLiteral.MatchString(lit.Value) {
			found = true
		}
		return !found
	})
	return found
}

func TestEveryReaderOfTheActivityTableExcludesRestrictedRows(t *testing.T) {
	defer restrictedReadersAdmitted.AssertAllMatched(t)
	for _, src := range activityReaderScope.Files(t) {
		if fileCarriesAnyMarker(src.File, restrictedExclusionMarkers) || restrictedReadersAdmitted.Waived(t, src.Path) {
			continue
		}
		t.Errorf("%s reads the activity table and nothing in it excludes a held row — compose auth.ActivityScopeClause / ActivityAvailableClause, filter `restricted_at IS NULL` or `archived_at IS NULL`, or ratify the reader in restrictedReadersAdmitted with the cost stated (A165/ADR-0114 §2)", src.Path)
	}
}

// fileCarriesAnyMarker looks at identifiers and string literals alike: the
// scope clause is reached as a selector, the SQL fragments as literals.
func fileCarriesAnyMarker(file *ast.File, markers []string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		var text string
		switch node := n.(type) {
		case *ast.Ident:
			text = node.Name
		case *ast.BasicLit:
			text = node.Value
		default:
			return true
		}
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
