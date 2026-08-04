// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder captures what a gate would have reported, so a test can assert on
// the report instead of failing with it. testing.TB is embedded for its
// unexported methods; every method this package calls is overridden.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recorder) Helper() {}

func (r *recorder) joined() string { return strings.Join(r.errs, "\n") }

func TestWaivedRatifiesAKnownSubjectAndRefusesAnUnknownOne(t *testing.T) {
	w := Waive(map[string]string{
		"record_grant": "no RBAC object may name it, so the row is refused for everyone forever",
	})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "record_grant") {
		t.Error("a ratified subject was not waived")
	}
	if w.Waived(rec, "person") {
		t.Error("an unratified subject was waived")
	}
	if len(rec.errs) != 0 {
		t.Errorf("a well-formed waiver reported: %s", rec.joined())
	}
}

func TestAReasonlessWaiverFailsWhereItIsReliedOn(t *testing.T) {
	w := Waive(map[string]string{"enrich": "enrich"})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "enrich") {
		t.Error("the subject stays waived — the reason is the defect, not the ratification")
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.joined(), "what it costs") {
		t.Errorf("a reason that only repeats its key was accepted: %s", rec.joined())
	}
}

// The floor measures the text a reason states, not the bytes it occupies: space
// is not an argument, and a subject long enough to clear the byte count is still
// only the subject.
func TestAReasonThatStatesNoCostIsRefusedHoweverLongItIs(t *testing.T) {
	const subject = "internal/modules/people/store.go"
	for _, probe := range []struct{ name, reason string }{
		{"blank padding", strings.Repeat(" ", 25)},
		{"mixed whitespace padding", "  \t\n   \n\t     \n            "},
		{"the subject restated", subject},
		{"the subject in another case", strings.ToUpper(subject)},
		{"the subject quoted", `"` + subject + `"`},
		{"the subject with a trailing period", subject + "."},
		{"the subject in parentheses", "(" + subject + ")"},
		{"the subject with padding around it", "  " + subject + " \n"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			w := Waive(map[string]string{subject: probe.reason})
			rec := &recorder{TB: t}
			if !w.Waived(rec, subject) {
				t.Error("the subject stays waived — the reason is the defect, not the ratification")
			}
			if len(rec.errs) != 1 || !strings.Contains(rec.joined(), "what it costs") {
				t.Errorf("a reason stating no cost was accepted: %s", rec.joined())
			}
		})
	}
}

// A reason may open with its subject and then explain it — the shape several
// live gates write — because naming the subject first and stating the cost after
// is a reason, and refusing it would push those gates to bury the subject.
func TestAReasonOpeningWithItsSubjectAndThenExplainingIsAccepted(t *testing.T) {
	for _, probe := range []struct{ subject, reason string }{
		{"enrich", "enrich — scrapeCompany fetches the target's own website through the web-read seam"},
		{"send_offer", "send — pinned for what the contract promises, though today's code performs no delivery"},
		{"arguments", "arguments names the JSON-RPC request shape rather than a REST contract property"},
	} {
		t.Run(probe.subject, func(t *testing.T) {
			w := Waive(map[string]string{probe.subject: probe.reason})
			rec := &recorder{TB: t}
			if !w.Waived(rec, probe.subject) {
				t.Fatal("a ratified subject was not waived")
			}
			if len(rec.errs) != 0 {
				t.Errorf("a reason that states a cost was refused: %s", rec.joined())
			}
		})
	}
}

func TestAWaiverMatchingNothingIsReportedAsStale(t *testing.T) {
	w := Waive(map[string]string{
		"live":  "reached by the lookup below, so it must not be reported",
		"stale": "describes a subject that no longer exists anywhere in the tree",
	})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "live") {
		t.Fatal("setup: the live subject was not waived")
	}
	w.AssertAllMatched(rec)
	if len(rec.errs) != 1 {
		t.Fatalf("want exactly one stale report, got %d: %s", len(rec.errs), rec.joined())
	}
	if !strings.Contains(rec.errs[0], "stale") {
		t.Errorf("the report names the wrong entry: %s", rec.errs[0])
	}
}

// Enumeration is deterministic because a gate that walks its waivers reports
// findings in that order, and an unstable order makes a failure unreadable.
func TestSubjectsEnumeratesInADeterministicOrder(t *testing.T) {
	w := Waive(map[string]string{
		"c": "the third subject, ratified for the reason stated right here",
		"a": "the first subject, ratified for the reason stated right here",
		"b": "the second subject, ratified for the reason stated here",
	})
	for range 8 {
		if got := w.Subjects(); strings.Join(got, ",") != "a,b,c" {
			t.Fatalf("Subjects() = %v, want a,b,c in every call", got)
		}
	}
}

// A gate keys its waivers by its own vocabulary's type, and two of them key by a
// named string type. The order is total over any such type, because a string
// subject has exactly one rendering and no two distinct subjects share it.
func TestSubjectsOfAWaiverSetKeyedByANamedStringTypeAreOrderedToo(t *testing.T) {
	type recordType string
	w := Waive(map[recordType]string{
		"organization": "the third subject, ratified for the reason stated right here",
		"deal":         "the first subject, ratified for the reason stated right here",
		"person":       "the second subject, ratified for the reason stated right here",
	})
	for range 8 {
		got := w.Subjects()
		if len(got) != 3 || got[0] != "deal" || got[1] != "organization" || got[2] != "person" {
			t.Fatalf("Subjects() = %v, want deal,organization,person in every call", got)
		}
	}
}

// A nil set behaves as an empty one. Gates hold these in case tables where a
// nil map means "no exceptions", and Go lets them read a nil map freely — the
// type has to preserve that, or converting such a gate replaces a passing test
// with a panic that takes its whole binary down.
func TestANilWaiversBehavesAsAnEmptySet(t *testing.T) {
	var w *Waivers[string]
	rec := &recorder{TB: t}
	if w.Waived(rec, "anything") {
		t.Error("a nil waiver set ratified a subject")
	}
	if got := w.Subjects(); len(got) != 0 {
		t.Errorf("Subjects() on a nil set = %v, want empty", got)
	}
	w.AssertAllMatched(rec)
	if len(rec.errs) != 0 {
		t.Errorf("a nil waiver set reported: %s", rec.joined())
	}
}

func TestWaiveCopiesItsInputSoALaterMutationCannotWidenTheSet(t *testing.T) {
	entries := map[string]string{"a": "ratified for the reason stated right here in full"}
	w := Waive(entries)
	entries["b"] = "smuggled in after ratification, which must not be honoured"
	rec := &recorder{TB: t}
	if w.Waived(rec, "b") {
		t.Error("a post-construction mutation widened the waiver set")
	}
}

// The probe trees below are written into t.TempDir() rather than testdata/,
// because several of this repo's gates walk the whole tree for every *.go file
// without excluding testdata — a probe package committed there would be
// license-checked, purity-checked and import-parsed as though it were product.
//
// obligated marks a file the probe Scope must judge; plain marks one it must
// not. The marker is a declaration rather than an import so the probe tree
// needs no resolvable module.
const (
	obligatedSource = "package %s\n\nfunc ObligatedSite() {}\n"
	plainSource     = "package %s\n\nfunc somethingElse() {}\n"
)

// writeProbeTree materializes rel-path → contents under a fresh temp dir and
// returns it, for use as a Scope's Tree.
func writeProbeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	tree := t.TempDir()
	for rel, contents := range files {
		path := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
	return tree
}

// declaresObligatedSite is the probe subject predicate.
func declaresObligatedSite(_ string, file *ast.File) bool {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ObligatedSite" {
			return true
		}
	}
	return false
}

// paths flattens a sweep result for comparison.
func paths(files []ParsedFile) string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return strings.Join(out, ",")
}

func TestFilesReturnsTheObligatedFilesUnderEveryRoot(t *testing.T) {
	tree := writeProbeTree(t, map[string]string{
		"inside/obligated.go":         fmt.Sprintf(obligatedSource, "inside"),
		"inside/plain.go":             fmt.Sprintf(plainSource, "inside"),
		"inside/obligated_test.go":    fmt.Sprintf(obligatedSource, "inside"),
		"inside/obligated_gen.go":     fmt.Sprintf(obligatedSource, "inside"),
		"inside/nested/obligated.go":  fmt.Sprintf(obligatedSource, "nested"),
		"alsoinside/obligated.go":     fmt.Sprintf(obligatedSource, "alsoinside"),
		"elsewhere/notevengo.txt":     "func ObligatedSite() {}\n",
		"elsewhere/alsoobligated.txt": "ignored",
	})
	scope := Scope{
		Tree:    tree,
		Roots:   []string{"inside", "alsoinside"},
		Subject: declaresObligatedSite,
	}
	rec := &recorder{TB: t}
	swept := scope.Files(rec)
	want := "alsoinside/obligated.go,inside/nested/obligated.go,inside/obligated.go"
	if got := paths(swept); got != want {
		t.Errorf("Files() = %q, want %q — a test, a generated or a non-Go file is not a subject", got, want)
	}
	for _, f := range swept {
		if f.File == nil {
			t.Errorf("%s came back without its syntax: a caller has nothing left to judge", f.Path)
		}
	}
	if len(rec.errs) != 0 {
		t.Errorf("a scope whose roots hold every subject reported: %s", rec.joined())
	}
}

// The vacuity floor: a root that yields nothing certifies nothing, and reads
// exactly like a root that yields no violation.
func TestFilesFailsWhenARootHoldsNoSubjectAtAll(t *testing.T) {
	tree := writeProbeTree(t, map[string]string{
		"inside/obligated.go": fmt.Sprintf(obligatedSource, "inside"),
		"barren/plain.go":     fmt.Sprintf(plainSource, "barren"),
	})
	scope := Scope{
		Tree:    tree,
		Roots:   []string{"inside", "barren"},
		Subject: declaresObligatedSite,
	}
	rec := &recorder{TB: t}
	scope.Files(rec)
	if len(rec.errs) != 1 {
		t.Fatalf("want exactly one vacuity report, got %d: %s", len(rec.errs), rec.joined())
	}
	if !strings.Contains(rec.errs[0], "barren") {
		t.Errorf("the vacuity report names the wrong root: %s", rec.errs[0])
	}
}

func TestFilesFailsWhenASubjectLivesOutsideEveryRoot(t *testing.T) {
	tree := writeProbeTree(t, map[string]string{
		"inside/obligated.go":  fmt.Sprintf(obligatedSource, "inside"),
		"outside/obligated.go": fmt.Sprintf(obligatedSource, "outside"),
	})
	scope := Scope{
		Tree:    tree,
		Roots:   []string{"inside"},
		Subject: declaresObligatedSite,
	}
	rec := &recorder{TB: t}
	if got := paths(scope.Files(rec)); got != "inside/obligated.go" {
		t.Errorf("Files() = %q, want only the in-root subject", got)
	}
	if len(rec.errs) != 1 {
		t.Fatalf("want exactly one out-of-root report, got %d: %s", len(rec.errs), rec.joined())
	}
	if !strings.Contains(rec.errs[0], "outside/obligated.go") {
		t.Errorf("the report names the wrong file: %s", rec.errs[0])
	}
}

// A Scope missing either half of its claim sweeps for nothing, and says which
// half is missing — neither guard needs a tree, because there is no question yet
// to ask of one.
func TestFilesRefusesAScopeThatClaimsNothing(t *testing.T) {
	for _, probe := range []struct {
		name  string
		scope Scope
		says  string
	}{
		{
			name:  "no subject predicate",
			scope: Scope{Roots: []string{"inside"}},
			says:  "sweeps for nothing",
		},
		{
			name:  "no roots",
			scope: Scope{Subject: declaresObligatedSite},
			says:  "claims no coverage",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			rec := &recorder{TB: t}
			if got := probe.scope.Files(rec); got != nil {
				t.Errorf("Files() = %v, want no subjects from a scope that claims nothing", paths(got))
			}
			if len(rec.errs) != 1 {
				t.Fatalf("want exactly one report, got %d: %s", len(rec.errs), rec.joined())
			}
			if !strings.Contains(rec.errs[0], probe.says) {
				t.Errorf("the report does not say %q: %s", probe.says, rec.errs[0])
			}
		})
	}
}

// A root that is not a subtree is refused at the root, not diagnosed as a
// missing obligation: swept, "." would report every subject out-of-root and the
// root itself vacuous, which reads as a tree-wide gap when the tree is fine.
func TestFilesRefusesARootThatNamesNoSubtree(t *testing.T) {
	for _, probe := range []struct {
		name string
		root string
		says string
	}{
		{name: "the sweep universe itself", root: ".", says: "covers the whole sweep universe"},
		{name: "an empty root", root: "", says: "covers the whole sweep universe"},
		{name: "an absolute root", root: "/", says: "is an absolute path"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			tree := writeProbeTree(t, map[string]string{
				"inside/obligated.go": fmt.Sprintf(obligatedSource, "inside"),
			})
			scope := Scope{Tree: tree, Roots: []string{probe.root}, Subject: declaresObligatedSite}
			rec := &recorder{TB: t}
			if got := scope.Files(rec); got != nil {
				t.Errorf("Files() = %v, want no subjects from a refused root", paths(got))
			}
			if len(rec.errs) != 1 {
				t.Fatalf("want exactly one report naming the root, got %d: %s", len(rec.errs), rec.joined())
			}
			if !strings.Contains(rec.errs[0], probe.says) {
				t.Errorf("the report does not say %q: %s", probe.says, rec.errs[0])
			}
		})
	}
}

// A root named twice is one root: it is reported once, and the sweep it proves
// is the same sweep either way.
func TestARootNamedTwiceIsReportedOnce(t *testing.T) {
	tree := writeProbeTree(t, map[string]string{
		"inside/obligated.go":  fmt.Sprintf(obligatedSource, "inside"),
		"outside/obligated.go": fmt.Sprintf(obligatedSource, "outside"),
	})
	scope := Scope{
		Tree:    tree,
		Roots:   []string{"inside", "inside"},
		Subject: declaresObligatedSite,
	}
	rec := &recorder{TB: t}
	if got := paths(scope.Files(rec)); got != "inside/obligated.go" {
		t.Errorf("Files() = %q, want only the in-root subject", got)
	}
	if len(rec.errs) != 1 {
		t.Fatalf("want exactly one out-of-root report, got %d: %s", len(rec.errs), rec.joined())
	}
	if !strings.Contains(rec.errs[0], "(inside)") {
		t.Errorf("the report lists the root more than once: %s", rec.errs[0])
	}
}

func TestAnExemptedOutsideSubjectIsAcceptedAndCountsAsMatched(t *testing.T) {
	tree := writeProbeTree(t, map[string]string{
		"inside/obligated.go":  fmt.Sprintf(obligatedSource, "inside"),
		"outside/obligated.go": fmt.Sprintf(obligatedSource, "outside"),
	})
	exempt := Waive(map[string]string{
		"outside/obligated.go": "holds the marker but answers to another gate, so this one must not judge it",
	})
	scope := Scope{
		Tree:    tree,
		Roots:   []string{"inside"},
		Subject: declaresObligatedSite,
		Exempt:  exempt,
	}
	rec := &recorder{TB: t}
	scope.Files(rec)
	if len(rec.errs) != 0 {
		t.Errorf("a ratified out-of-root subject was reported anyway: %s", rec.joined())
	}
	exempt.AssertAllMatched(rec)
	if len(rec.errs) != 0 {
		t.Errorf("the sweep did not mark the exemption it relied on: %s", rec.joined())
	}
}
