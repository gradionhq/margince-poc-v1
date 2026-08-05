// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package static

import (
	"strings"
	"testing"
)

// lintSource parses one in-memory file under the given name and returns its
// findings — the name drives the _test.go / domain-path flags, so a case can
// exercise those code paths without touching disk.
func lintSource(t *testing.T, name, src string) []Finding {
	t.Helper()
	fc, ok := newFileContext(name, []byte(src), "internal/modules/")
	if !ok {
		t.Fatalf("newFileContext(%s) failed to parse", name)
	}
	var out []Finding
	for _, chk := range checks {
		for _, f := range chk.run(fc, Config{}.withDefaults()) {
			if !fc.waived(f.Check, f.Line) {
				out = append(out, f)
			}
		}
	}
	return append(out, fc.waiverHygiene()...)
}

func counts(findings []Finding, check string) int {
	n := 0
	for _, f := range findings {
		if f.Check == check {
			n++
		}
	}
	return n
}

func TestSwallowedErrors_flagsBlankAssignedCall(t *testing.T) {
	src := `package p
func run() { _ = doThing() }
func doThing() error { return nil }`
	if got := counts(lintSource(t, "p.go", src), "swallowed-errors"); got != 1 {
		t.Fatalf("swallowed-errors = %d, want 1", got)
	}
}

func TestSwallowedErrors_ignoresNonCallAndWaived(t *testing.T) {
	src := `package p
func run() {
	_ = x
	_ = doThing() //craft:ignore swallowed-errors close is best-effort
}
var x int
func doThing() error { return nil }`
	if got := counts(lintSource(t, "p.go", src), "swallowed-errors"); got != 0 {
		t.Fatalf("swallowed-errors = %d, want 0 (ident discard + waived call)", got)
	}
}

func TestTestSleep_onlyInTestFiles(t *testing.T) {
	src := `package p
import "time"
func TestX(t *T) { time.Sleep(time.Second); t.Fatal("x") }
type T struct{}`
	if got := counts(lintSource(t, "p.go", src), "test-sleep"); got != 0 {
		t.Fatalf("non-test file flagged for sleep: %d", got)
	}
	if got := counts(lintSource(t, "p_test.go", src), "test-sleep"); got != 1 {
		t.Fatalf("test-sleep = %d, want 1", got)
	}
}

func TestAssertionFree(t *testing.T) {
	src := `package p
import "testing"
func TestReal(t *testing.T) { if 1 != 1 { t.Fatal("no") } }
func TestDelegates(t *testing.T) { check(t) }
func check(t *testing.T) {}
func TestEmpty(t *testing.T) { _ = 1 }`
	if got := counts(lintSource(t, "p_test.go", src), "assertion-free-test"); got != 1 {
		t.Fatalf("assertion-free-test = %d, want 1 (only TestEmpty)", got)
	}
}

func TestBooleanTrap(t *testing.T) {
	src := `package p
func good(a bool) {}
func bad(a, b bool) {}
func alsoBad(a bool, n int, b bool) {}`
	if got := counts(lintSource(t, "p.go", src), "boolean-trap"); got != 2 {
		t.Fatalf("boolean-trap = %d, want 2", got)
	}
}

func TestNakedAny_flagsSignatureNotTypeParam(t *testing.T) {
	src := `package p
func generic[T any](v T) {}
func loose(v any) {}
func alsoLoose() interface{} { return nil }`
	if got := counts(lintSource(t, "p.go", src), "naked-any"); got != 2 {
		t.Fatalf("naked-any = %d, want 2 (generic constraint must not count)", got)
	}
}

func TestPanicInDomain(t *testing.T) {
	src := `package deals
func Advance() { panic("boom") }
func MustLoad() { panic("ok in a Must constructor") }`
	if got := counts(lintSource(t, "internal/modules/deals/svc.go", src), "panic-in-domain"); got != 1 {
		t.Fatalf("panic-in-domain in module = %d, want 1", got)
	}
	if got := counts(lintSource(t, "internal/platform/x/svc.go", src), "panic-in-domain"); got != 0 {
		t.Fatalf("panic-in-domain outside module = %d, want 0", got)
	}
}

func TestTodoWithoutRef(t *testing.T) {
	src := `package p
// TODO fix this later
// TODO(#42) tracked
func f() {}`
	if got := counts(lintSource(t, "p.go", src), "todo-without-ref"); got != 1 {
		t.Fatalf("todo-without-ref = %d, want 1", got)
	}
}

func TestWaiverHygiene_flagsReasonlessDirective(t *testing.T) {
	src := `package p
func f() {
	_ = g() //craft:ignore swallowed-errors
}
func g() error { return nil }`
	f := lintSource(t, "p.go", src)
	if got := counts(f, "waiver-hygiene"); got != 1 {
		t.Fatalf("waiver-hygiene = %d, want 1 (reasonless waiver)", got)
	}
	if got := counts(f, "swallowed-errors"); got != 0 {
		t.Fatalf("swallowed-errors = %d, want 0 (directive still suppresses)", got)
	}
}

func TestVerdict_onlyBlockerBlocks(t *testing.T) {
	f := lintSource(t, "p.go", "package p\nfunc bad(a, b bool) {}")
	if v := verdict(f, false); v != "PASS" {
		t.Fatalf("verdict with only MAJOR = %q, want PASS", v)
	}
	if v := verdict(f, true); v != "BLOCK" {
		t.Fatalf("verdict -strict with MAJOR = %q, want BLOCK", v)
	}
}

// sizeSrc returns a package with one function whose body is n lines.
func sizeSrc(n int) string {
	var b strings.Builder
	b.WriteString("package p\n\nvar x int\n\nfunc run() {\n")
	for i := 0; i < n; i++ {
		b.WriteString("\tx++\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func TestLongFunc_testFilesGetTheRelaxedCeiling(t *testing.T) {
	src := sizeSrc(120) // over the 80 prod ceiling, under the 160 test one
	if got := counts(lintSource(t, "p.go", src), "long-func"); got != 1 {
		t.Fatalf("prod file at 120 body lines: long-func = %d, want 1", got)
	}
	if got := counts(lintSource(t, "p_test.go", src), "long-func"); got != 0 {
		t.Fatalf("test file at 120 body lines: long-func = %d, want 0", got)
	}
	if got := counts(lintSource(t, "p_test.go", sizeSrc(170)), "long-func"); got != 1 {
		t.Fatalf("test file at 170 body lines: long-func = %d, want 1", got)
	}
}

// The ceiling measures CODE, not text. golangci's funlen is configured
// ignore-comments in this repo, so a function that passes there must not block
// here — and a gate that charged for explanation would push a reader-hostile
// trade: delete the why, or hide the lines behind an abstraction nobody needs.
func TestLongFunc_countsCodeNotComments(t *testing.T) {
	// 60 code lines and 60 comment lines: 120 raw body lines, 60 of code.
	var b strings.Builder
	b.WriteString("package p\n\nvar x int\n\nfunc run() {\n")
	for range 60 {
		b.WriteString("\t// why this line is here\n\tx++\n")
	}
	b.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", b.String()), "long-func"); got != 0 {
		t.Fatalf("60 code lines under 60 comment lines: long-func = %d, want 0 — comments are not length", got)
	}

	// The same 120 raw lines, all code, must still be flagged: the change is what
	// counts as a line, not the ceiling.
	if got := counts(lintSource(t, "p.go", sizeSrc(120)), "long-func"); got != 1 {
		t.Fatalf("120 code lines: long-func = %d, want 1", got)
	}
}

// A comment sharing a line with code does not make that line free.
func TestLongFunc_aTrailingCommentDoesNotDiscountItsCodeLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nvar x int\n\nfunc run() {\n")
	for range 90 {
		b.WriteString("\tx++ // still a code line\n")
	}
	b.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", b.String()), "long-func"); got != 1 {
		t.Fatalf("90 code lines carrying trailing comments: long-func = %d, want 1", got)
	}
}

// A block comment spanning lines is comment-only for every line it covers.
func TestLongFunc_blockCommentLinesAreNotCode(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nvar x int\n\nfunc run() {\n")
	b.WriteString("\t/*\n")
	for range 100 {
		b.WriteString("\t   prose, not code\n")
	}
	b.WriteString("\t*/\n")
	for range 20 {
		b.WriteString("\tx++\n")
	}
	b.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", b.String()), "long-func"); got != 0 {
		t.Fatalf("20 code lines under a 102-line block comment: long-func = %d, want 0", got)
	}
}

// A block comment's boundaries are judged one at a time. Code sharing the
// opening or closing line keeps THAT line as code without costing the prose
// between them — bailing out of the whole block would count its interior as
// code, which is the false positive this check exists to avoid.
func TestLongFunc_blockCommentBoundariesAreJudgedIndependently(t *testing.T) {
	// 100 prose lines whose closing line carries code, plus 20 plain code lines:
	// 21 code lines in total, well under the ceiling.
	var closeLine strings.Builder
	closeLine.WriteString("package p\n\nvar x int\n\nfunc run() {\n\t/*\n")
	for range 100 {
		closeLine.WriteString("\t   prose\n")
	}
	closeLine.WriteString("\t*/ x++\n")
	for range 20 {
		closeLine.WriteString("\tx++\n")
	}
	closeLine.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", closeLine.String()), "long-func"); got != 0 {
		t.Errorf("code on the CLOSING line: long-func = %d, want 0 — the prose above it is not code", got)
	}

	// The same shape with code on the OPENING line instead.
	var openLine strings.Builder
	openLine.WriteString("package p\n\nvar x int\n\nfunc run() {\n\tx++ /*\n")
	for range 100 {
		openLine.WriteString("\t   prose\n")
	}
	openLine.WriteString("\t*/\n")
	for range 20 {
		openLine.WriteString("\tx++\n")
	}
	openLine.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", openLine.String()), "long-func"); got != 0 {
		t.Errorf("code on the OPENING line: long-func = %d, want 0 — the prose below it is not code", got)
	}
}

// A one-line block comment with code after it is a code line, not prose.
func TestLongFunc_aClosedBlockFollowedByCodeIsACodeLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nvar x int\n\nfunc run() {\n")
	for range 90 {
		b.WriteString("\t/* why */ x++\n")
	}
	b.WriteString("}\n")
	if got := counts(lintSource(t, "p.go", b.String()), "long-func"); got != 1 {
		t.Fatalf("90 code lines each prefixed by a closed block comment: long-func = %d, want 1", got)
	}
}

func TestLargeFile_testFilesGetTheRelaxedCeiling(t *testing.T) {
	src := "package p\n" + strings.Repeat("// filler\n", 600)
	if got := counts(lintSource(t, "p.go", src), "large-file"); got != 1 {
		t.Fatalf("prod file at 600 lines: large-file = %d, want 1", got)
	}
	if got := counts(lintSource(t, "p_test.go", src), "large-file"); got != 0 {
		t.Fatalf("test file at 600 lines: large-file = %d, want 0", got)
	}
	src = "package p\n" + strings.Repeat("// filler\n", 1100)
	if got := counts(lintSource(t, "p_test.go", src), "large-file"); got != 1 {
		t.Fatalf("test file at 1100 lines: large-file = %d, want 1", got)
	}
}

func TestGeneratedFilesSkipped(t *testing.T) {
	src := "// Code generated by tool. DO NOT EDIT.\npackage p\nfunc f() { _ = g() }\nfunc g() error { return nil }"
	if _, ok := newFileContext("p_gen_like.go", []byte(src), "internal/modules/"); ok {
		t.Fatal("generated file should be skipped")
	}
}
