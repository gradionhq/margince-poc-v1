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
//   - it carries the shared row scope (auth.ActivityDiscoverClause / ActivityContentClause or one of the
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
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// activityReadLiteral matches a SQL string literal that reads the activity
// table by name. `activity_link`, `activity_participant` and the other
// activity_* tables are deliberately not matched: they carry no content.
//
// The pattern is gatekit's, shared with the sibling censuses: a matcher that
// stops seeing this tree's SQL judges nothing and reads exactly like a clean
// tree, so it has one place to be right and one place to be tested.
var activityReadLiteral = gatekit.TableReadPattern("activity")

// scopeMarkers are the shared gates that carry the availability test for the
// whole file: a reader reaching activity through one of them cannot see a held
// row whatever else the file contains. Matched file-wide, because the gate is
// a Go call rather than SQL and a file that makes it makes it for its reads.
var scopeMarkers = []string{
	"ActivityDiscoverClause", "ActivityContentClause", "ActivityAvailableClause",
	"EnsureActivityVisible", "EnsureActivityVisibleLive",
	"EnsureActivityContentVisible", "EnsureActivityContentVisibleLive",
	"correspondenceFloorPredicate", "handelsbriefShielded",
}

// literalMarkers exclude a held row in the FUNCTION that carries them, not
// merely somewhere in the file. That is the granularity the tree actually
// supports: this SQL is assembled from concatenated fragments and shared
// constants, so the `FROM activity` and its `archived_at IS NULL` are
// routinely different string literals in one query, and matching per literal
// would report green code as red. Per function is still far tighter than per
// file — an `archived_at IS NULL` belonging to a different query in the same
// file no longer answers for the activity read beside it, which is the false
// negative both reviews of this gate found.
//
// `archived_at IS NULL` counts because the schema makes restricted imply
// archived (activity_restricted_is_archived), and the guard refuses a write
// that would un-archive a held row.
//
// The column may be aliased (`a.archived_at`) or bare, so the match is on the
// column and its test rather than on one spelling of the pair — a gate that
// only recognised the unaliased form would report green code as red, which
// costs its own credibility faster than a miss does.
var literalMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)restricted_at`),
	regexp.MustCompile(`(?i)\barchived_at\s+IS\s+NULL`),
}

// restrictedReadersAdmitted ratifies the readers that carry none of the
// markers and still exclude a held row — through a fragment declared in
// another file — or that reach held rows on purpose. Each names the cost.
var restrictedReadersAdmitted = gatekit.Waive(map[string]string{
	"internal/compose/audiencerescope.go":         "the audience-change consumer reads the thread key (content by the activity policy) and the capture owner of the ONE activity whose audience just moved — deliberately, as a system principal, because both are exactly what narrowing the derived models needs, to NARROW what other readers may see — excluding a held row here would leave a legal-hold conversation's derived signals workspace-visible, the exact disclosure the consumer exists to remove. The cost is that a held activity's thread key and owner id reach this system principal",
	"internal/modules/activities/activity.go":     "replayedActivity resolves the (source_system, source_id) idempotency key and deliberately reads it UNSCOPED, because the question it asks is whether the key is taken — a scoped lookup would answer 'free' for a key held by a row out of scope, and the insert behind it would then fail on the unique index instead. It discloses nothing on its own: the id it finds is handed straight to readActivity (activityread.go), which carries the row scope, and a scope miss is turned into the same 409 the unique-index race returns — the key is taken, the record is not shown. The cost is that this reader depends on readActivity keeping its gate, in a different file since the write and read halves were split at the 500-line cap",
	"internal/modules/privacy/auditlog.go":        "the compliance read joins activity to evaluate ONE predicate — the audience arm the row's author set — and projects a single boolean from it. No activity column reaches the caller: the join's whole output is content_readable, which can only ever WITHHOLD an audit image, never reveal an activity. A held activity is therefore no more readable through this join than without it. The cost is that the audit IMAGE of a held activity stays readable to the admin, which is a pre-existing property of audit_log rather than of this join — audit_log is append-only and the hold is on the activity — and is filed rather than settled here, because making the compliance trail skip held rows is a decision about A165 and not a fix to the audience gap this join closes",
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
	return gatekit.FileReadsTable(path, file, activityReadLiteral)
}

// unguardedActivityReaders names each function in the file that reads the
// activity table and carries no exclusion of its own. A read at file scope —
// a package-level SQL constant — is attributed to the file, since a fragment
// has no function to belong to and is judged where it is declared.
func unguardedActivityReaders(file *ast.File) []string {
	// A file that reaches one of the shared auth gates is judged as a whole:
	// the gate is a Go call, and this tree routinely splits one query across a
	// reader function and the helper that builds its WHERE (ListActivitiesTx +
	// listActivitiesFilter, listOpenTasks + openTasksFilter). Asking such a
	// file per function would report the reader red while the scope it applies
	// sits ten lines below, which teaches the next engineer to distrust the
	// gate rather than to fix anything.
	if gatekit.CallsAny(file, scopeMarkers) {
		return nil
	}
	var offenders []string
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		reads := gatekit.DeclReads(decl, activityReadLiteral)
		if len(reads) == 0 {
			continue
		}
		guarded := false
		ast.Inspect(decl, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && matchesAny(lit.Value, literalMarkers) {
				guarded = true
			}
			return !guarded
		})
		if guarded {
			continue
		}
		name := "a package-level SQL fragment"
		if isFunc {
			name = fn.Name.Name
		}
		offenders = append(offenders, name+": "+gatekit.FirstLineOf(reads[0].SQL))
	}
	return offenders
}

func TestEveryReaderOfTheActivityTableExcludesRestrictedRows(t *testing.T) {
	defer restrictedReadersAdmitted.AssertAllMatched(t)
	for _, src := range activityReaderScope.Files(t) {
		if restrictedReadersAdmitted.Waived(t, src.Path) {
			continue
		}
		for _, offender := range unguardedActivityReaders(src.File) {
			t.Errorf("%s: %s reads the activity table and excludes no held row — compose auth.ActivityContentClause / ActivityDiscoverClause / ActivityAvailableClause, filter `restricted_at IS NULL` or `archived_at IS NULL`, or ratify the reader in restrictedReadersAdmitted with the cost stated (A165/ADR-0114 §2)", src.Path, offender)
		}
	}
}

func matchesAny(text string, markers []*regexp.Regexp) bool {
	for _, marker := range markers {
		if marker.MatchString(text) {
			return true
		}
	}
	return false
}
